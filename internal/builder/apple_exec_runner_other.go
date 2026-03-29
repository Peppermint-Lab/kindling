//go:build !darwin

package builder

import (
	"context"
	"fmt"
)

type ExecRun struct {
	WorkspaceDir string
	Argv         []string
	Cwd          string
	Env          []string
	LogLine      func(string)
}

type AppleVZExecRunner struct{}

func NewAppleVZExecRunner(cfg AppleVZBuildRunnerConfig) (*AppleVZExecRunner, error) {
	_ = cfg
	return nil, fmt.Errorf("Apple VZ exec runner is only supported on macOS")
}

func (*AppleVZExecRunner) Exec(ctx context.Context, run ExecRun) (int, error) {
	_ = ctx
	_ = run
	return 0, fmt.Errorf("Apple VZ exec runner is only supported on macOS")
}
