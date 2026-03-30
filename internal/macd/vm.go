//go:build darwin

package macd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/google/uuid"
)

const (
	guestReadyTimeout = 60 * time.Second

	// Vsock ports for guest ↔ host communication.
	vsockConfigPort    = 1024
	vsockControlPort   = 1028
	vsockStatsPort     = 1026
	vsockTCPBridgePort = 1025

	// Guest networking. Apple VZ NAT presents the host bridge on 192.168.64.1.
	guestNATCIDR = "192.168.64.2/24"
	guestGW      = "192.168.64.1"
)

// VM represents a running microVM instance.
type localVM struct {
	id        string
	name      string
	hostGroup string // "box" or "temp"
	status    string

	vm            *vz.VirtualMachine
	vsock         *vz.VirtioSocketDevice
	controlDialer func() (net.Conn, error)
	hostPort      int
	appDir        string

	mu        sync.Mutex
	cancel    context.CancelFunc
	readyCh   chan struct{}
	readyOnce sync.Once
	stopped   chan struct{}
}

// Manager owns and manages all local VMs.
type Manager struct {
	cfg    *Config
	store  *Store
	logger *slog.Logger

	mu  sync.Mutex
	vms map[string]*localVM // keyed by VM id
}

// NewManager creates a VM manager backed by the given config and store.
func NewManager(cfg *Config, store *Store) *Manager {
	return &Manager{
		cfg:    cfg,
		store:  store,
		logger: slog.Default(),
		vms:    make(map[string]*localVM),
	}
}

// StartBox boots (or resumes) the persistent box VM.
func (m *Manager) StartBox(ctx context.Context) (*VM, error) {
	vm, err := m.store.EnsureBox(m.cfg.Box.Name, m.cfg.Box.VCPUs, m.cfg.Box.MemoryMB, m.cfg.Box.DiskMB)
	if err != nil {
		return nil, fmt.Errorf("ensure box: %w", err)
	}

	m.mu.Lock()
	if existing, ok := m.vms[vm.ID]; ok {
		if existing.status == "running" {
			m.mu.Unlock()
			return existing.ToPublic(), nil
		}
	}
	m.mu.Unlock()

	v, err := m.startVM(ctx, vm, "box")
	if err != nil {
		return nil, err
	}
	return v.ToPublic(), nil
}

// StopBox stops the box VM.
func (m *Manager) StopBox(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, err := m.store.Box()
	if err != nil {
		return err
	}
	if vm == nil {
		return nil
	}

	if v, ok := m.vms[vm.ID]; ok {
		v.cancel()
		<-v.stopped
	}
	return m.store.UpdateVMStatus(vm.ID, "stopped")
}

// ListVMs returns all VMs.
func (m *Manager) ListVMs() ([]VM, error) {
	return m.store.ListVMs("")
}

// GetBox returns the box VM info.
func (m *Manager) GetBox() (*VM, error) {
	return m.store.Box()
}

// StartTemp creates and starts an ephemeral temp VM.
func (m *Manager) StartTemp(ctx context.Context, template string) (*VM, error) {
	id := uuid.New().String()
	vm := &VM{
		ID:        id,
		Name:      "temp-" + id[:8],
		HostGroup: "temp",
		Status:    "stopped",
		Arch:      "arm64",
		VCPUs:     m.cfg.Temp.VCPUs,
		MemoryMB:  m.cfg.Temp.MemoryMB,
		DiskMB:    m.cfg.Temp.DiskMB,
		CreatedAt: time.Now(),
		Template:  template,
	}

	if template != "" {
		return m.startTempFromTemplate(ctx, vm, template)
	}

	return m.startTempFromScratch(ctx, vm)
}

// startTempFromTemplate starts a temp VM by cloning from a template.
func (m *Manager) startTempFromTemplate(ctx context.Context, vm *VM, templateID string) (*VM, error) {
	return m.startTempFromScratch(ctx, vm)
}

// startTempFromScratch starts a temp VM using the rootfs at the daemon's initramfs path.
func (m *Manager) startTempFromScratch(ctx context.Context, vm *VM) (*VM, error) {
	if err := m.store.CreateVM(*vm); err != nil {
		return nil, fmt.Errorf("create vm record: %w", err)
	}

	v, err := m.startVM(ctx, vm, "temp")
	if err != nil {
		m.store.UpdateVMStatus(vm.ID, "stopped")
		return nil, err
	}
	return v.ToPublic(), nil
}

