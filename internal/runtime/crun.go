package runtime

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/oci"
)

// CrunRuntime runs OCI containers via crun (no Docker daemon).
type CrunRuntime struct {
	mu            sync.Mutex
	instances     map[uuid.UUID]*crunInstance
	suspended     map[uuid.UUID]*crunSuspended
	templates     map[string]*crunTemplate
	advertiseHost string
	pullAuth      *oci.Auth
}

type crunInstance struct {
	id        uuid.UUID
	inst      Instance
	cmd       *exec.Cmd
	ip        string
	hostPort  int
	bundleDir string
	ociDir    string
	logs      []string
	logMu     sync.Mutex
	cancel    context.CancelFunc
	retain    bool
	stopped   chan struct{}
}

type crunSuspended struct {
	inst      Instance
	bundleDir string
	ociDir    string
	hostPort  int
	ip        string
}

type crunTemplate struct {
	path string
}

// NewCrunRuntime creates a new crun-based runtime. crun must be installed.
// advertiseHost rewrites published 127.0.0.1/0.0.0.0 addresses for browser reachability (same as former KINDLING_RUNTIME_ADVERTISE_HOST).
// pullAuth is optional registry credentials for skopeo pulls.
func NewCrunRuntime(advertiseHost string, pullAuth *oci.Auth) *CrunRuntime {
	if _, err := exec.LookPath("crun"); err != nil {
		slog.Error("crun is required for the OCI fallback runtime on Linux; install crun (e.g. apt install crun)",
			"err", err)
	}
	return &CrunRuntime{
		instances:     make(map[uuid.UUID]*crunInstance),
		suspended:     make(map[uuid.UUID]*crunSuspended),
		templates:     make(map[string]*crunTemplate),
		advertiseHost: strings.TrimSpace(advertiseHost),
		pullAuth:      pullAuth,
	}
}

func (r *CrunRuntime) Name() string { return "crun" }
func (r *CrunRuntime) Supports(cap Capability) bool {
	return cap == CapabilitySuspendResume || cap == CapabilityWarmClone
}

func (r *CrunRuntime) Start(ctx context.Context, inst Instance) (string, error) {
	if _, err := exec.LookPath("crun"); err != nil {
		return "", fmt.Errorf("crun not found in PATH: %w (install crun for Linux deployments without KVM)", err)
	}
	if inst.PersistentVolume != nil {
		return "", ErrPersistentVolumesUnsupported
	}
	return r.startCrun(ctx, inst)
}

func (r *CrunRuntime) startCrun(ctx context.Context, inst Instance) (string, error) {
	// Prepare OCI bundle from image.
	bundleDir := fmt.Sprintf("/tmp/kindling-bundle-%s", inst.ID)
	os.MkdirAll(bundleDir, 0o755)

	// Extract OCI image to bundle using umoci.
	imageRef := inst.ImageRef
	ociDir := fmt.Sprintf("/tmp/kindling-oci-%s", inst.ID)

	if err := oci.PullToOCILayout(ctx, imageRef, ociDir, r.pullAuth); err != nil {
		return "", err
	}

	if err := oci.UmociUnpack(ctx, ociDir+":latest", bundleDir); err != nil {
		return "", err
	}

	if err := patchBundleHostNetwork(bundleDir); err != nil {
		return "", fmt.Errorf("host-network oci patch: %w", err)
	}
	return r.startPreparedCrun(ctx, inst, bundleDir, ociDir, 0)
}

