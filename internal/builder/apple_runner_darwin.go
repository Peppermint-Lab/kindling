//go:build darwin

package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/kindlingvm/kindling/internal/oci"
)

// AppleVZBuildRunnerConfig configures the persistent Linux builder microVM.
type AppleVZBuildRunnerConfig struct {
	KernelPath       string
	InitramfsPath    string
	BuilderRootfsDir string
}

// AppleVZBuildRunner runs buildah inside a Kindling-managed Apple VZ VM.
type AppleVZBuildRunner struct {
	cfg AppleVZBuildRunnerConfig

	mu sync.Mutex
	vm *appleBuilderVM
}

// NewAppleVZBuildRunner validates paths and returns a runner. The builder rootfs must
// contain a Linux tree with buildah at usr/bin/buildah or bin/buildah (e.g. under ~/.kindling/builder-rootfs).
func NewAppleVZBuildRunner(cfg AppleVZBuildRunnerConfig) (*AppleVZBuildRunner, error) {
	if cfg.KernelPath == "" || cfg.InitramfsPath == "" {
		return nil, fmt.Errorf("kernel and initramfs paths are required for the builder VM")
	}
	st, err := os.Stat(cfg.BuilderRootfsDir)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("builder rootfs directory %q missing — unpack a Linux rootfs with buildah here (see AGENTS.md)", cfg.BuilderRootfsDir)
	}
	buildahOK := false
	for _, p := range []string{"usr/bin/buildah", "bin/buildah", "sbin/buildah"} {
		if _, err := os.Stat(filepath.Join(cfg.BuilderRootfsDir, p)); err == nil {
			buildahOK = true
			break
		}
	}
	if !buildahOK {
		return nil, fmt.Errorf("no buildah binary found under %s", cfg.BuilderRootfsDir)
	}
	return &AppleVZBuildRunner{cfg: cfg}, nil
}

func (r *AppleVZBuildRunner) workspaceDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".kindling", "builder-workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// BuildAndPush implements BuildRunner. Registry credentials are required on macOS.
func (r *AppleVZBuildRunner) BuildAndPush(ctx context.Context, run BuildRun) error {
	if run.RegistryUsername == "" {
		return fmt.Errorf("macOS OCI builds require a registry user (set registry username/password in cluster settings)")
	}
	ws, err := r.workspaceDir()
	if err != nil {
		return fmt.Errorf("resolve workspace dir: %w", err)
	}
	if err := replaceWorkspaceFromSource(run.BuildDir, ws); err != nil {
		return fmt.Errorf("sync workspace into builder VM mount: %w", err)
	}

	r.mu.Lock()
	if r.vm == nil {
		vm, err := newAppleBuilderVM(r.cfg.KernelPath, r.cfg.InitramfsPath, r.cfg.BuilderRootfsDir, ws, "builder")
		if err != nil {
			r.mu.Unlock()
			return fmt.Errorf("create builder VM: %w", err)
		}
		if err := vm.start(ctx); err != nil {
			vm.Close()
			r.mu.Unlock()
			return fmt.Errorf("start builder VM: %w", err)
		}
		r.vm = vm
	}
	vm := r.vm
	r.mu.Unlock()

	if run.LogLine != nil {
		run.LogLine("Using Kindling builder microVM (Apple Virtualization) with guest-local layer cache")
	}

	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin:/sbin:/usr/sbin"}

	bud := append([]string{"buildah"}, oci.BuildahBudArgs(run.ImageRef, run.DockerfilePath, true)...)
	code, err := vm.Exec(ctx, bud, "/workspace", env, run.LogLine)
	if err != nil {
		return fmt.Errorf("exec buildah bud: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("buildah bud exited with code %d", code)
	}
	if run.LogLine != nil {
		run.LogLine("OCI build completed")
		run.LogLine("Pushing image to registry...")
	}
	// Write a temporary Docker-format authfile to the shared workspace so the guest
	// can read it via the VirtIO mount. The file is created with 0600 permissions
	// and removed after the push completes to avoid leaking credentials on disk.
	auth := &oci.Auth{Username: run.RegistryUsername, Password: run.RegistryPassword}
	authFilePath, err := oci.WriteAuthFile(run.ImageRef, auth)
	if err != nil {
		return fmt.Errorf("create auth file for push: %w", err)
	}
	defer os.Remove(authFilePath)

	// Copy the authfile into the workspace so the guest can access it via the VirtIO mount.
	guestAuthPath := "/workspace/.kindling-push-auth.json"
	hostAuthDest := filepath.Join(ws, ".kindling-push-auth.json")
	authData, err := os.ReadFile(authFilePath)
	if err != nil {
		return fmt.Errorf("read auth file: %w", err)
	}
	if err := os.WriteFile(hostAuthDest, authData, 0o600); err != nil {
		return fmt.Errorf("write auth file to workspace: %w", err)
	}
	defer os.Remove(hostAuthDest)

	push := append([]string{"buildah"}, oci.BuildahPushArgs(run.ImageRef, guestAuthPath)...)
	code, err = vm.Exec(ctx, push, "/workspace", env, run.LogLine)
	if err != nil {
		return fmt.Errorf("exec buildah push: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("buildah push exited with code %d", code)
	}
	if run.LogLine != nil {
		run.LogLine("Image pushed successfully")
	}
	return nil
}
