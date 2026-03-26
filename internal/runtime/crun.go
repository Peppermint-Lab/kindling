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
	mu        sync.Mutex
	instances map[uuid.UUID]*crunInstance
}

type crunInstance struct {
	cmd    *exec.Cmd
	ip     string
	logs   []string
	logMu  sync.Mutex
	cancel context.CancelFunc
}

// NewCrunRuntime creates a new crun-based runtime. crun must be installed.
func NewCrunRuntime() *CrunRuntime {
	if _, err := exec.LookPath("crun"); err != nil {
		slog.Error("crun is required for the OCI fallback runtime on Linux; install crun (e.g. apt install crun)",
			"err", err)
	}
	return &CrunRuntime{
		instances: make(map[uuid.UUID]*crunInstance),
	}
}

func (r *CrunRuntime) Name() string { return "crun" }

func (r *CrunRuntime) Start(ctx context.Context, inst Instance) (string, error) {
	if _, err := exec.LookPath("crun"); err != nil {
		return "", fmt.Errorf("crun not found in PATH: %w (install crun for Linux deployments without KVM)", err)
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

	if err := oci.PullToOCILayout(ctx, imageRef, ociDir, oci.AuthFromEnv()); err != nil {
		return "", err
	}

	if err := oci.UmociUnpack(ctx, ociDir+":latest", bundleDir); err != nil {
		return "", err
	}

	if err := patchBundleHostNetwork(bundleDir); err != nil {
		return "", fmt.Errorf("host-network oci patch: %w", err)
	}

	// One free port on the host loopback; with host networking the app must bind
	// this PORT (same idea as publishing a random host port to the container).
	hostPort, err := pickFreeTCPPort()
	if err != nil {
		return "", fmt.Errorf("allocate local port: %w", err)
	}
	rawAddr := fmt.Sprintf("127.0.0.1:%d", hostPort)
	listenAddr, err := applyAdvertisedHost(rawAddr)
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
		cmd:    cmd,
		ip:     listenAddr,
		cancel: cancel,
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
		r.mu.Unlock()
		os.RemoveAll(bundleDir)
		os.RemoveAll(ociDir)
		slog.Info("container exited", "id", inst.ID, "runtime", "crun")
	}()

	slog.Info("container started",
		"id", inst.ID,
		"image", inst.ImageRef,
		"runtime", "crun",
		"localhost", listenAddr,
	)

	return ci.ip, nil
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
	r.mu.Unlock()

	if !ok {
		return nil // already stopped
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

// applyAdvertisedHost rewrites 0.0.0.0 / loopback to KINDLING_RUNTIME_ADVERTISE_HOST when set
// (bare-metal / cloud: browsers reach the public IP, not 127.0.0.1).
func applyAdvertisedHost(hostPort string) (string, error) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return hostPort, fmt.Errorf("split host/port %q: %w", hostPort, err)
	}
	adv := strings.TrimSpace(os.Getenv("KINDLING_RUNTIME_ADVERTISE_HOST"))
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
