//go:build linux

package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
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
	"github.com/kindlingvm/kindling/internal/chbridge"
	"github.com/kindlingvm/kindling/internal/oci"
	"github.com/vishvananda/netlink"
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

type guestConfig struct {
	Env             []string `json:"env"`
	IPAddr          string   `json:"ip_addr"`
	IPGW            string   `json:"ip_gw"`
	Hostname        string   `json:"hostname"`
	Port            int      `json:"port"`
	VolumeMountPath string   `json:"volume_mount_path,omitempty"`
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

func (r *CloudHypervisorRuntime) startPreparedVM(ctx context.Context, inst Instance, workDir, workDisk string, requestedHostPort int) (string, error) {
	port := inst.Port
	if port == 0 {
		port = 3000
	}

	slot := r.nextSlot.Add(1) - 1
	hostIP, guestCIDR, err := cloudHypervisorIPs(slot)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return "", err
	}
	guestPrefix, err := netip.ParsePrefix(guestCIDR)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return "", fmt.Errorf("parse guest cidr: %w", err)
	}
	guestIP := guestPrefix.Addr().String()

	tapName := cloudHypervisorTapName(inst.ID, slot)
	removeCHTap(tapName)
	hostAddr, err := netip.ParseAddr(hostIP)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return "", fmt.Errorf("parse host ip: %w", err)
	}
	if err := createCHTap(tapName, hostAddr); err != nil {
		_ = os.RemoveAll(workDir)
		return "", fmt.Errorf("tap setup: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
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

	if err := r.startGuestVsockServer(runCtx, inst, ai, guestCIDR, hostIP, port); err != nil {
		cancel()
		removeCHTap(tapName)
		_ = os.RemoveAll(workDir)
		return "", err
	}

	args := []string{
		"--kernel", r.kernelPath,
		"--initramfs", r.initramfsPath,
		"--cmdline", "console=hvc0",
		"--cpus", fmt.Sprintf("boot=%d", inst.VCPUs),
		"--memory", fmt.Sprintf("size=%dM", inst.MemoryMB),
	}
	args = append(args, cloudHypervisorDiskArgs(workDisk, inst.PersistentVolume)...)
	args = append(args,
		"--net", fmt.Sprintf("tap=%s,ip=%s,mask=255.255.255.254", tapName, hostIP),
		"--vsock", fmt.Sprintf("cid=3,socket=%s", ai.socketBase),
		"--api-socket", ai.apiSocket,
	)
	cmd := exec.CommandContext(runCtx, r.binaryPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		cancel()
		removeCHTap(tapName)
		_ = os.RemoveAll(workDir)
		r.cleanupGuestVsock(ai.socketBase)
		return "", fmt.Errorf("start cloud-hypervisor: %w", err)
	}
	ai.cmd = cmd
	if err := writePIDFile(cloudHypervisorVMPIDPath(workDir), cmd.Process.Pid); err != nil {
		cancel()
		_ = os.RemoveAll(workDir)
		return "", fmt.Errorf("write vm pid: %w", err)
	}
	go r.captureOutputCH(ai, stdout)
	go r.captureOutputCH(ai, stderr)

	r.mu.Lock()
	r.instances[inst.ID] = ai
	r.mu.Unlock()

	if err := waitForGuestReady(runCtx, ai.ready, 180*time.Second); err != nil {
		slog.Error("cloud-hypervisor guest never became ready",
			"id", inst.ID,
			"guest_ip", guestIP,
			"log_tail", tailCloudHypervisorLogs(ai, 40),
		)
		cancel()
		return "", fmt.Errorf("wait for guest ready: %w", err)
	}

	if requestedHostPort > 0 {
		ai.hostPort = requestedHostPort
	} else {
		ai.hostPort, err = pickFreeTCPPort()
		if err != nil {
			cancel()
			_ = os.RemoveAll(workDir)
			return "", fmt.Errorf("allocate host tcp forward port: %w", err)
		}
	}
	ai.ip, err = applyAdvertisedHost(net.JoinHostPort("0.0.0.0", strconv.Itoa(ai.hostPort)), r.advertiseHost)
	if err != nil {
		cancel()
		_ = os.RemoveAll(workDir)
		return "", err
	}
	bridgeCmd, err := startCloudHypervisorBridgeHelper(ai.hostPort, ai.socketBase)
	if err != nil {
		cancel()
		_ = os.RemoveAll(workDir)
		return "", fmt.Errorf("start host tcp bridge: %w", err)
	}
	ai.bridgeCmd = bridgeCmd
	if err := writePIDFile(cloudHypervisorBridgePIDPath(workDir), bridgeCmd.Process.Pid); err != nil {
		cancel()
		_ = terminatePID(bridgeCmd.Process.Pid)
		_ = os.RemoveAll(workDir)
		return "", fmt.Errorf("write bridge pid: %w", err)
	}
	if err := waitForTCPPort(runCtx, net.JoinHostPort("127.0.0.1", strconv.Itoa(ai.hostPort)), 5*time.Second); err != nil {
		cancel()
		_ = terminatePID(bridgeCmd.Process.Pid)
		_ = os.RemoveAll(workDir)
		return "", fmt.Errorf("wait for host tcp bridge: %w", err)
	}
	go r.waitCH(inst.ID, ai)

	slog.Info("cloud hypervisor VM started",
		"id", inst.ID,
		"image", inst.ImageRef,
		"tap", tapName,
		"guest_ip", guestIP,
		"runtime_url", ai.ip,
	)
	return ai.ip, nil
}

func (r *CloudHypervisorRuntime) Suspend(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	ai.retain = true
	r.suspended[id] = &cloudHypervisorSuspended{
		inst:     ai.inst,
		workDir:  ai.workDir,
		workDisk: ai.workDisk,
		hostPort: ai.hostPort,
	}
	r.mu.Unlock()
	if err := r.Stop(ctx, id); err != nil {
		return err
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

func (r *CloudHypervisorRuntime) Resume(ctx context.Context, id uuid.UUID) (string, error) {
	r.mu.Lock()
	s, ok := r.suspended[id]
	if ok {
		delete(r.suspended, id)
	}
	r.mu.Unlock()
	if !ok {
		return "", ErrInstanceNotRunning
	}
	return r.startPreparedVM(ctx, s.inst, s.workDir, s.workDisk, s.hostPort)
}

func (r *CloudHypervisorRuntime) CreateTemplate(ctx context.Context, id uuid.UUID) (string, error) {
	_ = ctx
	r.mu.Lock()
	s, ok := r.suspended[id]
	if !ok {
		r.mu.Unlock()
		return "", fmt.Errorf("cloud-hypervisor template requires suspended source")
	}
	templateDir := lifecyclePath("ch-template", id.String())
	templateDisk := filepath.Join(templateDir, "rootfs.qcow2")
	r.mu.Unlock()
	_ = os.RemoveAll(templateDir)
	if err := ensureDir(templateDir); err != nil {
		return "", err
	}
	if err := copyFile(s.workDisk, templateDisk); err != nil {
		return "", err
	}
	r.mu.Lock()
	r.templates[templateDir] = &cloudHypervisorTemplate{workDisk: templateDisk}
	r.mu.Unlock()
	return templateDir, nil
}

func (r *CloudHypervisorRuntime) StartClone(ctx context.Context, inst Instance, snapshotRef string, cloneSourceVMID uuid.UUID) (string, StartMetadata, error) {
	r.mu.Lock()
	tmpl, ok := r.templates[snapshotRef]
	r.mu.Unlock()
	if !ok {
		return "", StartMetadata{}, ErrInstanceNotRunning
	}
	workDir := filepath.Join(os.TempDir(), "kindling-ch-"+inst.ID.String())
	_ = os.RemoveAll(workDir)
	if err := ensureDir(workDir); err != nil {
		return "", StartMetadata{}, err
	}
	workDisk := filepath.Join(workDir, "rootfs.qcow2")
	if sharedDisk, ok := r.sharedRootfsPath(inst.ID); ok {
		if err := ensureDir(filepath.Dir(sharedDisk)); err != nil {
			return "", StartMetadata{}, err
		}
		workDisk = sharedDisk
	}
	_ = os.Remove(workDisk)
	if err := copyFile(tmpl.workDisk, workDisk); err != nil {
		return "", StartMetadata{}, err
	}
	ip, err := r.startPreparedVM(ctx, inst, workDir, workDisk, 0)
	if err != nil {
		return "", StartMetadata{}, err
	}
	return ip, StartMetadata{
		SnapshotRef:     snapshotRef,
		SharedRootfsRef: sharedRootfsRefFromWorkDisk(r.sharedRootfsDir, workDisk),
		CloneSourceVMID: cloneSourceVMID,
	}, nil
}

func (r *CloudHypervisorRuntime) MigrationMetadata(ctx context.Context, id uuid.UUID) (MigrationMetadata, error) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok {
		return MigrationMetadata{}, ErrInstanceNotRunning
	}
	info, err := r.pingVMM(ctx, ai.apiSocket)
	if err != nil {
		return MigrationMetadata{}, err
	}
	return MigrationMetadata{
		SharedRootfsRef: ai.sharedRootfsRef,
		Version:         strings.TrimSpace(info.Version),
	}, nil
}

func (r *CloudHypervisorRuntime) PrepareMigrationTarget(ctx context.Context, id uuid.UUID) (PreparedMigrationTarget, error) {
	if strings.TrimSpace(r.sharedRootfsDir) == "" {
		return PreparedMigrationTarget{}, ErrLiveMigrationUnsupported
	}
	workDir := cloudHypervisorWorkDir(id)
	_ = os.RemoveAll(workDir)
	if err := ensureDir(workDir); err != nil {
		return PreparedMigrationTarget{}, err
	}
	apiSocket := filepath.Join(workDir, "api.sock")
	socketBase := filepath.Join(os.TempDir(), "kindling-vsock-"+id.String()+".sock")
	cmd := exec.CommandContext(ctx, r.binaryPath, "--api-socket", apiSocket)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(workDir)
		return PreparedMigrationTarget{}, fmt.Errorf("start destination cloud-hypervisor: %w", err)
	}
	prepared := &cloudHypervisorPreparedMigration{
		id:              id,
		cmd:             cmd,
		workDir:         workDir,
		apiSocket:       apiSocket,
		socketBase:      socketBase,
		sharedRootfsRef: sharedRootfsRefFromWorkDisk(r.sharedRootfsDir, sharedRootfsPathForID(r.sharedRootfsDir, id)),
		stopped:         make(chan struct{}),
	}
	ai := &cloudHypervisorInstance{logMu: sync.Mutex{}}
	go func() {
		r.captureOutputCH(ai, stdout)
	}()
	go func() {
		r.captureOutputCH(ai, stderr)
	}()
	if err := waitForCloudHypervisorAPI(ctx, apiSocket); err != nil {
		_ = terminatePID(cmd.Process.Pid)
		_ = os.RemoveAll(workDir)
		return PreparedMigrationTarget{}, err
	}
	port, err := pickFreeTCPPort()
	if err != nil {
		_ = terminatePID(cmd.Process.Pid)
		_ = os.RemoveAll(workDir)
		return PreparedMigrationTarget{}, err
	}
	receiveAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	if err := r.putVMM(ctx, apiSocket, "/vm.receive-migration", map[string]string{"receiver_url": "tcp:" + receiveAddr}); err != nil {
		_ = terminatePID(cmd.Process.Pid)
		_ = os.RemoveAll(workDir)
		return PreparedMigrationTarget{}, err
	}
	prepared.hostPort = 0
	r.mu.Lock()
	r.prepared[id] = prepared
	r.mu.Unlock()
	go r.waitPreparedMigration(prepared)
	return PreparedMigrationTarget{ReceiveAddr: receiveAddr}, nil
}

func (r *CloudHypervisorRuntime) SendMigration(ctx context.Context, id uuid.UUID, req SendMigrationRequest) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok {
		return ErrInstanceNotRunning
	}
	payload := map[string]any{
		"destination_url":  req.DestinationURL,
		"downtime_ms":      req.DowntimeMS,
		"timeout_s":        req.TimeoutSeconds,
		"timeout_strategy": "Cancel",
	}
	return r.putVMM(ctx, ai.apiSocket, "/vm.send-migration", payload)
}

