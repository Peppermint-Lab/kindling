//go:build darwin

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/oci"
)

const (
	// appleNATGuestHostIP is eth0's IPv4 inside the VM; must match the host part of appleGuestConfig.IPAddr.
	appleNATGuestHostIP = "10.0.0.1"
	appleNATGuestCIDR   = appleNATGuestHostIP + "/31"
	// tcpBridgeVsockPort is the vsock port where the guest accepts host-initiated connections for a
	// 127.0.0.1 TCP relay (see cmd/guest-agent); must match guest init.
	tcpBridgeVsockPort uint32 = 1025
)

const appleGuestReadyTimeout = 30 * time.Second // max wait for Apple VZ guest agent ready

// AppleRuntime runs Linux microVMs via Apple Virtualization Framework.
// macOS only (Apple Silicon or Intel with Hypervisor.framework).
type AppleRuntime struct {
	mu        sync.Mutex
	instances map[uuid.UUID]*appleInstance
	suspended map[uuid.UUID]*appleSuspended
	templates map[string]*appleTemplate

	kernelPath    string
	initramfsPath string
	pullAuth      *oci.Auth
}

type appleInstance struct {
	vm       *vz.VirtualMachine
	vsock    *vz.VirtioSocketDevice
	ip       string
	hostFwd  net.Listener
	logs     []string
	logMu    sync.Mutex
	cancel   context.CancelFunc
	ready    chan struct{}
	once     sync.Once
	inst     Instance
	appDir   string
	hostPort int
	retain   bool
	stopped  chan struct{}
}

type appleSuspended struct {
	inst     Instance
	appDir   string
	hostPort int
}

type appleTemplate struct {
	appDir string
}

type guestConfig struct {
	Env        []string `json:"env"`
	IPAddr     string   `json:"ip_addr"`
	IPGW       string   `json:"ip_gw"`
	DNSServers []string `json:"dns_servers"`
	Hostname   string   `json:"hostname"`
	Port       int      `json:"port"`
}

// AppleRuntimeConfig holds configuration for the Apple runtime.
type AppleRuntimeConfig struct {
	// KernelPath is the path to a Linux kernel (vmlinuz).
	KernelPath string

	// InitramfsPath is the path to the initramfs with the guest agent.
	InitramfsPath string

	// PullAuth is optional registry credentials for image export.
	PullAuth *oci.Auth
}

// NewAppleRuntime creates a new Apple Virtualization Framework runtime.
func NewAppleRuntime(cfg AppleRuntimeConfig) *AppleRuntime {
	if cfg.KernelPath == "" {
		home, _ := os.UserHomeDir()
		cfg.KernelPath = home + "/.kindling/vmlinuz.bin"
	}
	if cfg.InitramfsPath == "" {
		home, _ := os.UserHomeDir()
		cfg.InitramfsPath = home + "/.kindling/initramfs.cpio.gz"
	}
	return &AppleRuntime{
		instances:     make(map[uuid.UUID]*appleInstance),
		suspended:     make(map[uuid.UUID]*appleSuspended),
		templates:     make(map[string]*appleTemplate),
		kernelPath:    cfg.KernelPath,
		initramfsPath: cfg.InitramfsPath,
		pullAuth:      cfg.PullAuth,
	}
}

func (r *AppleRuntime) Name() string { return "apple-vz" }
func (r *AppleRuntime) Supports(cap Capability) bool {
	return cap == CapabilitySuspendResume || cap == CapabilityWarmClone
}

func (r *AppleRuntime) Start(ctx context.Context, inst Instance) (string, error) {
	if _, err := os.Stat(r.kernelPath); err != nil {
		return "", fmt.Errorf("kernel not found at %s: %w", r.kernelPath, err)
	}
	if inst.PersistentVolume != nil {
		return "", ErrPersistentVolumesUnsupported
	}
	return r.startVM(ctx, inst)
}

