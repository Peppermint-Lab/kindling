//go:build linux

package runtime

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
	"github.com/kindlingvm/kindling/internal/oci"
	"github.com/vishvananda/netlink"
)

const (
	cloudHypervisorDefaultBin       = "/usr/local/bin/cloud-hypervisor"
	cloudHypervisorDefaultKernel    = "/data/vmlinux-ch.bin"
	cloudHypervisorDefaultInitramfs = "/data/initramfs.cpio.gz"
	cloudHypervisorVsockPort = 1024
	// Must match cmd/guest-agent tcpBridgeVsockPort. Host→guest uses the Firecracker/CH
	// UDS protocol on --vsock socket= (CONNECT <port>\\n), not AF_VSOCK.
	cloudHypervisorGuestBridgeVsockPort = 1025
)

type CloudHypervisorRuntime struct {
	mu        sync.Mutex
	instances map[uuid.UUID]*cloudHypervisorInstance
	nextSlot  atomic.Uint32

	binaryPath        string
	kernelPath        string
	initramfsPath     string
	advertiseHost     string
	pullAuth          *oci.Auth
}

type cloudHypervisorInstance struct {
	cmd        *exec.Cmd
	tapName    string
	ip         string
	vmIP       string
	workDir    string
	socketBase string
	hostFwd    net.Listener
	logs       []string
	logMu      sync.Mutex
	cancel     context.CancelFunc
	ready      chan struct{}
	once       sync.Once
}

type guestConfig struct {
	Env      []string `json:"env"`
	IPAddr   string   `json:"ip_addr"`
	IPGW     string   `json:"ip_gw"`
	Hostname string   `json:"hostname"`
	Port     int      `json:"port"`
}

// NewCloudHypervisorRuntime builds paths from cfg and defaults (no environment variables).
func NewCloudHypervisorRuntime(cfg CloudHypervisorHostConfig, advertiseHost string, pullAuth *oci.Auth) *CloudHypervisorRuntime {
	home, _ := os.UserHomeDir()
	homeKernel := filepath.Join(home, ".kindling", "vmlinuz.bin")
	homeInitramfs := filepath.Join(home, ".kindling", "initramfs.cpio.gz")
	return &CloudHypervisorRuntime{
		instances:     make(map[uuid.UUID]*cloudHypervisorInstance),
		binaryPath:    firstExistingPath(cfg.BinaryPath, cloudHypervisorDefaultBin),
		// Note: do not fall back to /data/vmlinux.bin — provision.sh stores rust hypervisor firmware there, not a Linux bzImage/vmlinux.
		kernelPath:    firstExistingPath(cfg.KernelPath, cloudHypervisorDefaultKernel, homeKernel),
		initramfsPath: firstExistingPath(cfg.InitramfsPath, cloudHypervisorDefaultInitramfs, homeInitramfs),
		advertiseHost: strings.TrimSpace(advertiseHost),
		pullAuth:      pullAuth,
	}
}

func (r *CloudHypervisorRuntime) Name() string { return "cloud-hypervisor" }

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
	port := inst.Port
	if port == 0 {
		port = 3000
	}

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
	if out, err := exec.CommandContext(ctx, "virt-make-fs", "--format=qcow2", "--type=ext4", "--size=+2G", rootfsDir, workDisk).CombinedOutput(); err != nil {
		_ = os.RemoveAll(workDir)
		return "", fmt.Errorf("virt-make-fs: %s: %w", string(out), err)
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
		tapName:    tapName,
		vmIP:       guestIP,
		workDir:    workDir,
		socketBase: filepath.Join(os.TempDir(), "kindling-vsock-"+inst.ID.String()+".sock"),
		cancel:     cancel,
		ready:      make(chan struct{}),
	}

	if err := r.startGuestVsockServer(runCtx, inst, ai, guestCIDR, hostIP, port); err != nil {
		cancel()
		removeCHTap(tapName)
		_ = os.RemoveAll(workDir)
		return "", err
	}

	cmd := exec.CommandContext(runCtx, r.binaryPath,
		"--kernel", r.kernelPath,
		"--initramfs", r.initramfsPath,
		"--cmdline", "console=hvc0",
		"--cpus", fmt.Sprintf("boot=%d", inst.VCPUs),
		"--memory", fmt.Sprintf("size=%dM", inst.MemoryMB),
		"--disk", fmt.Sprintf("path=%s,direct=off", workDisk),
		"--net", fmt.Sprintf("tap=%s,ip=%s,mask=255.255.255.254", tapName, hostIP),
		"--vsock", fmt.Sprintf("cid=3,socket=%s", ai.socketBase),
	)
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

	hostLn, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		cancel()
		return "", fmt.Errorf("host tcp forward listen: %w", err)
	}
	ai.hostFwd = hostLn
		ai.ip, err = applyAdvertisedHost(hostLn.Addr().String(), r.advertiseHost)
	if err != nil {
		cancel()
		return "", err
	}
	go r.forwardHostTCPToVM(runCtx, hostLn, ai.socketBase)
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

