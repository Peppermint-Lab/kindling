//go:build linux

package builder

import (
	"bufio"
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
	"github.com/vishvananda/netlink"
)

const (
	chExecConfigPort    = uint32(1024)
	chExecGuestCIDBase  = uint32(4096)
	chExecGuestExecPort = uint32(1027)
	chExecVCPU          = 4
	chExecMemoryMB      = 8192
	chExecReadyTimeout  = 120 * time.Second
	chExecMaxSlots      = uint32(1 << 15) // 10.30.0.0/16 split into /31s
)

var chExecNextSlot atomic.Uint32

type CloudHypervisorExecRunnerConfig struct {
	BinaryPath       string
	KernelPath       string
	InitramfsPath    string
	BuilderRootfsDir string
	VirtiofsdPath    string
}

type CloudHypervisorExecRunner struct {
	cfg resolvedCloudHypervisorExecRunnerConfig

	mu sync.Mutex
	vm *cloudHypervisorExecVM
}

type resolvedCloudHypervisorExecRunnerConfig struct {
	binaryPath       string
	kernelPath       string
	initramfsPath    string
	builderRootfsDir string
	virtiofsdPath    string
}

type cloudHypervisorExecVM struct {
	binaryPath       string
	kernelPath       string
	initramfsPath    string
	builderRootfsDir string
	virtiofsdPath    string
	workspaceDir     string
	dummyAppDir      string
	runtimeDir       string
	socketBase       string
	apiSocket        string
	tapName          string
	guestIP          string
	hostCIDR         string
	hostGW           string
	guestCID         uint32

	mu         sync.Mutex
	started    bool
	cancel     context.CancelFunc
	cmd        *exec.Cmd
	virtiofsds []*exec.Cmd

	readyOnce sync.Once
	readyCh   chan struct{}

	diagMu    sync.Mutex
	diagLines []string
}

func NewCloudHypervisorExecRunner(cfg CloudHypervisorExecRunnerConfig) (*CloudHypervisorExecRunner, error) {
	resolved, err := resolveCloudHypervisorExecRunnerConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &CloudHypervisorExecRunner{cfg: resolved}, nil
}

func (r *CloudHypervisorExecRunner) Backend() string { return "cloud_hypervisor" }

func (r *CloudHypervisorExecRunner) Close() error {
	r.mu.Lock()
	vm := r.vm
	r.vm = nil
	r.mu.Unlock()
	if vm != nil {
		vm.Close()
	}
	return nil
}

func (r *CloudHypervisorExecRunner) Exec(ctx context.Context, run ExecRun) (int, error) {
	r.mu.Lock()
	if r.vm == nil {
		vm, err := newCloudHypervisorExecVM(r.cfg, run.WorkspaceDir)
		if err != nil {
			r.mu.Unlock()
			return 0, err
		}
		if err := vm.start(ctx, run.LogLine); err != nil {
			vm.Close()
			r.mu.Unlock()
			return 0, err
		}
		r.vm = vm
	}
	vm := r.vm
	r.mu.Unlock()

	return vm.Exec(ctx, run.Argv, run.Cwd, run.Env, run.LogLine)
}

func resolveCloudHypervisorExecRunnerConfig(cfg CloudHypervisorExecRunnerConfig) (resolvedCloudHypervisorExecRunnerConfig, error) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return resolvedCloudHypervisorExecRunnerConfig{}, fmt.Errorf("/dev/kvm is required for cloud-hypervisor CI execution: %w", err)
	}
	home, _ := os.UserHomeDir()
	binaryPath := strings.TrimSpace(cfg.BinaryPath)
	if binaryPath == "" {
		if p, err := exec.LookPath("cloud-hypervisor"); err == nil {
			binaryPath = p
		} else {
			binaryPath = "/usr/local/bin/cloud-hypervisor"
		}
	}
	if _, err := os.Stat(binaryPath); err != nil {
		return resolvedCloudHypervisorExecRunnerConfig{}, fmt.Errorf("cloud-hypervisor not found at %s: %w", binaryPath, err)
	}
	kernelPath := firstExistingPath(strings.TrimSpace(cfg.KernelPath), "/data/vmlinux-ch.bin", filepath.Join(home, ".kindling", "vmlinuz.bin"))
	if kernelPath == "" {
		return resolvedCloudHypervisorExecRunnerConfig{}, fmt.Errorf("cloud-hypervisor kernel not found")
	}
	initramfsPath := firstExistingPath(strings.TrimSpace(cfg.InitramfsPath), "/data/initramfs.cpio.gz", filepath.Join(home, ".kindling", "initramfs.cpio.gz"))
	if initramfsPath == "" {
		return resolvedCloudHypervisorExecRunnerConfig{}, fmt.Errorf("cloud-hypervisor initramfs not found")
	}
	builderRootfsDir := firstExistingPath(strings.TrimSpace(cfg.BuilderRootfsDir), filepath.Join(home, ".kindling", "builder-rootfs"), "/data/builder-rootfs")
	if builderRootfsDir == "" {
		return resolvedCloudHypervisorExecRunnerConfig{}, fmt.Errorf("builder rootfs directory not found")
	}
	virtiofsdPath := strings.TrimSpace(cfg.VirtiofsdPath)
	if virtiofsdPath == "" {
		if p, err := exec.LookPath("virtiofsd"); err == nil {
			virtiofsdPath = p
		} else {
			virtiofsdPath = firstExistingPath("/usr/libexec/virtiofsd", "/usr/lib/qemu/virtiofsd")
		}
	}
	if virtiofsdPath == "" {
		return resolvedCloudHypervisorExecRunnerConfig{}, fmt.Errorf("virtiofsd not found")
	}
	if _, err := os.Stat(virtiofsdPath); err != nil {
		return resolvedCloudHypervisorExecRunnerConfig{}, fmt.Errorf("virtiofsd not found at %s: %w", virtiofsdPath, err)
	}
	return resolvedCloudHypervisorExecRunnerConfig{
		binaryPath:       binaryPath,
		kernelPath:       kernelPath,
		initramfsPath:    initramfsPath,
		builderRootfsDir: builderRootfsDir,
		virtiofsdPath:    virtiofsdPath,
	}, nil
}

