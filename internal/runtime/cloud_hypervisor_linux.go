//go:build linux

package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/oci"
)

const (
	cloudHypervisorDefaultBin       = "/usr/local/bin/cloud-hypervisor"
	cloudHypervisorDefaultKernel    = "/data/vmlinux-ch.bin"
	cloudHypervisorDefaultInitramfs = "/data/initramfs.cpio.gz"
	cloudHypervisorVsockPort        = 1024
	// Must match cmd/guest-agent tcpBridgeVsockPort. Host→guest uses the Firecracker/CH
	// UDS protocol on --vsock socket= (CONNECT <port>\\n), not AF_VSOCK.
	cloudHypervisorGuestBridgeVsockPort = 1025
)

// Cloud Hypervisor duration constants.
const chGuestReadyTimeout = 180 * time.Second       // max wait for guest agent ready signal
const chTCPBridgeTimeout = 5 * time.Second           // max wait for host TCP bridge port to open
const chAPIReadyTimeout = 10 * time.Second            // max wait for cloud-hypervisor API socket
const chAPIReadyPollInterval = 100 * time.Millisecond // poll interval when waiting for API/TCP
const chAPIClientTimeout = 30 * time.Second           // HTTP client timeout for API socket calls
const chTCPDialTimeout = 200 * time.Millisecond       // per-attempt TCP dial timeout when waiting for port

type CloudHypervisorRuntime struct {
	mu        sync.Mutex
	instances map[uuid.UUID]*cloudHypervisorInstance
	suspended map[uuid.UUID]*cloudHypervisorSuspended
	templates map[string]*cloudHypervisorTemplate
	prepared  map[uuid.UUID]*cloudHypervisorPreparedMigration
	nextSlot  atomic.Uint32

	binaryPath      string
	kernelPath      string
	initramfsPath   string
	sharedRootfsDir string
	advertiseHost   string
	pullAuth        *oci.Auth
}

type cloudHypervisorInstance struct {
	cmd             *exec.Cmd
	bridgeCmd       *exec.Cmd
	tapName         string
	ip              string
	vmIP            string
	workDir         string
	socketBase      string
	apiSocket       string
	logs            []string
	logMu           sync.Mutex
	cancel          context.CancelFunc
	ready           chan struct{}
	once            sync.Once
	inst            Instance
	workDisk        string
	sharedRootfsRef string
	hostPort        int
	retain          bool
	stopped         chan struct{}
}

type cloudHypervisorSuspended struct {
	inst     Instance
	workDir  string
	workDisk string
	hostPort int
}

type cloudHypervisorTemplate struct {
	workDisk string
}

type cloudHypervisorPreparedMigration struct {
	id              uuid.UUID
	cmd             *exec.Cmd
	workDir         string
	apiSocket       string
	socketBase      string
	sharedRootfsRef string
	hostPort        int
	ip              string
	stopped         chan struct{}
}

// NewCloudHypervisorRuntime builds paths from cfg and defaults (no environment variables).
func NewCloudHypervisorRuntime(cfg CloudHypervisorHostConfig, advertiseHost string, pullAuth *oci.Auth) *CloudHypervisorRuntime {
	home, _ := os.UserHomeDir()
	homeKernel := filepath.Join(home, ".kindling", "vmlinuz.bin")
	homeInitramfs := filepath.Join(home, ".kindling", "initramfs.cpio.gz")
	sharedRootfsDir := strings.TrimSpace(cfg.SharedRootfsDir)
	if sharedRootfsDir == "" {
		sharedRootfsDir = strings.TrimSpace(os.Getenv("KINDLING_CH_SHARED_ROOTFS_DIR"))
	}
	if sharedRootfsDir == "" {
		defaultShared := filepath.Join("/data", "kindling-shared-rootfs")
		if _, err := os.Stat(defaultShared); err == nil {
			sharedRootfsDir = defaultShared
		}
	}
	return &CloudHypervisorRuntime{
		instances:  make(map[uuid.UUID]*cloudHypervisorInstance),
		suspended:  make(map[uuid.UUID]*cloudHypervisorSuspended),
		templates:  make(map[string]*cloudHypervisorTemplate),
		prepared:   make(map[uuid.UUID]*cloudHypervisorPreparedMigration),
		binaryPath: firstExistingPath(cfg.BinaryPath, cloudHypervisorDefaultBin),
		// Note: do not fall back to /data/vmlinux.bin — provision.sh stores rust hypervisor firmware there, not a Linux bzImage/vmlinux.
		kernelPath:      firstExistingPath(cfg.KernelPath, cloudHypervisorDefaultKernel, homeKernel),
		initramfsPath:   firstExistingPath(cfg.InitramfsPath, cloudHypervisorDefaultInitramfs, homeInitramfs),
		sharedRootfsDir: sharedRootfsDir,
		advertiseHost:   strings.TrimSpace(advertiseHost),
		pullAuth:        pullAuth,
	}
}