func (r *AppleRuntime) startVM(ctx context.Context, inst Instance) (string, error) {
	port := inst.Port
	if port == 0 {
		port = 3000
	}

	// Export Docker image to a directory for sharing into the VM.
	appDir, err := r.exportImage(ctx, inst.ImageRef, inst.ID)
	if err != nil {
		return "", fmt.Errorf("export image: %w", err)
	}
	return r.startPreparedVM(ctx, inst, appDir, 0)
}

func (r *AppleRuntime) startPreparedVM(ctx context.Context, inst Instance, appDir string, requestedHostPort int) (string, error) {
	// Boot Linux kernel with initramfs.
	bootLoader, err := vz.NewLinuxBootLoader(
		r.kernelPath,
		vz.WithInitrd(r.initramfsPath),
		vz.WithCommandLine("console=hvc0"),
	)
	if err != nil {
		return "", fmt.Errorf("create boot loader: %w", err)
	}

	// VM configuration.
	vmCfg, err := vz.NewVirtualMachineConfiguration(
		bootLoader,
		uint(inst.VCPUs),
		uint64(inst.MemoryMB)*1024*1024,
	)
	if err != nil {
		return "", fmt.Errorf("create VM config: %w", err)
	}

	// Network: NAT (built-in, no TAP needed).
	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return "", fmt.Errorf("create NAT attachment: %w", err)
	}
	netCfg, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		return "", fmt.Errorf("create network config: %w", err)
	}
	vmCfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netCfg})

	// Console: capture stdout.
	serialAttachment, err := vz.NewFileHandleSerialPortAttachment(os.Stdin, os.Stdout)
	if err != nil {
		return "", fmt.Errorf("create serial attachment: %w", err)
	}
	consoleCfg, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
	if err != nil {
		return "", fmt.Errorf("create console config: %w", err)
	}
	vmCfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{consoleCfg})

	// Vsock device (for guest agent ↔ host communication).
	vsockCfg, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return "", fmt.Errorf("create vsock config: %w", err)
	}
	vmCfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsockCfg})

	// Shared directory: mount the app's rootfs into the VM at /app.
	sharedDir, err := vz.NewSharedDirectory(appDir, false)
	if err != nil {
		return "", fmt.Errorf("create shared dir: %w", err)
	}
	singleDirShare, err := vz.NewSingleDirectoryShare(sharedDir)
	if err != nil {
		return "", fmt.Errorf("create single dir share: %w", err)
	}
	fsCfg, err := vz.NewVirtioFileSystemDeviceConfiguration("app")
	if err != nil {
		return "", fmt.Errorf("create fs config: %w", err)
	}
	fsCfg.SetDirectoryShare(singleDirShare)
	vmCfg.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{fsCfg})

	// Entropy device (for /dev/random in guest).
	entropyCfg, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return "", fmt.Errorf("create entropy config: %w", err)
	}
	vmCfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyCfg})

	// Validate.
	if _, err := vmCfg.Validate(); err != nil {
		return "", fmt.Errorf("validate VM config: %w", err)
	}

	// Create and start VM.
	vm, err := vz.NewVirtualMachine(vmCfg)
	if err != nil {
		return "", fmt.Errorf("create VM: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	ai := &appleInstance{
		vm:      vm,
		cancel:  cancel,
		ready:   make(chan struct{}),
		inst:    inst,
		appDir:  appDir,
		stopped: make(chan struct{}),
	}

	// Set up vsock listener BEFORE starting the VM.
	// The guest agent connects immediately on boot.
	var vsockDev *vz.VirtioSocketDevice
	socketDevices := vm.SocketDevices()
	if len(socketDevices) > 0 {
		vsockDev = socketDevices[0]
		ai.vsock = vsockDev
		listener, err := vsockDev.Listen(1024)
		if err != nil {
			cancel()
			return "", fmt.Errorf("vsock listen: %w", err)
		}

		// Serve config and log endpoints over vsock.
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go r.handleVsockConn(conn, inst, ai)
			}
		}()
	}

	if err := vm.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("start VM: %w", err)
	}

	r.mu.Lock()
	r.instances[inst.ID] = ai
	r.mu.Unlock()

	if err := waitForGuestReady(runCtx, ai.ready, appleGuestReadyTimeout); err != nil {
		cancel()
		return "", fmt.Errorf("wait for guest ready: %w", err)
	}

	if vsockDev == nil {
		cancel()
		return "", fmt.Errorf("vsock device required for apple runtime")
	}

	hostListenAddr := "127.0.0.1:0"
	if requestedHostPort > 0 {
		hostListenAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(requestedHostPort))
	}
	hostTCP, err := net.Listen("tcp", hostListenAddr)
	if err != nil {
		cancel()
		return "", fmt.Errorf("host tcp forward listen: %w", err)
	}
	hostPort := hostTCP.Addr().(*net.TCPAddr).Port
	ai.ip = net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort))
	ai.hostFwd = hostTCP
	ai.hostPort = hostPort

	go r.forwardHostTCP(runCtx, hostTCP, vsockDev, tcpBridgeVsockPort)

	// Monitor VM state.
	go func() {
		<-runCtx.Done()
		if vm.CanStop() {
			vm.Stop()
		}
		r.mu.Lock()
		delete(r.instances, inst.ID)
		r.mu.Unlock()
		slog.Info("VM stopped", "id", inst.ID, "runtime", "apple-vz")
		close(ai.stopped)
	}()

	slog.Info("VM started",
		"id", inst.ID,
		"image", inst.ImageRef,
		"runtime", "apple-vz",
		"vcpus", inst.VCPUs,
		"memory_mb", inst.MemoryMB,
		"localhost_forward_port", hostPort,
	)

	return ai.ip, nil
}

