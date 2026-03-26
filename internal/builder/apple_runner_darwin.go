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
		return nil, fmt.Errorf("builder rootfs directory %q missing — unpack a Linux rootfs with buildah here (see CLAUDE.md)", cfg.BuilderRootfsDir)
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
		return err
	}
	if err := replaceWorkspaceFromSource(run.BuildDir, ws); err != nil {
		return fmt.Errorf("sync workspace into builder VM mount: %w", err)
	}

	r.mu.Lock()
	if r.vm == nil {
		vm, err := newAppleBuilderVM(r.cfg.KernelPath, r.cfg.InitramfsPath, r.cfg.BuilderRootfsDir, ws)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		if err := vm.start(ctx); err != nil {
			vm.Close()
			r.mu.Unlock()
			return err
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
	code, err := vm.Exec(ctx, bud, env, run.LogLine)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("buildah bud exited with code %d", code)
	}
	if run.LogLine != nil {
		run.LogLine("OCI build completed")
		run.LogLine("Pushing image to registry...")
	}
	creds := run.RegistryUsername + ":" + run.RegistryPassword
	push := append([]string{"buildah"}, oci.BuildahPushArgs(run.ImageRef, creds)...)
	code, err = vm.Exec(ctx, push, env, run.LogLine)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("buildah push exited with code %d", code)
	}
	if run.LogLine != nil {
		run.LogLine("Image pushed successfully")
	}
	return nil
}