func (r *CloudHypervisorRuntime) Name() string { return "cloud-hypervisor" }
func (r *CloudHypervisorRuntime) Supports(cap Capability) bool {
	return cap == CapabilitySuspendResume || cap == CapabilityWarmClone || cap == CapabilityLiveMigration
}

func (r *CloudHypervisorRuntime) Start(ctx context.Context, inst Instance) (string, error) {
	if _, err := os.Stat(r.binaryPath); err != nil {
		return "", fmt.Errorf("cloud-hypervisor not found at %s: %w", r.binaryPath, err)
	}
	if _, err := os.Stat(r.kernelPath); err != nil {
		return "", fmt.Errorf("kernel not found at %s: %w", r.kernelPath, err)
	}
	if _, err := os.Stat(r.initramfsPath); err != nil {
		return "", fmt.Errorf("initramfs not found at %s: %w", r.initramfsPath, err)
	}
	return r.startVM(ctx, inst)
}

func (r *CloudHypervisorRuntime) startVM(ctx context.Context, inst Instance) (string, error) {
	workDir := filepath.Join(os.TempDir(), "kindling-ch-"+inst.ID.String())
	_ = os.RemoveAll(workDir)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", fmt.Errorf("create work dir: %w", err)
	}

	rootfsDir, err := exportImageToDir(ctx, inst.ImageRef, filepath.Join(workDir, "rootfs"), r.pullAuth)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return "", err
	}
	workDisk := filepath.Join(workDir, "rootfs.qcow2")
	if sharedDisk, ok := r.sharedRootfsPath(inst.ID); ok {
		if err := ensureDir(filepath.Dir(sharedDisk)); err != nil {
			_ = os.RemoveAll(workDir)
			return "", err
		}
		workDisk = sharedDisk
	}
	_ = os.Remove(workDisk)
	if out, err := exec.CommandContext(ctx, "virt-make-fs", "--format=qcow2", "--type=ext4", "--size=+2G", rootfsDir, workDisk).CombinedOutput(); err != nil {
		_ = os.RemoveAll(workDir)
		return "", fmt.Errorf("virt-make-fs: %s: %w", string(out), err)
	}
	if inst.PersistentVolume != nil {
		if err := r.ensurePersistentVolumeDisk(ctx, inst.PersistentVolume); err != nil {
			_ = os.RemoveAll(workDir)
			return "", err
		}
	}
	return r.startPreparedVM(ctx, inst, workDir, workDisk, 0)
}