// startVM boots a Linux VM using Apple Virtualization Framework.
func (m *Manager) startVM(ctx context.Context, vm *VM, hostGroup string) (*localVM, error) {
	cfg := m.resolveGroupConfig(hostGroup)

	kernelPath := m.cfg.Daemon.KernelPath
	if _, err := os.Stat(kernelPath); err != nil {
		return nil, fmt.Errorf("kernel not found at %s: %w", kernelPath, err)
	}

	initramfsPath := m.cfg.Daemon.InitramfsPath
	if _, err := os.Stat(initramfsPath); err != nil {
		return nil, fmt.Errorf("initramfs not found at %s: %w", initramfsPath, err)
	}

	// Create a working directory for this VM's rootfs.
	vmDir := filepath.Join(DaemonDir(), "vms", vm.ID)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return nil, fmt.Errorf("create vm dir: %w", err)
	}

	// Use a minimal rootfs or export from a base image if available.
	rootfsDir := filepath.Join(vmDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		return nil, fmt.Errorf("create rootfs dir: %w", err)
	}

	if err := seedRootFS(rootfsDir, m.cfg.Daemon.RootfsArchivePath); err != nil {
		return nil, fmt.Errorf("seed rootfs: %w", err)
	}
	if empty, err := dirEmpty(rootfsDir); err != nil {
		return nil, fmt.Errorf("check rootfs dir: %w", err)
	} else if empty {
		// Create a placeholder so the directory is not empty.
		if err := os.WriteFile(filepath.Join(rootfsDir, ".kindling"), []byte{}, 0644); err != nil {
			return nil, fmt.Errorf("create rootfs marker: %w", err)
		}
	}

	return m.bootVM(ctx, vm, hostGroup, cfg, kernelPath, initramfsPath, rootfsDir)
}

func (m *Manager) resolveGroupConfig(hostGroup string) GroupConfig {
	if hostGroup == "box" {
		return GroupConfig{
			VCPUs:         m.cfg.Box.VCPUs,
			MemoryMB:      m.cfg.Box.MemoryMB,
			DiskMB:        m.cfg.Box.DiskMB,
			SharedFolders: m.cfg.Box.SharedFolders,
			Rosetta:       m.cfg.Box.Rosetta,
		}
	}
	return GroupConfig{
		VCPUs:         m.cfg.Temp.VCPUs,
		MemoryMB:      m.cfg.Temp.MemoryMB,
		DiskMB:        m.cfg.Temp.DiskMB,
		SharedFolders: m.cfg.Temp.SharedFolders,
		Rosetta:       m.cfg.Temp.Rosetta,
	}
}

type GroupConfig struct {
	VCPUs         int
	MemoryMB      int
	DiskMB        int
	SharedFolders []SharedFolderConfig
	Rosetta       bool
}