func (r *CrunRuntime) startPreparedCrun(ctx context.Context, inst Instance, bundleDir, ociDir string, hostPort int) (string, error) {
	var err error
	// One free port on the host loopback; with host networking the app must bind
	// this PORT (same idea as publishing a random host port to the container).
	if hostPort == 0 {
		hostPort, err = pickFreeTCPPort()
		if err != nil {
			return "", fmt.Errorf("allocate local port: %w", err)
		}
	}
	rawAddr := fmt.Sprintf("127.0.0.1:%d", hostPort)
	listenAddr, err := applyAdvertisedHost(rawAddr, r.advertiseHost)
	if err != nil {
		return "", err
	}
	containerID := fmt.Sprintf("kindling-%s", inst.ID)
	runCtx, cancel := context.WithCancel(ctx)

	args := []string{"run", "--bundle", bundleDir, containerID}
	cmd := exec.CommandContext(runCtx, "crun", args...)
	env := make([]string, 0, len(inst.Env)+1)
	for _, e := range inst.Env {
		if strings.HasPrefix(e, "PORT=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, fmt.Sprintf("PORT=%d", hostPort))
	cmd.Env = env

	ci := &crunInstance{
		id:        inst.ID,
		inst:      inst,
		cmd:       cmd,
		ip:        listenAddr,
		hostPort:  hostPort,
		bundleDir: bundleDir,
		ociDir:    ociDir,
		cancel:    cancel,
		stopped:   make(chan struct{}),
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		cancel()
		os.RemoveAll(bundleDir)
		os.RemoveAll(ociDir)
		return "", fmt.Errorf("crun run: %w", err)
	}

	go r.captureOutput(ci, stdout)
	go r.captureOutput(ci, stderr)

	r.mu.Lock()
	r.instances[inst.ID] = ci
	r.mu.Unlock()

	go func() {
		cmd.Wait()
		r.mu.Lock()
		delete(r.instances, inst.ID)
		retain := ci.retain
		r.mu.Unlock()
		if !retain {
			if bundleDir != "" {
				os.RemoveAll(bundleDir)
			}
			if ociDir != "" {
				os.RemoveAll(ociDir)
			}
		}
		slog.Info("container exited", "id", inst.ID, "runtime", "crun")
		close(ci.stopped)
	}()

	slog.Info("container started",
		"id", inst.ID,
		"image", inst.ImageRef,
		"runtime", "crun",
		"localhost", listenAddr,
	)

	return ci.ip, nil
}

func (r *CrunRuntime) Suspend(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ci, ok := r.instances[id]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	ci.retain = true
	r.suspended[id] = &crunSuspended{
		inst:      ci.inst,
		bundleDir: ci.bundleDir,
		ociDir:    ci.ociDir,
		hostPort:  ci.hostPort,
		ip:        ci.ip,
	}
	r.mu.Unlock()
	if err := r.Stop(ctx, id); err != nil {
		return err
	}
	select {
	case <-ci.stopped:
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		s := r.suspended[id]
		delete(r.suspended, id)
		ci.retain = false
		r.mu.Unlock()
		if s != nil {
			_ = os.RemoveAll(s.bundleDir)
			if s.ociDir != "" {
				_ = os.RemoveAll(s.ociDir)
			}
		}
		return ctx.Err()
	}
}

func (r *CrunRuntime) Resume(ctx context.Context, id uuid.UUID) (string, error) {
	r.mu.Lock()
	s, ok := r.suspended[id]
	if ok {
		delete(r.suspended, id)
	}
	r.mu.Unlock()
	if !ok {
		return "", ErrInstanceNotRunning
	}
	return r.startPreparedCrun(ctx, s.inst, s.bundleDir, s.ociDir, s.hostPort)
}

func (r *CrunRuntime) CreateTemplate(ctx context.Context, id uuid.UUID) (string, error) {
	_ = ctx
	r.mu.Lock()
	var bundleDir string
	if s, ok := r.suspended[id]; ok {
		bundleDir = s.bundleDir
	} else if ci, ok := r.instances[id]; ok {
		bundleDir = ci.bundleDir
	}
	templatePath := lifecyclePath("crun-template", id.String())
	r.mu.Unlock()
	if strings.TrimSpace(bundleDir) == "" {
		return "", ErrInstanceNotRunning
	}
	_ = os.RemoveAll(templatePath)
	if err := copyDir(bundleDir, templatePath); err != nil {
		return "", err
	}
	r.mu.Lock()
	r.templates[templatePath] = &crunTemplate{path: templatePath}
	r.mu.Unlock()
	return templatePath, nil
}

func (r *CrunRuntime) StartClone(ctx context.Context, inst Instance, snapshotRef string, cloneSourceVMID uuid.UUID) (string, StartMetadata, error) {
	if inst.PersistentVolume != nil {
		return "", StartMetadata{}, ErrPersistentVolumesUnsupported
	}
	r.mu.Lock()
	template, ok := r.templates[snapshotRef]
	r.mu.Unlock()
	if !ok {
		return "", StartMetadata{}, ErrInstanceNotRunning
	}
	bundleDir := fmt.Sprintf("/tmp/kindling-bundle-%s", inst.ID)
	_ = os.RemoveAll(bundleDir)
	if err := copyDir(template.path, bundleDir); err != nil {
		return "", StartMetadata{}, err
	}
	ip, err := r.startPreparedCrun(ctx, inst, bundleDir, "", 0)
	if err != nil {
		return "", StartMetadata{}, err
	}
	return ip, StartMetadata{SnapshotRef: snapshotRef, CloneSourceVMID: cloneSourceVMID}, nil
}

func pickFreeTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (r *CrunRuntime) Stop(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ci, ok := r.instances[id]
	if !ok {
		if s, suspended := r.suspended[id]; suspended {
			delete(r.suspended, id)
			r.mu.Unlock()
			if s.bundleDir != "" {
				_ = os.RemoveAll(s.bundleDir)
			}
			if s.ociDir != "" {
				_ = os.RemoveAll(s.ociDir)
			}
			return nil
		}
	}
	r.mu.Unlock()

	if !ok {
		containerID := fmt.Sprintf("kindling-%s", id)
		cmd := exec.CommandContext(ctx, "crun", "delete", "-f", containerID)
		_ = cmd.Run()
		_ = os.RemoveAll(fmt.Sprintf("/tmp/kindling-bundle-%s", id))
		_ = os.RemoveAll(fmt.Sprintf("/tmp/kindling-oci-%s", id))
		return nil
	}

	ci.cancel()
	return nil
}

func (r *CrunRuntime) Healthy(ctx context.Context, id uuid.UUID) bool {
	r.mu.Lock()
	ci, ok := r.instances[id]
	r.mu.Unlock()

	if !ok {
		return false
	}

	return ci.cmd.ProcessState == nil // still running
}

func (r *CrunRuntime) Logs(ctx context.Context, id uuid.UUID) ([]string, error) {
	r.mu.Lock()
	ci, ok := r.instances[id]
	r.mu.Unlock()

	if !ok {
		return nil, nil
	}

	ci.logMu.Lock()
	defer ci.logMu.Unlock()
	out := make([]string, len(ci.logs))
	copy(out, ci.logs)
	return out, nil
}

func (r *CrunRuntime) captureOutput(ci *crunInstance, rd interface{ Read([]byte) (int, error) }) {
	scanner := bufio.NewScanner(rd)
	for scanner.Scan() {
		line := scanner.Text()
		ci.logMu.Lock()
		ci.logs = append(ci.logs, line)
		// Keep last 1000 lines.
		if len(ci.logs) > 1000 {
			ci.logs = ci.logs[len(ci.logs)-1000:]
		}
		ci.logMu.Unlock()
	}
}

// applyAdvertisedHost rewrites 0.0.0.0 / loopback to advertiseHost when set
// (bare-metal / cloud: browsers reach the public IP, not 127.0.0.1).
func applyAdvertisedHost(hostPort, advertiseHost string) (string, error) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return hostPort, fmt.Errorf("split host/port %q: %w", hostPort, err)
	}
	adv := strings.TrimSpace(advertiseHost)
	if adv == "" {
		return hostPort, nil
	}
	if host == "0.0.0.0" || host == "127.0.0.1" || host == "localhost" {
		return net.JoinHostPort(adv, port), nil
	}
	if host == "::" || host == "[::]" {
		return net.JoinHostPort(adv, port), nil
	}
	return hostPort, nil
}

// StopAll kills all running instances. Called during graceful shutdown.
func (r *CrunRuntime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, ci := range r.instances {
		slog.Info("stopping instance on shutdown", "id", id)
		ci.cancel()
	}
}

// ResourceStats implements Runtime (host /proc metrics for the crun child).
func (r *CrunRuntime) ResourceStats(ctx context.Context, id uuid.UUID) (ResourceStats, error) {
	_ = ctx
	r.mu.Lock()
	ci, ok := r.instances[id]
	r.mu.Unlock()
	if !ok || ci.cmd == nil || ci.cmd.Process == nil {
		return ResourceStats{}, ErrInstanceNotRunning
	}
	stats, err := LinuxPIDResourceStats(ci.cmd.Process.Pid)
	if err != nil {
		return ResourceStats{}, err
	}
	return stats, nil
}
