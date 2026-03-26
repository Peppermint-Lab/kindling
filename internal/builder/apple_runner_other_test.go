//go:build !darwin

package builder

import (
	"context"
	"testing"
)

func TestAppleVZBuildRunnerUnavailableOnNonDarwin(t *testing.T) {
	r, err := NewAppleVZBuildRunner(AppleVZBuildRunnerConfig{})
	if err == nil {
		t.Fatal("expected non-darwin Apple builder runner construction to fail")
	}
	if r != nil {
		t.Fatal("expected no runner on non-darwin")
	}

	var nilRunner *AppleVZBuildRunner
	if nilRunner != nil {
		t.Fatal("expected zero value runner pointer to be nil")
	}

	if err := SmokeTestAppleBuilderVM(context.Background()); err == nil {
		t.Fatal("expected smoke test to be unavailable on non-darwin")
	}
}
