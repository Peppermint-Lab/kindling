//go:build !darwin

package builder

import (
	"context"
	"fmt"
)

// AppleVZBuildRunnerConfig mirrors the darwin-only config so callers can
// compile on non-macOS platforms even though the implementation is unavailable.
type AppleVZBuildRunnerConfig struct {
	KernelPath       string
	InitramfsPath    string
	BuilderRootfsDir string
}

// AppleVZBuildRunner is unavailable on non-darwin platforms.
type AppleVZBuildRunner struct{}

func NewAppleVZBuildRunner(cfg AppleVZBuildRunnerConfig) (*AppleVZBuildRunner, error) {
	_ = cfg
	return nil, fmt.Errorf("Apple VZ builder runner is only supported on macOS")
}

func (*AppleVZBuildRunner) BuildAndPush(ctx context.Context, run BuildRun) error {
	_ = ctx
	_ = run
	return fmt.Errorf("Apple VZ builder runner is only supported on macOS")
}