func newCloudHypervisorExecVM(cfg resolvedCloudHypervisorExecRunnerConfig, workspaceDir string) (*cloudHypervisorExecVM, error) {
	runtimeDir, err := os.MkdirTemp("", "kindling-ci-ch-*")
	if err != nil {
		return nil, err
	}
	dummy, err := os.MkdirTemp("", "kindling-builder-app-*")
	if err != nil {
		_ = os.RemoveAll(runtimeDir)
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dummy, ".keep"), []byte{}, 0o644); err != nil {
		_ = os.RemoveAll(runtimeDir)
		_ = os.RemoveAll(dummy)
		return nil, err
	}
	slot, err := reserveCHExecSlot()
	if err != nil {
		_ = os.RemoveAll(runtimeDir)
		_ = os.RemoveAll(dummy)
		return nil, err
	}
	hostGW, guestCIDR, err := chExecIPs(slot)
	if err != nil {
		_ = os.RemoveAll(runtimeDir)
		_ = os.RemoveAll(dummy)
		return nil, err
	}
	guestPrefix, err := netip.ParsePrefix(guestCIDR)
	if err != nil {
		_ = os.RemoveAll(runtimeDir)
		_ = os.RemoveAll(dummy)
		return nil, err
	}
	id := uuid.New()
	guestCID := chExecGuestCIDBase + slot
	return &cloudHypervisorExecVM{
		binaryPath:       cfg.binaryPath,
		kernelPath:       cfg.kernelPath,
		initramfsPath:    cfg.initramfsPath,
		builderRootfsDir: cfg.builderRootfsDir,
		virtiofsdPath:    cfg.virtiofsdPath,
		workspaceDir:     workspaceDir,
		dummyAppDir:      dummy,
		runtimeDir:       runtimeDir,
		socketBase:       filepath.Join(runtimeDir, "vsock.sock"),
		apiSocket:        filepath.Join(runtimeDir, "api.sock"),
		tapName:          chExecTapName(id, slot),
		guestIP:          guestPrefix.Addr().String(),
		hostCIDR:         guestCIDR,
		hostGW:           hostGW,
		guestCID:         guestCID,
		readyCh:          make(chan struct{}),
	}, nil
}