// bootVM creates and starts a Linux VM using Apple Virtualization Framework.
func (m *Manager) bootVM(ctx context.Context, vm *VM, hostGroup string, cfg GroupConfig, kernelPath, initramfsPath, rootfsDir string) (*localVM, error) {
	bootLoader, err := vz.NewLinuxBootLoader(
		kernelPath,
		vz.WithInitrd(initramfsPath),
		vz.WithCommandLine("console=hvc0"),
	)
	if err != nil {
		return nil, fmt.Errorf("create boot loader: %w", err)
	}

	vmCfg, err := vz.NewVirtualMachineConfiguration(
		bootLoader,
		uint(cfg.VCPUs),
		uint64(cfg.MemoryMB)*1024*1024,
	)
	if err != nil {
		return nil, fmt.Errorf("create vm config: %w", err)
	}

	// Network: NAT (no TAP/TUN needed on macOS).
	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("create NAT attachment: %w", err)
	}
	netDev, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		return nil, fmt.Errorf("create network config: %w", err)
	}
	vmCfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netDev})

	// Serial console (discard output).
	devNullR, _ := os.Open(os.DevNull)
	devNullW, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	serialAttachment, err := vz.NewFileHandleSerialPortAttachment(devNullR, devNullW)
	if err != nil {
		return nil, fmt.Errorf("create serial attachment: %w", err)
	}
	consoleCfg, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
	if err != nil {
		return nil, fmt.Errorf("create console config: %w", err)
	}
	vmCfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{consoleCfg})

	// Vsock for guest ↔ host communication.
	vsockCfg, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("create vsock config: %w", err)
	}
	vmCfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsockCfg})

	// Shared directories (VirtIO FileSystem).
	var fsDevices []vz.DirectorySharingDeviceConfiguration

	// Rootfs at /app.
	rootfsShare, err := vz.NewSharedDirectory(rootfsDir, false)
	if err != nil {
		return nil, fmt.Errorf("create rootfs share: %w", err)
	}
	rootfsTagged, err := vz.NewSingleDirectoryShare(rootfsShare)
	if err != nil {
		return nil, fmt.Errorf("create rootfs tagged share: %w", err)
	}
	rootfsDevice, err := vz.NewVirtioFileSystemDeviceConfiguration("app")
	if err != nil {
		return nil, fmt.Errorf("create rootfs fs device: %w", err)
	}
	rootfsDevice.SetDirectoryShare(rootfsTagged)
	fsDevices = append(fsDevices, rootfsDevice)

	// Additional shared folders.
	for _, sf := range cfg.SharedFolders {
		if sf.HostPath == "" || sf.GuestPath == "" {
			continue
		}
		hostDir, err := expandPath(sf.HostPath)
		if err != nil || !dirExists(hostDir) {
			m.logger.Debug("skipping missing shared folder", "path", hostDir)
			continue
		}
		share, err := vz.NewSharedDirectory(hostDir, false)
		if err != nil {
			m.logger.Debug("shared folder error", "path", hostDir, "err", err)
			continue
		}
		tagged, err := vz.NewSingleDirectoryShare(share)
		if err != nil {
			continue
		}
		device, err := vz.NewVirtioFileSystemDeviceConfiguration(sf.GuestPath)
		if err != nil {
			continue
		}
		device.SetDirectoryShare(tagged)
		fsDevices = append(fsDevices, device)
	}

	// Entropy device.
	entropyCfg, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("create entropy config: %w", err)
	}
	vmCfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyCfg})

	// Rosetta support (x86_64 binary translation on Apple Silicon).
	if cfg.Rosetta {
		if err := enableRosetta(vmCfg, fsDevices); err != nil {
			m.logger.Warn("rosetta not available", "err", err)
		}
	} else if len(fsDevices) > 0 {
		vmCfg.SetDirectorySharingDevicesVirtualMachineConfiguration(fsDevices)
	}

	if _, err := vmCfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate vm config: %w", err)
	}

	vzVM, err := vz.NewVirtualMachine(vmCfg)
	if err != nil {
		return nil, fmt.Errorf("create vm: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	lvm := &localVM{
		id:        vm.ID,
		name:      vm.Name,
		hostGroup: hostGroup,
		status:    "running",
		vm:        vzVM,
		appDir:    rootfsDir,
		readyCh:   make(chan struct{}),
		stopped:   make(chan struct{}),
		cancel:    cancel,
	}

	// Set up vsock listener BEFORE starting the VM.
	var vsockDev *vz.VirtioSocketDevice
	socketDevices := vzVM.SocketDevices()
	if len(socketDevices) > 0 {
		vsockDev = socketDevices[0]
		lvm.vsock = vsockDev
		lvm.controlDialer = func() (net.Conn, error) {
			return vsockDev.Connect(vsockControlPort)
		}
		listener, err := vsockDev.Listen(vsockConfigPort)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("vsock listen: %w", err)
		}

		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go lvm.handleVsockConn(conn)
			}
		}()
	}

	if err := vzVM.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start vm: %w", err)
	}

	m.mu.Lock()
	m.vms[vm.ID] = lvm
	m.mu.Unlock()

	if err := m.store.UpdateVMStatus(vm.ID, "running"); err != nil {
		m.logger.Warn("failed to update vm status", "id", vm.ID, "err", err)
	}

	// Wait for guest to become ready.
	select {
	case <-lvm.readyCh:
	case <-ctx.Done():
		cancel()
		return nil, fmt.Errorf("start cancelled")
	case <-time.After(guestReadyTimeout):
		cancel()
		return nil, fmt.Errorf("guest ready timed out after %s", guestReadyTimeout)
	}

	if vsockDev == nil {
		cancel()
		return nil, fmt.Errorf("vsock device required")
	}

	// Set up localhost TCP forwarding.
	hostLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("host tcp listen: %w", err)
	}
	lvm.hostPort = hostLn.Addr().(*net.TCPAddr).Port
	if err := m.store.UpdateVMHostPort(vm.ID, lvm.hostPort); err != nil {
		m.logger.Warn("failed to update vm host port", "id", vm.ID, "err", err)
	}

	go lvm.forwardHostTCP(runCtx, hostLn, vsockDev)
	go lvm.monitorVM(runCtx)

	m.logger.Info("vm started",
		"id", vm.ID,
		"name", vm.Name,
		"host_group", hostGroup,
		"vcpus", cfg.VCPUs,
		"memory_mb", cfg.MemoryMB,
		"host_port", lvm.hostPort,
	)

	return lvm, nil
}

