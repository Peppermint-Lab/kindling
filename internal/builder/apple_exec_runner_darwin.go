//go:build darwin

package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type ExecRun struct {
	WorkspaceDir string
	Argv         []string
	Cwd          string
	Env          []string
	LogLine      func(string)
}

// AppleVZExecRunner runs generic commands inside a Kindling-managed Linux VM.
type AppleVZExecRunner struct {
	cfg AppleVZBuildRunnerConfig

	mu sync.Mutex
	vm *appleBuilderVM
}

func NewAppleVZExecRunner(cfg AppleVZBuildRunnerConfig) (*AppleVZExecRunner, error) {
	if cfg.KernelPath == "" || cfg.InitramfsPath == "" {
		return nil, fmt.Errorf("kernel and initramfs paths are required for the exec VM")
	}
	st, err := os.Stat(cfg.BuilderRootfsDir)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("builder rootfs directory %q missing", cfg.BuilderRootfsDir)
	}
	return &AppleVZExecRunner{cfg: cfg}, nil
}

func (r *AppleVZExecRunner) workspaceDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".kindling", "ci-workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (r *AppleVZExecRunner) Exec(ctx context.Context, run ExecRun) (int, error) {
	ws, err := r.workspaceDir()
	if err != nil {
		return 0, err
	}
	if err := replaceWorkspaceFromSource(run.WorkspaceDir, ws); err != nil {
		return 0, fmt.Errorf("sync workspace into exec VM mount: %w", err)
	}

	r.mu.Lock()
	if r.vm == nil {
		vm, err := newAppleBuilderVM(r.cfg.KernelPath, r.cfg.InitramfsPath, r.cfg.BuilderRootfsDir, ws, "ci")
		if err != nil {
			r.mu.Unlock()
			return 0, err
		}
		if err := vm.start(ctx); err != nil {
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