func (r *CloudHypervisorRuntime) FinalizeMigrationTarget(ctx context.Context, id uuid.UUID) (string, StartMetadata, error) {
	r.mu.Lock()
	prepared, ok := r.prepared[id]
	if ok {
		delete(r.prepared, id)
	}
	r.mu.Unlock()
	if !ok {
		return "", StartMetadata{}, ErrInstanceNotRunning
	}
	prepared.hostPort, _ = pickFreeTCPPort()
	prepared.ip, _ = applyAdvertisedHost(net.JoinHostPort("0.0.0.0", strconv.Itoa(prepared.hostPort)), r.advertiseHost)
	bridgeCmd, err := startCloudHypervisorBridgeHelper(prepared.hostPort, prepared.socketBase)
	if err != nil {
		_ = terminatePID(prepared.cmd.Process.Pid)
		_ = os.RemoveAll(prepared.workDir)
		return "", StartMetadata{}, fmt.Errorf("start migration bridge: %w", err)
	}
	if err := waitForTCPPort(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(prepared.hostPort)), 5*time.Second); err != nil {
		_ = terminatePID(bridgeCmd.Process.Pid)
		_ = terminatePID(prepared.cmd.Process.Pid)
		_ = os.RemoveAll(prepared.workDir)
		return "", StartMetadata{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	ai := &cloudHypervisorInstance{
		cmd:             prepared.cmd,
		bridgeCmd:       bridgeCmd,
		ip:              prepared.ip,
		workDir:         prepared.workDir,
		socketBase:      prepared.socketBase,
		apiSocket:       prepared.apiSocket,
		workDisk:        sharedRootfsPathForID(r.sharedRootfsDir, id),
		sharedRootfsRef: prepared.sharedRootfsRef,
		hostPort:        prepared.hostPort,
		cancel:          cancel,
		ready:           make(chan struct{}),
		inst:            Instance{ID: id},
		stopped:         make(chan struct{}),
	}
	r.mu.Lock()
	r.instances[id] = ai
	r.mu.Unlock()
	go r.waitCH(id, ai)
	_ = runCtx
	return prepared.ip, StartMetadata{SharedRootfsRef: prepared.sharedRootfsRef}, nil
}

func (r *CloudHypervisorRuntime) AbortMigrationTarget(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	prepared, ok := r.prepared[id]
	if ok {
		delete(r.prepared, id)
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	if prepared.cmd != nil && prepared.cmd.Process != nil {
		_ = terminatePID(prepared.cmd.Process.Pid)
	}
	_ = os.RemoveAll(prepared.workDir)
	return nil
}

func (r *CloudHypervisorRuntime) startGuestVsockServer(ctx context.Context, inst Instance, ai *cloudHypervisorInstance, guestCIDR, hostIP string, port int) error {
	socketPath := ai.socketBase + "_" + strconv.Itoa(cloudHypervisorVsockPort)
	_ = os.Remove(socketPath)
	_ = os.Remove(ai.socketBase)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen guest vsock uds: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, _ *http.Request) {
		cfg := guestConfig{
			Env:      envWithPort(inst.Env, port),
			IPAddr:   guestCIDR,
			IPGW:     hostIP,
			Hostname: fmt.Sprintf("kindling-%s", inst.ID.String()[:8]),
			Port:     port,
		}
		if inst.PersistentVolume != nil {
			cfg.VolumeMountPath = inst.PersistentVolume.MountPath
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfg)
	})
	mux.HandleFunc("POST /logs", func(w http.ResponseWriter, req *http.Request) {
		scanner := bufio.NewScanner(req.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			ai.logMu.Lock()
			ai.logs = append(ai.logs, scanner.Text())
			if len(ai.logs) > 1000 {
				ai.logs = ai.logs[len(ai.logs)-1000:]
			}
			ai.logMu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /ready", func(w http.ResponseWriter, _ *http.Request) {
		ai.markReady()
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = lis.Close()
		r.cleanupGuestVsock(ai.socketBase)
	}()
	go func() {
		if err := srv.Serve(lis); err != nil && err != http.ErrServerClosed {
			slog.Debug("cloud-hypervisor vsock server ended", "error", err)
		}
	}()
	return nil
}

func (r *CloudHypervisorRuntime) cleanupGuestVsock(base string) {
	_ = os.Remove(base)
	_ = os.Remove(base + "_" + strconv.Itoa(cloudHypervisorVsockPort))
}

func dialCloudHypervisorGuestOverUDS(vsockUDS string, port uint32) (net.Conn, error) {
	return chbridge.DialGuestOverUDS(vsockUDS, port)
}

func (r *CloudHypervisorRuntime) waitCH(id uuid.UUID, ai *cloudHypervisorInstance) {
	err := ai.cmd.Wait()
	if ai.bridgeCmd != nil && ai.bridgeCmd.Process != nil {
		_ = terminatePID(ai.bridgeCmd.Process.Pid)
	}
	_ = os.Remove(cloudHypervisorVMPIDPath(ai.workDir))
	_ = os.Remove(cloudHypervisorBridgePIDPath(ai.workDir))
	removeCHTap(ai.tapName)
	r.cleanupGuestVsock(ai.socketBase)
	r.mu.Lock()
	delete(r.instances, id)
	retain := ai.retain
	r.mu.Unlock()
	if !retain {
		_ = os.RemoveAll(ai.workDir)
	}
	if err != nil {
		slog.Error("cloud-hypervisor VM exited", "id", id, "error", err)
	} else {
		slog.Info("cloud-hypervisor VM exited", "id", id)
	}
	close(ai.stopped)
}

func (r *CloudHypervisorRuntime) waitPreparedMigration(prepared *cloudHypervisorPreparedMigration) {
	if prepared == nil || prepared.cmd == nil {
		return
	}
	_ = prepared.cmd.Wait()
	r.mu.Lock()
	current, ok := r.prepared[prepared.id]
	if ok && current == prepared {
		delete(r.prepared, prepared.id)
	}
	r.mu.Unlock()
	_ = os.RemoveAll(prepared.workDir)
	close(prepared.stopped)
}

func (r *CloudHypervisorRuntime) Stop(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	if !ok {
		if prepared, prepOK := r.prepared[id]; prepOK {
			delete(r.prepared, id)
			r.mu.Unlock()
			if prepared.cmd != nil && prepared.cmd.Process != nil {
				_ = terminatePID(prepared.cmd.Process.Pid)
			}
			_ = os.RemoveAll(prepared.workDir)
			return nil
		}
		if s, suspended := r.suspended[id]; suspended {
			delete(r.suspended, id)
			r.mu.Unlock()
			if s.workDir != "" {
				_ = os.RemoveAll(s.workDir)
			}
			return nil
		}
	}
	r.mu.Unlock()
	if !ok {
		workDir := cloudHypervisorWorkDir(id)
		_ = terminatePIDFromFile(cloudHypervisorBridgePIDPath(workDir))
		_ = terminatePIDFromFile(cloudHypervisorVMPIDPath(workDir))
		_ = os.Remove(cloudHypervisorBridgePIDPath(workDir))
		_ = os.Remove(cloudHypervisorVMPIDPath(workDir))
		_ = os.RemoveAll(workDir)
		return nil
	}
	if ai.bridgeCmd != nil && ai.bridgeCmd.Process != nil {
		_ = terminatePID(ai.bridgeCmd.Process.Pid)
	}
	ai.cancel()
	return nil
}

func (r *CloudHypervisorRuntime) Healthy(ctx context.Context, id uuid.UUID) bool {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	return ok && ai.cmd != nil && ai.cmd.ProcessState == nil
}

func (r *CloudHypervisorRuntime) Logs(ctx context.Context, id uuid.UUID) ([]string, error) {
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

// ResourceStats pulls guest-agent /stats over the vsock bridge.
func (r *CloudHypervisorRuntime) ResourceStats(ctx context.Context, id uuid.UUID) (ResourceStats, error) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok || ai.socketBase == "" {
		return ResourceStats{}, ErrInstanceNotRunning
	}
	conn, err := dialCloudHypervisorGuestOverUDS(ai.socketBase, GuestStatsVsockPort)
	if err != nil {
		return ResourceStats{}, err
	}
	defer conn.Close()
	return resourceStatsFromGuestHTTP(ctx, conn)
}

func (r *CloudHypervisorRuntime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ai := range r.instances {
		ai.cancel()
	}
	for _, prepared := range r.prepared {
		if prepared.cmd != nil && prepared.cmd.Process != nil {
			_ = terminatePID(prepared.cmd.Process.Pid)
		}
	}
}

func (r *CloudHypervisorRuntime) captureOutputCH(ai *cloudHypervisorInstance, rd interface{ Read([]byte) (int, error) }) {
	scanner := bufio.NewScanner(rd)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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

func tailCloudHypervisorLogs(ai *cloudHypervisorInstance, max int) string {
	ai.logMu.Lock()
	defer ai.logMu.Unlock()
	if len(ai.logs) == 0 {
		return ""
	}
	if max > len(ai.logs) {
		max = len(ai.logs)
	}
	return strings.Join(ai.logs[len(ai.logs)-max:], "\n")
}

func cloudHypervisorWorkDir(id uuid.UUID) string {
	return filepath.Join(os.TempDir(), "kindling-ch-"+id.String())
}

func cloudHypervisorVMPIDPath(workDir string) string {
	return filepath.Join(workDir, "cloud-hypervisor.pid")
}

func cloudHypervisorBridgePIDPath(workDir string) string {
	return filepath.Join(workDir, "cloud-hypervisor-bridge.pid")
}

func startCloudHypervisorBridgeHelper(hostPort int, vsockUDS string) (*exec.Cmd, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin,
		"ch-bridge-proxy",
		"--listen", net.JoinHostPort("0.0.0.0", strconv.Itoa(hostPort)),
		"--vsock", vsockUDS,
		"--guest-port", strconv.Itoa(cloudHypervisorGuestBridgeVsockPort),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

type cloudHypervisorPingResponse struct {
	Version string `json:"version"`
}

func (r *CloudHypervisorRuntime) sharedRootfsPath(id uuid.UUID) (string, bool) {
	if strings.TrimSpace(r.sharedRootfsDir) == "" {
		return "", false
	}
	return sharedRootfsPathForID(r.sharedRootfsDir, id), true
}

func sharedRootfsPathForID(base string, id uuid.UUID) string {
	return filepath.Join(base, id.String(), "rootfs.qcow2")
}

func sharedRootfsRefFromWorkDisk(sharedDir, workDisk string) string {
	if strings.TrimSpace(sharedDir) == "" {
		return ""
	}
	cleanDisk := filepath.Clean(workDisk)
	cleanBase := filepath.Clean(sharedDir) + string(os.PathSeparator)
	if strings.HasPrefix(cleanDisk, cleanBase) {
		return cleanDisk
	}
	return ""
}

func waitForCloudHypervisorAPI(ctx context.Context, apiSocket string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		client := cloudHypervisorAPIClient(apiSocket)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/v1/vmm.ping", nil)
		if err == nil {
			resp, callErr := client.Do(req)
			if callErr == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("cloud-hypervisor api socket %s did not become ready", apiSocket)
}

func cloudHypervisorAPIClient(apiSocket string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", apiSocket)
			},
		},
	}
}

func (r *CloudHypervisorRuntime) pingVMM(ctx context.Context, apiSocket string) (cloudHypervisorPingResponse, error) {
	var out cloudHypervisorPingResponse
	client := cloudHypervisorAPIClient(apiSocket)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/v1/vmm.ping", nil)
	if err != nil {
		return out, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, fmt.Errorf("cloud-hypervisor ping: %s", strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

func (r *CloudHypervisorRuntime) putVMM(ctx context.Context, apiSocket, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := cloudHypervisorAPIClient(apiSocket)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/api/v1"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("cloud-hypervisor api %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func writePIDFile(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600)
}

func terminatePIDFromFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return err
	}
	return terminatePID(pid)
}

func terminatePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func waitForTCPPort(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for %s", addr)
}

func cloudHypervisorIPs(slot uint32) (string, string, error) {
	base := netip.MustParseAddr("10.0.0.0")
	host := base
	for i := uint32(0); i < slot*2; i++ {
		host = host.Next()
	}
	guest := host.Next()
	if !guest.IsValid() {
		return "", "", fmt.Errorf("invalid guest ip for slot %d", slot)
	}
	return host.String(), guest.String() + "/31", nil
}

func cloudHypervisorTapName(id uuid.UUID, slot uint32) string {
	// Linux interface names max out at 15 chars. Keep enough deployment entropy to
	// avoid collisions across process restarts while still leaving room for retries.
	return fmt.Sprintf("kch%s%x", id.String()[:8], slot&0xf)
}

func exportImageToDir(ctx context.Context, imageRef, rootfsDir string, auth *oci.Auth) (string, error) {
	if err := oci.ExportImageRootfs(ctx, imageRef, rootfsDir, auth); err != nil {
		return "", err
	}
	return rootfsDir, nil
}

func envWithPort(env []string, port int) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "PORT=") {
			continue
		}
		out = append(out, e)
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

func (ai *cloudHypervisorInstance) markReady() {
	ai.once.Do(func() {
		close(ai.ready)
	})
}

func firstExistingPath(paths ...string) string {
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, p := range paths {
		if strings.TrimSpace(p) != "" {
			return p
		}
	}
	return ""
}

func createCHTap(tapName string, hostIP netip.Addr) error {
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: tapName},
		Mode:      netlink.TUNTAP_MODE_TAP,
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return fmt.Errorf("create TAP %s: %w", tapName, err)
	}
	link, err := netlink.LinkByName(tapName)
	if err != nil {
		return fmt.Errorf("find TAP %s: %w", tapName, err)
	}
	addr := &netlink.Addr{
		IPNet: &net.IPNet{IP: hostIP.AsSlice(), Mask: net.CIDRMask(31, 32)},
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("add addr to %s: %w", tapName, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring up %s: %w", tapName, err)
	}
	return nil
}

func cloudHypervisorDiskArgs(workDisk string, vol *PersistentVolumeMount) []string {
	args := []string{"--disk", fmt.Sprintf("path=%s,direct=off", workDisk)}
	if vol != nil && strings.TrimSpace(vol.HostPath) != "" {
		args = append(args, "--disk", fmt.Sprintf("path=%s,direct=off", vol.HostPath))
	}
	return args
}

func (r *CloudHypervisorRuntime) ensurePersistentVolumeDisk(ctx context.Context, vol *PersistentVolumeMount) error {
	if vol == nil {
		return nil
	}
	if strings.TrimSpace(vol.HostPath) == "" {
		return fmt.Errorf("persistent volume host path is required")
	}
	if vol.SizeGB <= 0 {
		return fmt.Errorf("persistent volume size must be positive")
	}
	if err := ensureDir(filepath.Dir(vol.HostPath)); err != nil {
		return fmt.Errorf("create volume dir: %w", err)
	}
	if _, err := os.Stat(vol.HostPath); err == nil {
		return ensurePersistentVolumeSize(ctx, vol.HostPath, vol.SizeGB)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat volume disk: %w", err)
	} else if !vol.CreateIfMissing {
		return fmt.Errorf("persistent volume disk does not exist at %s", vol.HostPath)
	}

	emptyDir, err := os.MkdirTemp("", "kindling-volume-empty-")
	if err != nil {
		return fmt.Errorf("create temp volume source dir: %w", err)
	}
	defer os.RemoveAll(emptyDir)

	sizeArg := fmt.Sprintf("%dG", vol.SizeGB)
	if out, err := exec.CommandContext(ctx, "virt-make-fs", "--format=qcow2", "--type=ext4", "--size="+sizeArg, emptyDir, vol.HostPath).CombinedOutput(); err != nil {
		return fmt.Errorf("virt-make-fs volume: %s: %w", string(out), err)
	}
	return nil
}

func ensurePersistentVolumeSize(ctx context.Context, path string, sizeGB int) error {
	currentSize, err := qcow2VirtualSize(path)
	if err != nil {
		return err
	}
	targetSize := int64(sizeGB) * 1024 * 1024 * 1024
	if currentSize >= targetSize {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "qemu-img", "resize", path, fmt.Sprintf("%dG", sizeGB)).CombinedOutput(); err != nil {
		return fmt.Errorf("qemu-img resize: %s: %w", string(out), err)
	}
	return nil
}

func qcow2VirtualSize(path string) (int64, error) {
	out, err := exec.Command("qemu-img", "info", "--output=json", path).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("qemu-img info: %s: %w", string(out), err)
	}
	var meta struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal(out, &meta); err != nil {
		return 0, fmt.Errorf("decode qemu-img info: %w", err)
	}
	if meta.VirtualSize <= 0 {
		return 0, fmt.Errorf("qemu-img info returned invalid virtual size for %s", path)
	}
	return meta.VirtualSize, nil
}

func removeCHTap(tapName string) {
	if link, err := netlink.LinkByName(tapName); err == nil {
		_ = netlink.LinkDel(link)
	}
}

func ioCopyClose(dst, src net.Conn) (int64, error) {
	n, err := io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	return n, err
}