func (v *cloudHypervisorExecVM) Close() {
	v.mu.Lock()
	cancel := v.cancel
	cmd := v.cmd
	virtiofsds := append([]*exec.Cmd(nil), v.virtiofsds...)
	tapName := v.tapName
	runtimeDir := v.runtimeDir
	dummyDir := v.dummyAppDir
	v.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, proc := range virtiofsds {
		if proc != nil && proc.Process != nil {
			_ = proc.Process.Kill()
			_, _ = proc.Process.Wait()
		}
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	removeCHExecTap(tapName)
	_ = os.RemoveAll(runtimeDir)
	_ = os.RemoveAll(dummyDir)
}

func (v *cloudHypervisorExecVM) start(parentCtx context.Context, logLine func(string)) error {
	v.mu.Lock()
	if v.started {
		v.mu.Unlock()
		return nil
	}
	v.mu.Unlock()

	hostAddr, err := netip.ParseAddr(v.hostGW)
	if err != nil {
		return err
	}
	removeCHExecTap(v.tapName)
	if err := createCHExecTap(v.tapName, hostAddr); err != nil {
		return err
	}
	v.emitDiagnostic(logLine, fmt.Sprintf("Starting cloud-hypervisor CI VM with tap=%s guest_ip=%s guest_cid=%d", v.tapName, v.guestIP, v.guestCID))
	if err := v.startVirtiofsd("app", v.dummyAppDir, logLine); err != nil {
		removeCHExecTap(v.tapName)
		return err
	}
	if err := v.startVirtiofsd("workspace", v.workspaceDir, logLine); err != nil {
		v.Close()
		return err
	}
	if err := v.startVirtiofsd("builder", v.builderRootfsDir, logLine); err != nil {
		v.Close()
		return err
	}
	if err := v.startVsockConfigServer(parentCtx); err != nil {
		v.Close()
		return err
	}

	runCtx, cancel := context.WithCancel(parentCtx)
	args := []string{
		"--kernel", v.kernelPath,
		"--initramfs", v.initramfsPath,
		"--cmdline", "console=hvc0",
		"--cpus", fmt.Sprintf("boot=%d", chExecVCPU),
		"--memory", fmt.Sprintf("size=%dM,shared=on", chExecMemoryMB),
		"--net", fmt.Sprintf("tap=%s,ip=%s,mask=255.255.255.254", v.tapName, v.guestIP),
		"--vsock", fmt.Sprintf("cid=%d,socket=%s", v.guestCID, v.socketBase),
		"--api-socket", v.apiSocket,
		"--fs",
		fmt.Sprintf("tag=app,socket=%s,num_queues=1,queue_size=1024", v.virtiofsSocketPath("app")),
		fmt.Sprintf("tag=workspace,socket=%s,num_queues=1,queue_size=1024", v.virtiofsSocketPath("workspace")),
		fmt.Sprintf("tag=builder,socket=%s,num_queues=1,queue_size=1024", v.virtiofsSocketPath("builder")),
	}
	cmd := exec.CommandContext(runCtx, v.binaryPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		cancel()
		v.Close()
		return fmt.Errorf("start cloud-hypervisor: %w", err)
	}
	go v.captureProcessOutput(logLine, "cloud-hypervisor", stdout)
	go v.captureProcessOutput(logLine, "cloud-hypervisor", stderr)

	v.mu.Lock()
	v.cmd = cmd
	v.cancel = cancel
	v.started = true
	v.mu.Unlock()

	if err := waitCHExecGuestReady(runCtx, v.readyCh, chExecReadyTimeout); err != nil {
		cancel()
		v.Close()
		return fmt.Errorf("%w; diagnostics: %s", err, v.diagnosticTail())
	}

	go func() {
		<-runCtx.Done()
		v.Close()
	}()
	return nil
}

func (v *cloudHypervisorExecVM) Exec(ctx context.Context, argv []string, cwd string, extraEnv []string, logLine func(string)) (int, error) {
	conn, err := chbridge.DialGuestOverUDS(v.socketBase, chExecGuestExecPort)
	if err != nil {
		return 0, fmt.Errorf("dial guest exec: %w; diagnostics: %s", err, v.diagnosticTail())
	}
	defer conn.Close()

	payload, err := json.Marshal(map[string]any{
		"argv": argv,
		"cwd":  firstNonEmpty(cwd, "/workspace"),
		"env":  extraEnv,
	})
	if err != nil {
		return 0, err
	}
	reqStr := fmt.Sprintf("POST /exec HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		len(payload), string(payload))
	if _, err := io.WriteString(conn, reqStr); err != nil {
		return 0, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return 0, fmt.Errorf("read exec response: %w; diagnostics: %s", err, v.diagnosticTail())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("exec HTTP %d: %s", resp.StatusCode, string(body))
	}

	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	code := -1
	for _, line := range lines {
		if strings.HasPrefix(line, "KINDLING_EXIT_CODE ") {
			c, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "KINDLING_EXIT_CODE ")))
			if err == nil {
				code = c
			}
			continue
		}
		if logLine != nil && line != "" {
			logLine(line)
		}
	}
	if code < 0 {
		return 0, fmt.Errorf("missing exit code in guest response")
	}
	return code, nil
}

func (v *cloudHypervisorExecVM) startVirtiofsd(tag, dir string, logLine func(string)) error {
	socketPath := v.virtiofsSocketPath(tag)
	_ = os.Remove(socketPath)
	cmd := exec.Command(v.virtiofsdPath, "--socket-path", socketPath, "--shared-dir", dir)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start virtiofsd for %s: %w", tag, err)
	}
	v.emitDiagnostic(logLine, fmt.Sprintf("Started virtiofsd for %s from %s", tag, dir))
	go v.captureProcessOutput(logLine, "virtiofsd["+tag+"]", stdout)
	go v.captureProcessOutput(logLine, "virtiofsd["+tag+"]", stderr)
	if err := waitForUnixSocket(socketPath, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return fmt.Errorf("wait for virtiofsd socket %s: %w; diagnostics: %s", tag, err, v.diagnosticTail())
	}
	v.mu.Lock()
	v.virtiofsds = append(v.virtiofsds, cmd)
	v.mu.Unlock()
	return nil
}

