//go:build darwin

package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/Code-Hex/vz/v3"
	"github.com/google/uuid"
)

// AppleRuntime runs Linux microVMs via Apple Virtualization Framework.
// macOS only (Apple Silicon or Intel with Hypervisor.framework).
type AppleRuntime struct {
	mu        sync.Mutex
	instances map[uuid.UUID]*appleInstance

	kernelPath    string
	initramfsPath string
}

type appleInstance struct {
	vm     *vz.VirtualMachine
	ip     string
	logs   []string
	logMu  sync.Mutex
	cancel context.CancelFunc
}

// AppleRuntimeConfig holds configuration for the Apple runtime.
type AppleRuntimeConfig struct {
	// KernelPath is the path to a Linux kernel (vmlinuz).
	KernelPath string

	// InitramfsPath is the path to the initramfs with the guest agent.
	InitramfsPath string
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
		kernelPath:    cfg.KernelPath,
		initramfsPath: cfg.InitramfsPath,
	}
}

func (r *AppleRuntime) Name() string { return "apple-vz" }

func (r *AppleRuntime) Start(ctx context.Context, inst Instance) (string, error) {
	if _, err := os.Stat(r.kernelPath); err != nil {
		return "", fmt.Errorf("kernel not found at %s: %w", r.kernelPath, err)
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
		vm:     vm,
		ip:     fmt.Sprintf("192.168.64.2:%d", port), // macOS NAT default range
		cancel: cancel,
	}

	// Set up vsock listener BEFORE starting the VM.
	// The guest agent connects immediately on boot.
	socketDevices := vm.SocketDevices()
	if len(socketDevices) > 0 {
		vsockDev := socketDevices[0]
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
				go r.handleVsockConn(conn, inst)
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
	}()

	slog.Info("VM started",
		"id", inst.ID,
		"image", inst.ImageRef,
		"runtime", "apple-vz",
		"vcpus", inst.VCPUs,
		"memory_mb", inst.MemoryMB,
	)

	return ai.ip, nil
}

func (r *AppleRuntime) Stop(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
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

// exportImage exports a Docker image to a directory so it can be shared into the VM.
func (r *AppleRuntime) exportImage(ctx context.Context, imageRef string, id uuid.UUID) (string, error) {
	home, _ := os.UserHomeDir()
	appDir := fmt.Sprintf("%s/.kindling/apps/%s", home, id)
	os.MkdirAll(appDir, 0o755)

	// Create a container from the image and copy its filesystem out.
	containerName := fmt.Sprintf("kindling-export-%s", id.String()[:8])

	// Create container (don't start it).
	if out, err := exec.CommandContext(ctx, "docker", "create", "--name", containerName, imageRef).CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker create: %s: %w", string(out), err)
	}

	// Copy the entire container rootfs — includes the runtime (node, ruby, etc.)
	// plus the app code at /app.
	if out, err := exec.CommandContext(ctx, "docker", "cp", containerName+":/.", appDir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker cp: %s: %w", string(out), err)
	}

	// Remove the temporary container.
	exec.CommandContext(ctx, "docker", "rm", containerName).Run()

	slog.Info("exported image to directory", "image", imageRef, "dir", appDir)
	return appDir, nil
}

// handleVsockConn serves HTTP over a vsock connection from the guest agent.
func (r *AppleRuntime) handleVsockConn(conn net.Conn, inst Instance) {
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
		// Serve config as JSON.
		hostname := fmt.Sprintf("kindling-%s", inst.ID.String()[:8])
		config := fmt.Sprintf(`{"env":[%s],"ip_addr":"10.0.0.1/31","ip_gw":"10.0.0.0","hostname":"%s"}`,
			formatEnvJSON(inst.Env), hostname)

		response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
			len(config), config)
		conn.Write([]byte(response))
		slog.Info("served config via vsock", "id", inst.ID)
	} else if strings.Contains(request, "POST /logs") {
		// Accept log stream.
		response := "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"
		conn.Write([]byte(response))
	} else {
		response := "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"
		conn.Write([]byte(response))
	}
}

func formatEnvJSON(env []string) string {
	if len(env) == 0 {
		return ""
	}
	var parts []string
	for _, e := range env {
		parts = append(parts, fmt.Sprintf(`"%s"`, strings.ReplaceAll(e, `"`, `\"`)))
	}
	return strings.Join(parts, ",")
}

func (r *AppleRuntime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, ai := range r.instances {
		slog.Info("stopping instance on shutdown", "id", id)
		ai.cancel()
	}
}

