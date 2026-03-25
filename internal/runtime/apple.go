//go:build darwin

package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

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
		cfg.KernelPath = "/data/vmlinuz.bin"
	}
	if cfg.InitramfsPath == "" {
		cfg.InitramfsPath = "/data/initramfs.cpio.gz"
	}
	return &AppleRuntime{
		instances:     make(map[uuid.UUID]*appleInstance),
		kernelPath:    cfg.KernelPath,
		initramfsPath: cfg.InitramfsPath,
	}
}

func (r *AppleRuntime) Name() string { return "apple-vz" }

func (r *AppleRuntime) Start(ctx context.Context, inst Instance) (string, error) {
	// Check if we have a kernel — if not, fall back to Docker.
	if _, err := os.Stat(r.kernelPath); err != nil {
		slog.Info("no kernel found for apple-vz, falling back to docker", "kernel", r.kernelPath)
		return r.startDocker(ctx, inst)
	}

	return r.startVM(ctx, inst)
}

func (r *AppleRuntime) startVM(ctx context.Context, inst Instance) (string, error) {
	port := inst.Port
	if port == 0 {
		port = 3000
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

// startDocker is the fallback when kernel/initramfs aren't available.
func (r *AppleRuntime) startDocker(ctx context.Context, inst Instance) (string, error) {
	containerName := fmt.Sprintf("kindling-%s", inst.ID)
	port := inst.Port
	if port == 0 {
		port = 3000
	}

	args := []string{
		"run", "--rm",
		"--name", containerName,
		"--memory", fmt.Sprintf("%dm", inst.MemoryMB),
		"--cpus", fmt.Sprintf("%d", inst.VCPUs),
		"-p", fmt.Sprintf("0:%d", port),
	}
	for _, e := range inst.Env {
		args = append(args, "-e", e)
	}
	args = append(args, "-e", fmt.Sprintf("PORT=%d", port))
	args = append(args, inst.ImageRef)

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, "docker", args...)

	ai := &appleInstance{
		cancel: cancel,
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("docker run: %w", err)
	}

	go r.captureOutput(ai, stdout)
	go r.captureOutput(ai, stderr)

	// Wait for container to start, then get port.
	time.Sleep(2 * time.Second)
	ip, err := dockerPort(containerName, port)
	if err != nil {
		ip = fmt.Sprintf("127.0.0.1:%d", port)
	}
	ai.ip = ip

	r.mu.Lock()
	r.instances[inst.ID] = ai
	r.mu.Unlock()

	go func() {
		cmd.Wait()
		r.mu.Lock()
		delete(r.instances, inst.ID)
		r.mu.Unlock()
		slog.Info("container exited", "id", inst.ID, "runtime", "apple-vz(docker)")
	}()

	slog.Info("container started (docker fallback)",
		"id", inst.ID,
		"image", inst.ImageRef,
		"ip", ip,
	)

	return ip, nil
}

func (r *AppleRuntime) Stop(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()

	if !ok {
		return nil
	}

	// Try docker stop first (for docker fallback instances).
	exec.CommandContext(ctx, "docker", "stop", fmt.Sprintf("kindling-%s", id)).Run()

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

func (r *AppleRuntime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, ai := range r.instances {
		slog.Info("stopping instance on shutdown", "id", id)
		exec.Command("docker", "stop", fmt.Sprintf("kindling-%s", id)).Run()
		ai.cancel()
	}
}

func (r *AppleRuntime) captureOutput(ai *appleInstance, rd io.Reader) {
	scanner := bufio.NewScanner(rd)
	for scanner.Scan() {
		line := scanner.Text()
		ai.logMu.Lock()
		ai.logs = append(ai.logs, line)
		if len(ai.logs) > 1000 {
			ai.logs = ai.logs[len(ai.logs)-1000:]
		}
		ai.logMu.Unlock()
	}
}

func dockerPort(containerName string, containerPort int) (string, error) {
	out, err := exec.Command("docker", "port", containerName, fmt.Sprintf("%d", containerPort)).Output()
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	parts := strings.Split(line, "\n")
	if len(parts) > 0 {
		return strings.TrimSpace(parts[0]), nil
	}
	return "", fmt.Errorf("no port mapping found")
}