// buildCHInstance sets up the TAP interface and instance state for a prepared VM launch.
func (r *CloudHypervisorRuntime) buildCHInstance(inst Instance, workDir, workDisk string) (*cloudHypervisorInstance, string, string, uint32, context.CancelFunc, error) {
	slot := r.nextSlot.Add(1) - 1
	hostIP, guestCIDR, err := cloudHypervisorIPs(slot)
	if err != nil {
		return nil, "", "", 0, nil, err
	}
	guestPrefix, err := netip.ParsePrefix(guestCIDR)
	if err != nil {
		return nil, "", "", 0, nil, fmt.Errorf("parse guest cidr: %w", err)
	}
	guestIP := guestPrefix.Addr().String()

	tapName := cloudHypervisorTapName(inst.ID, slot)
	removeCHTap(tapName)
	hostAddr, err := netip.ParseAddr(hostIP)
	if err != nil {
		return nil, "", "", 0, nil, fmt.Errorf("parse host ip: %w", err)
	}
	if err := createCHTap(tapName, hostAddr); err != nil {
		return nil, "", "", 0, nil, fmt.Errorf("tap setup: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	ai := &cloudHypervisorInstance{
		tapName:         tapName,
		vmIP:            guestIP,
		workDir:         workDir,
		workDisk:        workDisk,
		sharedRootfsRef: sharedRootfsRefFromWorkDisk(r.sharedRootfsDir, workDisk),
		socketBase:      filepath.Join(os.TempDir(), "kindling-vsock-"+inst.ID.String()+".sock"),
		apiSocket:       filepath.Join(workDir, "api.sock"),
		cancel:          cancel,
		ready:           make(chan struct{}),
		inst:            inst,
		stopped:         make(chan struct{}),
	}
	return ai, guestCIDR, hostIP, slot, func() { cancel(); removeCHTap(tapName) }, runCtx.Err()
}

// launchCHProcess starts the cloud-hypervisor process and captures its output.
func (r *CloudHypervisorRuntime) launchCHProcess(runCtx context.Context, ai *cloudHypervisorInstance, inst Instance) error {
	args := []string{
		"--kernel", r.kernelPath,
		"--initramfs", r.initramfsPath,
		"--cmdline", "console=hvc0",
		"--cpus", fmt.Sprintf("boot=%d", inst.VCPUs),
		"--memory", fmt.Sprintf("size=%dM", inst.MemoryMB),
	}
	args = append(args, cloudHypervisorDiskArgs(ai.workDisk, inst.PersistentVolume)...)
	args = append(args,
		"--net", fmt.Sprintf("tap=%s,ip=%s,mask=255.255.255.254", ai.tapName, ai.vmIP),
		"--vsock", fmt.Sprintf("cid=3,socket=%s", ai.socketBase),
		"--api-socket", ai.apiSocket,
	)
	cmd := exec.CommandContext(runCtx, r.binaryPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start cloud-hypervisor: %w", err)
	}
	ai.cmd = cmd
	if err := writePIDFile(cloudHypervisorVMPIDPath(ai.workDir), cmd.Process.Pid); err != nil {
		return fmt.Errorf("write vm pid: %w", err)
	}
	go r.captureOutputCH(ai, stdout)
	go r.captureOutputCH(ai, stderr)
	return nil
}

// setupHostBridge allocates a host port and starts the TCP bridge helper.
func (r *CloudHypervisorRuntime) setupHostBridge(ctx context.Context, ai *cloudHypervisorInstance, requestedHostPort int) error {
	var err error
	if requestedHostPort > 0 {
		ai.hostPort = requestedHostPort
	} else {
		ai.hostPort, err = pickFreeTCPPort()
		if err != nil {
			return fmt.Errorf("allocate host tcp forward port: %w", err)
		}
	}
	ai.ip, err = applyAdvertisedHost(net.JoinHostPort("0.0.0.0", strconv.Itoa(ai.hostPort)), r.advertiseHost)
	if err != nil {
		return err
	}
	bridgeCmd, err := startCloudHypervisorBridgeHelper(ai.hostPort, ai.socketBase)
	if err != nil {
		return fmt.Errorf("start host tcp bridge: %w", err)
	}
	ai.bridgeCmd = bridgeCmd
	if err := writePIDFile(cloudHypervisorBridgePIDPath(ai.workDir), bridgeCmd.Process.Pid); err != nil {
		_ = terminatePID(bridgeCmd.Process.Pid)
		return fmt.Errorf("write bridge pid: %w", err)
	}
	if err := waitForTCPPort(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(ai.hostPort)), chTCPBridgeTimeout); err != nil {
		_ = terminatePID(bridgeCmd.Process.Pid)
		return fmt.Errorf("wait for host tcp bridge: %w", err)
	}
	return nil
}

func (r *CloudHypervisorRuntime) startPreparedVM(ctx context.Context, inst Instance, workDir, workDisk string, requestedHostPort int) (string, error) {
	port := inst.Port
	if port == 0 {
		port = 3000
	}

	ai, guestCIDR, hostIP, _, cleanup, err := r.buildCHInstance(inst, workDir, workDisk)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return "", err
	}

	if err := r.startGuestVsockServer(ctx, inst, ai, guestCIDR, hostIP, port); err != nil {
		cleanup()
		_ = os.RemoveAll(workDir)
		return "", err
	}

	runCtx, cancel := context.WithCancel(ctx)
	ai.cancel = cancel

	if err := r.launchCHProcess(runCtx, ai, inst); err != nil {
		cancel()
		cleanup()
		_ = os.RemoveAll(workDir)
		r.cleanupGuestVsock(ai.socketBase)
		return "", err
	}

	r.mu.Lock()
	r.instances[inst.ID] = ai
	r.mu.Unlock()

	if err := waitForGuestReady(runCtx, ai.ready, chGuestReadyTimeout); err != nil {
		slog.Error("cloud-hypervisor guest never became ready",
			"id", inst.ID,
			"guest_ip", ai.vmIP,
			"log_tail", tailCloudHypervisorLogs(ai, 40),
		)
		cancel()
		return "", fmt.Errorf("wait for guest ready: %w", err)
	}

	if err := r.setupHostBridge(runCtx, ai, requestedHostPort); err != nil {
		cancel()
		_ = os.RemoveAll(workDir)
		return "", err
	}

	go r.waitCH(inst.ID, ai)

	slog.Info("cloud hypervisor VM started",
		"id", inst.ID,
		"image", inst.ImageRef,
		"tap", ai.tapName,
		"guest_ip", ai.vmIP,
		"runtime_url", ai.ip,
	)
	return ai.ip, nil
}