func enableRosetta(vmCfg *vz.VirtualMachineConfiguration, fsDevices []vz.DirectorySharingDeviceConfiguration) error {
	// Check if Rosetta is available.
	if vz.LinuxRosettaDirectoryShareAvailability() == vz.LinuxRosettaAvailabilityNotSupported {
		return fmt.Errorf("rosetta not available")
	}
	rosettaShare, err := vz.NewLinuxRosettaDirectoryShare()
	if err != nil {
		return err
	}
	rosettaDevice, err := vz.NewVirtioFileSystemDeviceConfiguration("rosetta")
	if err != nil {
		return err
	}
	rosettaDevice.SetDirectoryShare(rosettaShare)
	vmCfg.SetDirectorySharingDevicesVirtualMachineConfiguration(append(fsDevices, rosettaDevice))
	return nil
}

func expandPath(p string) (string, error) {
	p = os.ExpandEnv(p)
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	return filepath.Abs(p)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func dirEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}

func seedRootFS(rootfsDir, archivePath string) error {
	if strings.TrimSpace(archivePath) == "" {
		return nil
	}
	empty, err := dirEmpty(rootfsDir)
	if err != nil {
		return err
	}
	if !empty {
		return nil
	}
	archivePath = os.ExpandEnv(archivePath)
	if strings.HasPrefix(archivePath, "~/") {
		home, _ := os.UserHomeDir()
		archivePath = filepath.Join(home, archivePath[2:])
	}
	if _, err := os.Stat(archivePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return extractTarGz(rootfsDir, archivePath)
}

func extractTarGz(destDir, archivePath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(filepath.Clean(hdr.Name), string(filepath.Separator))
		if name == "." || name == "" {
			continue
		}
		target := filepath.Join(destDir, name)
		rel, err := filepath.Rel(destDir, target)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "..") {
			return fmt.Errorf("invalid rootfs archive path: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

func (lvm *localVM) handleVsockConn(conn net.Conn) {
	defer conn.Close()

	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return
	}
	req := string(buf[:n])

	if strings.Contains(req, "GET /config") {
		guestCfg := map[string]any{
			"mode":     lvm.hostGroup,
			"ip_addr":  guestNATCIDR,
			"ip_gw":    guestGW,
			"hostname": lvm.name,
			"port":     3000,
		}
		cfgBytes, _ := json.Marshal(guestCfg)
		resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			len(cfgBytes), string(cfgBytes))
		conn.Write([]byte(resp))
		return
	}

	if strings.Contains(req, "POST /logs") {
		conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
		return
	}

	if strings.Contains(req, "POST /ready") {
		lvm.readyOnce.Do(func() { close(lvm.readyCh) })
		conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
		return
	}

	conn.Write([]byte("HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"))
}

func (lvm *localVM) forwardHostTCP(ctx context.Context, hostLn net.Listener, vsockDev *vz.VirtioSocketDevice) {
	go func() {
		<-ctx.Done()
		hostLn.Close()
	}()

	for {
		tcpConn, err := hostLn.Accept()
		if err != nil {
			return
		}
		go lvm.relayTCP(tcpConn, vsockDev)
	}
}

func (lvm *localVM) relayTCP(tcpConn net.Conn, vsockDev *vz.VirtioSocketDevice) {
	defer tcpConn.Close()

	guestConn, err := vsockDev.Connect(vsockTCPBridgePort)
	if err != nil {
		return
	}
	defer guestConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(guestConn, tcpConn) }()
	go func() { defer wg.Done(); io.Copy(tcpConn, guestConn) }()
	wg.Wait()
}

