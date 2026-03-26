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
	"time"

	"github.com/google/uuid"
)

// CrunRuntime runs OCI containers via crun (or docker as fallback).
// No daemon required — each instance is a direct child process.
type CrunRuntime struct {
	mu        sync.Mutex
	instances map[uuid.UUID]*crunInstance
	useDocker bool // fallback to docker when crun isn't available
}

type crunInstance struct {
	cmd    *exec.Cmd
	ip     string
	logs   []string
	logMu  sync.Mutex
	cancel context.CancelFunc
}

// NewCrunRuntime creates a new crun runtime.
// Falls back to Docker if crun is not installed (e.g. macOS dev).
func NewCrunRuntime() *CrunRuntime {
	useDocker := false
	if _, err := exec.LookPath("crun"); err != nil {
		slog.Info("crun not found, falling back to docker for container runtime")
		useDocker = true
	}
	return &CrunRuntime{
		instances: make(map[uuid.UUID]*crunInstance),
		useDocker: useDocker,
	}
}

func (r *CrunRuntime) Name() string {
	if r.useDocker {
		return "docker"
	}
	return "crun"
}

func (r *CrunRuntime) Start(ctx context.Context, inst Instance) (string, error) {
	if r.useDocker {
		return r.startDocker(ctx, inst)
	}
	return r.startCrun(ctx, inst)
}

func (r *CrunRuntime) startDocker(ctx context.Context, inst Instance) (string, error) {
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
		"-p", fmt.Sprintf("0:%d", port), // random host port → container port
	}

	// Add env vars.
	for _, e := range inst.Env {
		args = append(args, "-e", e)
	}
	args = append(args, "-e", fmt.Sprintf("PORT=%d", port))

	args = append(args, inst.ImageRef)

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, "docker", args...)

	// Capture logs.
	ci := &crunInstance{
		cmd:    cmd,
		cancel: cancel,
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("docker run: %w", err)
	}

	// Stream logs in background.
	go r.captureOutput(ci, stdout)
	go r.captureOutput(ci, stderr)

	// docker port may fail immediately after container start; wait briefly.
	var ip string
	var err error
	for attempt := 0; attempt < 25; attempt++ {
		time.Sleep(120 * time.Millisecond)
		ip, err = r.getDockerIP(containerName, port)
		if err == nil {
			break
		}
	}
	if err != nil {
		ip = fmt.Sprintf("127.0.0.1:%d", port)
		slog.Warn("could not get docker port, using default", "error", err)
	}
	ip, err = applyAdvertisedHost(ip)
	if err != nil {
		return "", err
	}
	ci.ip = ip

	r.mu.Lock()
	r.instances[inst.ID] = ci
	r.mu.Unlock()

	// Monitor process.
	go func() {
		cmd.Wait()
		r.mu.Lock()
		delete(r.instances, inst.ID)
		r.mu.Unlock()
		slog.Info("container exited", "id", inst.ID, "runtime", "docker")
	}()

	slog.Info("container started",
		"id", inst.ID,
		"image", inst.ImageRef,
		"runtime", "docker",
		"ip", ip,
	)

	return ip, nil
}

func (r *CrunRuntime) startCrun(ctx context.Context, inst Instance) (string, error) {
	// Prepare OCI bundle from image.
	bundleDir := fmt.Sprintf("/tmp/kindling-bundle-%s", inst.ID)
	os.MkdirAll(bundleDir, 0o755)

	// Extract OCI image to bundle using umoci.
	imageRef := inst.ImageRef
	ociDir := fmt.Sprintf("/tmp/kindling-oci-%s", inst.ID)

	// Pull image with skopeo.
	pullCmd := exec.CommandContext(ctx, "skopeo", "copy",
		fmt.Sprintf("docker://%s", imageRef),
		fmt.Sprintf("oci:%s:latest", ociDir),
	)
	if out, err := pullCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("skopeo copy: %s: %w", string(out), err)
	}

	// Unpack to OCI bundle.
	unpackCmd := exec.CommandContext(ctx, "umoci", "unpack",
		"--image", ociDir+":latest", bundleDir,
	)
	if out, err := unpackCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("umoci unpack: %s: %w", string(out), err)
	}

	if err := patchBundleHostNetwork(bundleDir); err != nil {
		return "", fmt.Errorf("host-network oci patch: %w", err)
	}

	// One free port on the host loopback; with host networking the app must bind
	// this PORT (same idea as docker -p 0:containerPort).
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

	if r.useDocker {
		// docker stop is more graceful.
		exec.CommandContext(ctx, "docker", "stop", fmt.Sprintf("kindling-%s", id)).Run()
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

func (r *CrunRuntime) getDockerIP(containerName string, containerPort int) (string, error) {
	// Get the host port mapped to the container port.
	out, err := exec.Command("docker", "port", containerName, fmt.Sprintf("%d", containerPort)).Output()
	if err != nil {
		return "", err
	}
	// Output is like "0.0.0.0:32768" and/or "[::]:32768" — prefer IPv4 for SplitHostPort + browsers.
	line := strings.TrimSpace(string(out))
	for _, part := range strings.Split(line, "\n") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "0.0.0.0:") {
			return part, nil
		}
	}
	parts := strings.Split(line, "\n")
	if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
		return strings.TrimSpace(parts[0]), nil
	}
	return "", fmt.Errorf("no port mapping found")
}

// applyAdvertisedHost rewrites 0.0.0.0 / loopback to KINDLING_RUNTIME_ADVERTISE_HOST when set
// (bare-metal / cloud: browsers reach the public IP, not docker’s "0.0.0.0" or crun’s 127.0.0.1).
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
		if r.useDocker {
			exec.Command("docker", "stop", fmt.Sprintf("kindling-%s", id)).Run()
		}
		ci.cancel()
	}
}