func (r *AppleRuntime) Suspend(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	ai.retain = true
	r.suspended[id] = &appleSuspended{
		inst:     ai.inst,
		appDir:   ai.appDir,
		hostPort: ai.hostPort,
	}
	r.mu.Unlock()
	if err := r.Stop(ctx, id); err != nil {
		return fmt.Errorf("stop instance for suspend: %w", err)
	}
	select {
	case <-ai.stopped:
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.suspended, id)
		ai.retain = false
		r.mu.Unlock()
		return ctx.Err()
	}
}

func (r *AppleRuntime) Resume(ctx context.Context, id uuid.UUID) (string, error) {
	r.mu.Lock()
	s, ok := r.suspended[id]
	if ok {
		delete(r.suspended, id)
	}
	r.mu.Unlock()
	if !ok {
		return "", ErrInstanceNotRunning
	}
	return r.startPreparedVM(ctx, s.inst, s.appDir, s.hostPort)
}

func (r *AppleRuntime) CreateTemplate(ctx context.Context, id uuid.UUID) (string, error) {
	_ = ctx
	r.mu.Lock()
	var appDir string
	if s, ok := r.suspended[id]; ok {
		appDir = s.appDir
	} else if ai, ok := r.instances[id]; ok {
		appDir = ai.appDir
	}
	r.mu.Unlock()
	if strings.TrimSpace(appDir) == "" {
		return "", ErrInstanceNotRunning
	}
	templateRef := lifecyclePath("apple-template", id.String())
	_ = os.RemoveAll(templateRef)
	if err := copyDir(appDir, templateRef); err != nil {
		return "", err
	}
	r.mu.Lock()
	r.templates[templateRef] = &appleTemplate{appDir: templateRef}
	r.mu.Unlock()
	return templateRef, nil
}

func (r *AppleRuntime) StartClone(ctx context.Context, inst Instance, snapshotRef string, cloneSourceVMID uuid.UUID) (string, StartMetadata, error) {
	if inst.PersistentVolume != nil {
		return "", StartMetadata{}, ErrPersistentVolumesUnsupported
	}
	r.mu.Lock()
	tmpl, ok := r.templates[snapshotRef]
	r.mu.Unlock()
	if !ok {
		return "", StartMetadata{}, ErrInstanceNotRunning
	}
	cloneDir := lifecyclePath("apple-clone", inst.ID.String())
	_ = os.RemoveAll(cloneDir)
	if err := copyDir(tmpl.appDir, cloneDir); err != nil {
		return "", StartMetadata{}, err
	}
	ip, err := r.startPreparedVM(ctx, inst, cloneDir, 0)
	if err != nil {
		return "", StartMetadata{}, err
	}
	return ip, StartMetadata{SnapshotRef: snapshotRef, CloneSourceVMID: cloneSourceVMID}, nil
}