func (lvm *localVM) monitorVM(ctx context.Context) {
	<-ctx.Done()
	lvm.mu.Lock()
	lvm.status = "stopped"
	lvm.mu.Unlock()

	if lvm.vm.CanStop() {
		lvm.vm.Stop()
	}
	close(lvm.stopped)
}

// Stop stops a VM by id.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	lvm, ok := m.vms[id]
	m.mu.Unlock()

	if !ok {
		// Not running, just mark stopped.
		return m.store.UpdateVMStatus(id, "stopped")
	}

	lvm.cancel()
	<-lvm.stopped
	return m.store.UpdateVMStatus(id, "stopped")
}

// Delete stops and removes a VM.
func (m *Manager) Delete(id string) error {
	if err := m.Stop(id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.vms, id)
	m.mu.Unlock()

	// Remove VM directory.
	vmDir := filepath.Join(DaemonDir(), "vms", id)
	os.RemoveAll(vmDir)

	return m.store.DeleteVM(id)
}

// Exec runs a command inside a VM.
func (m *Manager) Exec(ctx context.Context, id string, argv []string, cwd string, extraEnv []string) (int, string, error) {
	m.mu.Lock()
	lvm, ok := m.vms[id]
	m.mu.Unlock()

	if !ok {
		return 0, "", fmt.Errorf("vm not running: %s", id)
	}

	conn, err := lvm.openControlConn()
	if err != nil {
		return 0, "", fmt.Errorf("vsock connect: %w", err)
	}
	defer conn.Close()

	if cwd == "" {
		cwd = "/"
	}

	return execGuestHTTP(ctx, conn, argv, cwd, extraEnv)
}

// OpenShell opens a proxied interactive shell stream to the guest control server.
func (m *Manager) OpenShell(ctx context.Context, id string, argv []string, cwd string, extraEnv []string) (io.ReadWriteCloser, error) {
	m.mu.Lock()
	lvm, ok := m.vms[id]
	m.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("vm not running: %s", id)
	}

	conn, err := lvm.openControlConn()
	if err != nil {
		return nil, fmt.Errorf("vsock connect: %w", err)
	}
	if cwd == "" {
		cwd = "/"
	}
	if len(argv) == 0 {
		argv = []string{"sh"}
	}
	stream, err := streamGuestHTTP(ctx, conn, argv, cwd, extraEnv)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return stream, nil
}

// OpenTCP opens a proxied raw TCP stream to guest loopback on the requested port.
func (m *Manager) OpenTCP(ctx context.Context, id string, port int) (io.ReadWriteCloser, error) {
	m.mu.Lock()
	lvm, ok := m.vms[id]
	m.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("vm not running: %s", id)
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid guest port %d", port)
	}

	conn, err := lvm.openControlConn()
	if err != nil {
		return nil, fmt.Errorf("vsock connect: %w", err)
	}
	stream, err := streamGuestTCPHTTP(ctx, conn, port)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return stream, nil
}

func (lvm *localVM) openControlConn() (net.Conn, error) {
	if lvm.controlDialer != nil {
		return lvm.controlDialer()
	}
	if lvm.vsock == nil {
		return nil, fmt.Errorf("control socket unavailable")
	}
	return lvm.vsock.Connect(vsockControlPort)
}

// ToPublic converts a localVM to a public VM info struct.
func (lvm *localVM) ToPublic() *VM {
	lvm.mu.Lock()
	status := lvm.status
	lvm.mu.Unlock()

	return &VM{
		ID:        lvm.id,
		Name:      lvm.name,
		HostGroup: lvm.hostGroup,
		Status:    status,
		HostPort:  lvm.hostPort,
		CreatedAt: time.Now(),
	}
}
