// Package vmm manages Cloud Hypervisor microVM lifecycle: boot, stop, destroy.
//
// Each VM gets:
//   - A CoW qcow2 disk backed by a shared base image
//   - A TAP network device with a /31 point-to-point link
//   - A vsock UDS for guest agent communication
//   - A Cloud Hypervisor process managed as a child
//
// The VMM reconciles VM state: pending VMs are started, deleted VMs are cleaned up,
// stopped/failed VMs are restarted.
package vmm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
	"github.com/kindlingvm/kindling/internal/oci"
)

// Config holds VMM configuration.
type Config struct {
	// CloudHypervisorBin is the path to the cloud-hypervisor binary.
	CloudHypervisorBin string

	// KernelPath is the path to the vmlinuz kernel image.
	KernelPath string

	// InitramfsPath is the path to the initramfs (guest agent).
	InitramfsPath string

	// BaseImageDir is where base qcow2 images are stored.
	BaseImageDir string

	// WorkImageDir is where per-VM CoW overlay disks are stored.
	WorkImageDir string
}

// Defaults returns a Config with sensible defaults.
func Defaults() Config {
	return Config{
		CloudHypervisorBin: "/usr/local/bin/cloud-hypervisor",
		KernelPath:         "/data/vmlinuz.bin",
		InitramfsPath:      "/data/initramfs.cpio.gz",
		BaseImageDir:       "/data/base",
		WorkImageDir:       "/data/work",
	}
}

// vmProcess tracks a running VM's resources.
type vmProcess struct {
	cmd     *exec.Cmd
	tapName string
}

// Manager manages Cloud Hypervisor VM processes.
type Manager struct {
	cfg     Config
	serverID uuid.UUID
	q       *queries.Queries

	mu      sync.Mutex
	vms     map[uuid.UUID]*vmProcess
	nextTap atomic.Uint64
}

// NewManager creates a new VM manager.
func NewManager(cfg Config, serverID uuid.UUID, q *queries.Queries) *Manager {
	return &Manager{
		cfg:      cfg,
		serverID: serverID,
		q:        q,
		vms:      make(map[uuid.UUID]*vmProcess),
	}
}

// ReconcileVM is the reconcile function for VMs.
// It handles starting pending VMs and cleaning up deleted ones.
func (m *Manager) ReconcileVM(ctx context.Context, vmID uuid.UUID) error {
	vm, err := m.q.VMFirstByID(ctx, pguuid.ToPgtype(vmID))
	if err != nil {
		return fmt.Errorf("fetch VM: %w", err)
	}

	// Only reconcile VMs assigned to this server.
	if pguuid.FromPgtype(vm.ServerID) != m.serverID {
		return nil
	}

	// Deleted VMs: clean up.
	if vm.DeletedAt.Valid {
		return m.cleanupVM(ctx, vm)
	}

	// Already running: ensure status reflects reality.
	m.mu.Lock()
	_, running := m.vms[vmID]
	m.mu.Unlock()
	if running {
		if vm.Status == "pending" || vm.Status == "starting" {
			m.q.VMUpdateStatus(ctx, queries.VMUpdateStatusParams{
				ID:     vm.ID,
				Status: "running",
			})
		}
		return nil
	}

	// Recover stopped/failed VMs.
	if vm.Status == "stopped" || vm.Status == "failed" {
		slog.Info("recovering VM", "vm_id", vmID, "from", vm.Status)
	}

	// Advance to starting.
	if vm.Status == "pending" || vm.Status == "stopped" || vm.Status == "failed" {
		m.q.VMUpdateStatus(ctx, queries.VMUpdateStatusParams{
			ID:     vm.ID,
			Status: "starting",
		})
	}

	return m.startVM(ctx, vm)
}