func (r *AppleRuntime) MigrationMetadata(ctx context.Context, id uuid.UUID) (MigrationMetadata, error) {
	return MigrationMetadata{}, ErrLiveMigrationUnsupported
}

func (r *AppleRuntime) PrepareMigrationTarget(ctx context.Context, id uuid.UUID) (PreparedMigrationTarget, error) {
	return PreparedMigrationTarget{}, ErrLiveMigrationUnsupported
}

func (r *AppleRuntime) SendMigration(ctx context.Context, id uuid.UUID, req SendMigrationRequest) error {
	return ErrLiveMigrationUnsupported
}

func (r *AppleRuntime) FinalizeMigrationTarget(ctx context.Context, id uuid.UUID) (string, StartMetadata, error) {
	return "", StartMetadata{}, ErrLiveMigrationUnsupported
}

func (r *AppleRuntime) AbortMigrationTarget(ctx context.Context, id uuid.UUID) error {
	return ErrLiveMigrationUnsupported
}

// forwardHostTCP accepts connections on a localhost listener and relays each to the guest app over vsock.
// Required because VZNATNetworkDeviceAttachment does not support inbound port forwarding from the host to guest eth0.
func (r *AppleRuntime) forwardHostTCP(ctx context.Context, hostLn net.Listener, dev *vz.VirtioSocketDevice, guestVsockPort uint32) {
	go func() {
		<-ctx.Done()
		_ = hostLn.Close()
	}()

	for {
		tcpConn, err := hostLn.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Debug("apple-vz host forward accept ended", "error", err)
				return
			}
		}
		go r.relayTCPOverVsock(tcpConn, dev, guestVsockPort)
	}
}

func (r *AppleRuntime) relayTCPOverVsock(client net.Conn, dev *vz.VirtioSocketDevice, guestVsockPort uint32) {
	defer client.Close()

	guestConn, err := dev.Connect(guestVsockPort)
	if err != nil {
		slog.Debug("apple-vz vsock connect failed", "error", err)
		return
	}
	defer guestConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(guestConn, client)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, guestConn)
	}()
	wg.Wait()
}

func (r *AppleRuntime) Stop(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	if !ok {
		if s, suspended := r.suspended[id]; suspended {
			delete(r.suspended, id)
			r.mu.Unlock()
			if s.appDir != "" {
				_ = os.RemoveAll(s.appDir)
			}
			return nil
		}
	}
	r.mu.Unlock()

	if !ok {
		return nil
	}

	ai.cancel()
	return nil
}

func (r *AppleRuntime) Healthy(ctx context.Context, id uuid.UUID) bool {
	r.mu.Lock()
	_, ok := r.instances[id]
	r.mu.Unlock()
	return ok
}

func (r *AppleRuntime) Logs(ctx context.Context, id uuid.UUID) ([]string, error) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()

	if !ok {
		return nil, nil
	}

	ai.logMu.Lock()
	defer ai.logMu.Unlock()
	out := make([]string, len(ai.logs))
	copy(out, ai.logs)
	return out, nil
}

// ResourceStats connects to guest-agent GET /stats over vsock.
func (r *AppleRuntime) ResourceStats(ctx context.Context, id uuid.UUID) (ResourceStats, error) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok || ai.vsock == nil {
		return ResourceStats{}, ErrInstanceNotRunning
	}
	conn, err := ai.vsock.Connect(GuestStatsVsockPort)
	if err != nil {
		return ResourceStats{}, err
	}
	defer conn.Close()
	return resourceStatsFromGuestHTTP(ctx, conn)
}