func (v *cloudHypervisorExecVM) virtiofsSocketPath(tag string) string {
	return filepath.Join(v.runtimeDir, tag+".virtiofsd.sock")
}

func (v *cloudHypervisorExecVM) startVsockConfigServer(ctx context.Context) error {
	socketPath := v.socketBase + "_" + strconv.Itoa(int(chExecConfigPort))
	_ = os.Remove(socketPath)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen config socket: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, _ *http.Request) {
		v.emitDiagnostic(nil, "CI guest requested host config over vsock")
		cfg := map[string]any{
			"mode":        "ci",
			"ip_addr":     v.hostCIDR,
			"ip_gw":       v.hostGW,
			"dns_servers": []string{"1.1.1.1", "8.8.8.8"},
			"hostname":    "kindling-builder",
			"port":        3000,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfg)
	})
	mux.HandleFunc("POST /logs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /ready", func(w http.ResponseWriter, _ *http.Request) {
		v.emitDiagnostic(nil, "CI guest reported ready")
		v.readyOnce.Do(func() { close(v.readyCh) })
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = lis.Close()
		_ = os.Remove(socketPath)
	}()
	go func() {
		_ = srv.Serve(lis)
	}()
	return nil
}

func waitCHExecGuestReady(ctx context.Context, ready <-chan struct{}, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("timed out after %s waiting for cloud-hypervisor CI guest", timeout)
	}
}

func waitForUnixSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for unix socket %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func firstExistingPath(paths ...string) string {
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func chExecIPs(slot uint32) (string, string, error) {
	base := netip.MustParseAddr("10.30.0.0")
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

func reserveCHExecSlot() (uint32, error) {
	start := chExecNextSlot.Add(1) - 1
	for i := uint32(0); i < chExecMaxSlots; i++ {
		slot := (start + i) % chExecMaxSlots
		_, guestCIDR, err := chExecIPs(slot)
		if err != nil {
			return 0, err
		}
		inUse, err := chExecCIDRInUse(guestCIDR)
		if err != nil {
			return 0, err
		}
		if !inUse {
			chExecNextSlot.Store(slot + 1)
			return slot, nil
		}
	}
	return 0, fmt.Errorf("no available cloud-hypervisor CI network slots")
}

func chExecCIDRInUse(cidr string) (bool, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false, fmt.Errorf("parse exec CIDR %q: %w", cidr, err)
	}
	prefix = prefix.Masked()
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return false, fmt.Errorf("list IPv4 routes: %w", err)
	}
	for _, route := range routes {
		if route.Dst == nil {
			continue
		}
		if route.Dst.IP == nil {
			continue
		}
		existing, ok := netip.AddrFromSlice(route.Dst.IP)
		if !ok {
			continue
		}
		ones, bits := route.Dst.Mask.Size()
		if bits != 32 {
			continue
		}
		if existing == prefix.Addr() && ones == prefix.Bits() {
			return true, nil
		}
	}
	return false, nil
}

func chExecTapName(id uuid.UUID, slot uint32) string {
	return fmt.Sprintf("kci%s%x", id.String()[:8], slot&0xf)
}

func createCHExecTap(tapName string, hostIP netip.Addr) error {
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

func removeCHExecTap(tapName string) {
	if link, err := netlink.LinkByName(tapName); err == nil {
		_ = netlink.LinkDel(link)
	}
}

func (v *cloudHypervisorExecVM) captureProcessOutput(logLine func(string), prefix string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := fmt.Sprintf("%s: %s", prefix, strings.TrimSpace(scanner.Text()))
		v.emitDiagnostic(logLine, line)
	}
	if err := scanner.Err(); err != nil {
		v.emitDiagnostic(logLine, fmt.Sprintf("%s stream error: %v", prefix, err))
	}
}

func (v *cloudHypervisorExecVM) emitDiagnostic(logLine func(string), line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	v.diagMu.Lock()
	v.diagLines = append(v.diagLines, line)
	if len(v.diagLines) > 80 {
		v.diagLines = append([]string(nil), v.diagLines[len(v.diagLines)-80:]...)
	}
	v.diagMu.Unlock()
	slog.Info("cloud-hypervisor CI diagnostic", "tap", v.tapName, "message", line)
	if logLine != nil {
		logLine(line)
	}
}

func (v *cloudHypervisorExecVM) diagnosticTail() string {
	v.diagMu.Lock()
	defer v.diagMu.Unlock()
	if len(v.diagLines) == 0 {
		return "no diagnostics captured"
	}
	return strings.Join(v.diagLines, " | ")
}