func (r *CloudHypervisorRuntime) forwardHostTCPToVM(ctx context.Context, hostLn net.Listener, vsockUDS string) {
	go func() {
		<-ctx.Done()
		_ = hostLn.Close()
	}()
	for {
		conn, err := hostLn.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Debug("cloud-hypervisor host forward ended", "error", err)
				return
			}
		}
		go r.relayHostTCPToGuestVsock(conn, vsockUDS)
	}
}

// relayHostTCPToGuestVsock forwards one inbound TCP connection to the guest app over
// virtio-vsock. The app binds 127.0.0.1; dialing guest eth0:port does not work (unlike
// Docker publish). Cloud Hypervisor uses the Firecracker UDS vsock: Unix-connect to
// --vsock socket= and send "CONNECT <port>\n" (see cloud-hypervisor docs/vsock.md).
func (r *CloudHypervisorRuntime) relayHostTCPToGuestVsock(client net.Conn, vsockUDS string) {
	defer client.Close()
	back, err := dialCloudHypervisorGuestOverUDS(vsockUDS, cloudHypervisorGuestBridgeVsockPort)
	if err != nil {
		slog.Debug("cloud-hypervisor vsock to guest failed", "error", err)
		return
	}
	defer back.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = ioCopyClose(back, client)
	}()
	go func() {
		defer wg.Done()
		_, _ = ioCopyClose(client, back)
	}()
	wg.Wait()
}

func dialCloudHypervisorGuestOverUDS(vsockUDS string, port uint32) (net.Conn, error) {
	c, err := net.Dial("unix", vsockUDS)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(c, "CONNECT %d\n", port); err != nil {
		_ = c.Close()
		return nil, err
	}
	// Firecracker/CH vsock: host must consume "OK <cid-or-port>\\n" before application data.
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vsock connect ack: %w", err)
	}
	if len(line) < 3 || line[0] != 'O' || line[1] != 'K' {
		_ = c.Close()
		return nil, fmt.Errorf("vsock connect ack: got %q", strings.TrimSpace(line))
	}
	// br wraps c; after the ACK line, all reads must go through br so peek-ahead stays in order.
	return &vsockBridgedConn{Conn: c, br: br}, nil
}

type vsockBridgedConn struct {
	net.Conn
	br *bufio.Reader
}

func (v *vsockBridgedConn) Read(b []byte) (int, error) { return v.br.Read(b) }

func (r *CloudHypervisorRuntime) waitCH(id uuid.UUID, ai *cloudHypervisorInstance) {
	err := ai.cmd.Wait()
	if ai.hostFwd != nil {
		_ = ai.hostFwd.Close()
	}
	removeCHTap(ai.tapName)
	r.cleanupGuestVsock(ai.socketBase)
	_ = os.RemoveAll(ai.workDir)
	r.mu.Lock()
	delete(r.instances, id)
	r.mu.Unlock()
	if err != nil {
		slog.Error("cloud-hypervisor VM exited", "id", id, "error", err)
	} else {
		slog.Info("cloud-hypervisor VM exited", "id", id)
	}
}

func (r *CloudHypervisorRuntime) Stop(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok {
		return nil
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

func (r *CloudHypervisorRuntime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ai := range r.instances {
		ai.cancel()
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