func (r *AppleRuntime) ExecGuest(ctx context.Context, id uuid.UUID, argv []string, cwd string, env []string) (GuestExecResult, error) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok || ai.vsock == nil {
		return GuestExecResult{}, ErrInstanceNotRunning
	}
	conn, err := ai.vsock.Connect(GuestControlVsockPort)
	if err != nil {
		return GuestExecResult{}, err
	}
	defer conn.Close()
	return execGuestHTTP(ctx, conn, argv, cwd, env)
}

func (r *AppleRuntime) ReadGuestFile(ctx context.Context, id uuid.UUID, filePath string) ([]byte, error) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok || ai.vsock == nil {
		return nil, ErrInstanceNotRunning
	}
	conn, err := ai.vsock.Connect(GuestControlVsockPort)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return readGuestFileHTTP(ctx, conn, filePath)
}

func (r *AppleRuntime) WriteGuestFile(ctx context.Context, id uuid.UUID, filePath string, data []byte) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok || ai.vsock == nil {
		return ErrInstanceNotRunning
	}
	conn, err := ai.vsock.Connect(GuestControlVsockPort)
	if err != nil {
		return err
	}
	defer conn.Close()
	return writeGuestFileHTTP(ctx, conn, filePath, data)
}

// exportImage pulls/unpacks an OCI image to a directory so it can be shared into the VM.
func (r *AppleRuntime) exportImage(ctx context.Context, imageRef string, id uuid.UUID) (string, error) {
	home, _ := os.UserHomeDir()
	appDir := fmt.Sprintf("%s/.kindling/apps/%s", home, id)
	if err := oci.ExportImageRootfs(ctx, imageRef, appDir, r.pullAuth); err != nil {
		return "", err
	}
	slog.Info("exported image to directory", "image", imageRef, "dir", appDir)
	return appDir, nil
}

// handleVsockConn serves HTTP over a vsock connection from the guest agent.
func (r *AppleRuntime) handleVsockConn(conn net.Conn, inst Instance, ai *appleInstance) {
	defer conn.Close()

	// Read the HTTP request from the guest.
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		slog.Debug("vsock read error", "error", err)
		return
	}

	request := string(buf[:n])

	if strings.Contains(request, "GET /config") {
		cfgBytes, err := json.Marshal(appleGuestConfig(inst))
		if err != nil {
			response := "HTTP/1.1 500 Internal Server Error\r\nContent-Length: 0\r\n\r\n"
			conn.Write([]byte(response))
			return
		}
		response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
			len(cfgBytes), cfgBytes)
		conn.Write([]byte(response))
		slog.Info("served config via vsock", "id", inst.ID)
	} else if strings.Contains(request, "POST /logs") {
		// Accept log stream.
		response := "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"
		conn.Write([]byte(response))
	} else if strings.Contains(request, "POST /ready") {
		ai.markReady()
		response := "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"
		conn.Write([]byte(response))
		slog.Info("guest reported ready", "id", inst.ID)
	} else {
		response := "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"
		conn.Write([]byte(response))
	}
}

func appleGuestConfig(inst Instance) guestConfig {
	port := inst.Port
	if port == 0 {
		port = 3000
	}

	return guestConfig{
		Env:      envWithPort(inst.Env, port),
		IPAddr:   appleNATGuestCIDR,
		IPGW:     "10.0.0.0",
		Hostname: fmt.Sprintf("kindling-%s", inst.ID.String()[:8]),
		Port:     port,
	}
}

func envWithPort(env []string, port int) []string {
	out := append([]string(nil), env...)
	for _, entry := range out {
		if strings.HasPrefix(entry, "PORT=") {
			return out
		}
	}
	return append(out, fmt.Sprintf("PORT=%d", port))
}

func waitForGuestReady(ctx context.Context, ready <-chan struct{}, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("timed out after %s", timeout)
	}
}

func (ai *appleInstance) markReady() {
	ai.once.Do(func() {
		close(ai.ready)
	})
}

func (r *AppleRuntime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, ai := range r.instances {
		slog.Info("stopping instance on shutdown", "id", id)
		ai.cancel()
	}
}