func (m *Manager) startVM(ctx context.Context, vm queries.Vm) error {
	vmID := pguuid.FromPgtype(vm.ID)

	// Ensure work disk exists (CoW overlay).
	image, err := m.q.ImageFindByID(ctx, vm.ImageID)
	if err != nil {
		return fmt.Errorf("fetch image: %w", err)
	}

	if err := m.ensureBaseImage(ctx, image); err != nil {
		return fmt.Errorf("base image: %w", err)
	}

	if err := m.createWorkDisk(ctx, vmID, pguuid.FromPgtype(image.ID)); err != nil {
		return fmt.Errorf("work disk: %w", err)
	}

	// Create TAP device.
	tapName := fmt.Sprintf("ktap%d", m.nextTap.Add(1))
	vmIP := vm.IpAddress
	hostIP := vmIP.Prev()

	if err := createTAP(tapName, hostIP); err != nil {
		return fmt.Errorf("TAP setup: %w", err)
	}

	// vsock path.
	vsockPath := VsockPath(vmID)

	// Prepare stdout/stderr log files.
	stdout, err := os.Create(fmt.Sprintf("/tmp/kindling-vm-%s.out", vmID))
	if err != nil {
		return fmt.Errorf("create stdout log: %w", err)
	}
	stderr, err := os.Create(fmt.Sprintf("/tmp/kindling-vm-%s.err", vmID))
	if err != nil {
		stdout.Close()
		return fmt.Errorf("create stderr log: %w", err)
	}

	// Build Cloud Hypervisor command.
	workDisk := fmt.Sprintf("%s/%s.qcow2", m.cfg.WorkImageDir, vmID)
	cmd := exec.Command(m.cfg.CloudHypervisorBin,
		"--kernel", m.cfg.KernelPath,
		"--disk", fmt.Sprintf("path=%s,direct=on,queue_size=256", workDisk),
		"--initramfs", m.cfg.InitramfsPath,
		"--cmdline", "console=hvc0",
		"--cpus", fmt.Sprintf("boot=%d", vm.Vcpus),
		"--memory", fmt.Sprintf("size=%dM", vm.Memory),
		"--net", fmt.Sprintf("tap=%s,ip=%s,mask=255.255.255.254", tapName, hostIP),
		"--vsock", fmt.Sprintf("cid=3,socket=%s", vsockPath),
	)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = sysProcAttr()

	slog.Info("starting VM",
		"vm_id", vmID,
		"vcpus", vm.Vcpus,
		"memory_mb", vm.Memory,
		"vm_ip", vmIP,
		"host_ip", hostIP,
		"tap", tapName,
	)

	// Register before starting.
	m.mu.Lock()
	m.vms[vmID] = &vmProcess{cmd: cmd, tapName: tapName}
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		m.mu.Lock()
		delete(m.vms, vmID)
		m.mu.Unlock()
		stdout.Close()
		stderr.Close()
		return fmt.Errorf("start cloud-hypervisor: %w", err)
	}

	// Mark running.
	m.q.VMUpdateStatus(ctx, queries.VMUpdateStatusParams{
		ID:     vm.ID,
		Status: "running",
	})

	// Monitor process in background.
	go m.waitVM(vmID, vm.ID, cmd, stdout, stderr, tapName)

	return nil
}

// waitVM waits for the Cloud Hypervisor process to exit and updates state.
func (m *Manager) waitVM(vmID uuid.UUID, dbID pgtype.UUID, cmd *exec.Cmd, stdout, stderr *os.File, tapName string) {
	err := cmd.Wait()
	stdout.Close()
	stderr.Close()

	ctx := context.Background()

	if err != nil {
		slog.Error("VM exited with error", "vm_id", vmID, "error", err)
		m.q.VMUpdateStatus(ctx, queries.VMUpdateStatusParams{
			ID:     dbID,
			Status: "failed",
		})
	} else {
		slog.Info("VM exited cleanly", "vm_id", vmID)
		m.q.VMUpdateStatus(ctx, queries.VMUpdateStatusParams{
			ID:     dbID,
			Status: "stopped",
		})
	}

	// Clean up resources.
	m.mu.Lock()
	delete(m.vms, vmID)
	m.mu.Unlock()

	removeTAP(tapName)
}

// cleanupVM kills a running VM and removes its resources.
func (m *Manager) cleanupVM(ctx context.Context, vm queries.Vm) error {
	vmID := pguuid.FromPgtype(vm.ID)
	slog.Info("cleaning up VM", "vm_id", vmID)

	m.mu.Lock()
	proc, exists := m.vms[vmID]
	delete(m.vms, vmID)
	m.mu.Unlock()

	if exists {
		if proc.cmd.Process != nil {
			proc.cmd.Process.Kill()
		}
		removeTAP(proc.tapName)
	}

	// Remove work disk.
	workDisk := fmt.Sprintf("%s/%s.qcow2", m.cfg.WorkImageDir, vmID)
	os.Remove(workDisk)

	// Remove vsock socket.
	os.Remove(VsockPath(vmID))

	return nil
}

// ensureBaseImage checks if the base qcow2 image exists on disk.
// If not, it downloads and converts the OCI image.
func (m *Manager) ensureBaseImage(ctx context.Context, image queries.Image) error {
	imageID := pguuid.FromPgtype(image.ID)
	basePath := fmt.Sprintf("%s/%s.qcow2", m.cfg.BaseImageDir, imageID)

	if _, err := os.Stat(basePath); err == nil {
		return nil // already exists
	}

	os.MkdirAll(m.cfg.BaseImageDir, 0o755)

	// Download OCI image with skopeo.
	tmpDir, err := os.MkdirTemp("", "kindling-image-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	imageRef := fmt.Sprintf("%s/%s:%s", image.Registry, image.Repository, image.Tag)
	ociPath := tmpDir + "/oci"

	slog.Info("pulling OCI image", "ref", imageRef)
	if err := runCmd(ctx, "skopeo", "copy",
		fmt.Sprintf("docker://%s", imageRef),
		fmt.Sprintf("oci:%s:latest", ociPath),
	); err != nil {
		return fmt.Errorf("skopeo copy: %w", err)
	}

	bundlePath := tmpDir + "/bundle"
	if err := oci.UmociUnpack(ctx, ociPath+":latest", bundlePath); err != nil {
		return fmt.Errorf("umoci unpack: %w", err)
	}

	// Convert to qcow2 from unpacked rootfs (not the full OCI bundle with config.json).
	rootfsPath := filepath.Join(bundlePath, "rootfs")
	if err := runCmd(ctx, "virt-make-fs",
		"--format=qcow2", "--type=ext4", "--size=+5G",
		rootfsPath, basePath,
	); err != nil {
		return fmt.Errorf("virt-make-fs: %w", err)
	}

	slog.Info("created base image", "image_id", imageID, "path", basePath)
	return nil
}

// createWorkDisk creates a CoW overlay disk for a VM.
func (m *Manager) createWorkDisk(ctx context.Context, vmID, imageID uuid.UUID) error {
	os.MkdirAll(m.cfg.WorkImageDir, 0o755)

	basePath := fmt.Sprintf("%s/%s.qcow2", m.cfg.BaseImageDir, imageID)
	workPath := fmt.Sprintf("%s/%s.qcow2", m.cfg.WorkImageDir, vmID)

	// Remove stale work disk.
	os.Remove(workPath)

	return runCmd(ctx, "qemu-img", "create",
		"-f", "qcow2",
		"-b", basePath,
		"-F", "qcow2",
		workPath,
	)
}

// VsockPath returns the vsock UDS path for a VM.
func VsockPath(vmID uuid.UUID) string {
	return fmt.Sprintf("/tmp/kindling-vsock-%s.sock", vmID)
}

// VsockGuestPath returns the UDS path for guest-initiated vsock connections.
func VsockGuestPath(vmID uuid.UUID, port int) string {
	return fmt.Sprintf("%s_%d", VsockPath(vmID), port)
}

// Stop kills all running VMs. Called during graceful shutdown.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for vmID, proc := range m.vms {
		slog.Info("killing VM on shutdown", "vm_id", vmID)
		if proc.cmd.Process != nil {
			proc.cmd.Process.Kill()
		}
		removeTAP(proc.tapName)
	}
}

func runCmd(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("command failed", "cmd", name, "args", args, "output", string(out), "error", err)
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
