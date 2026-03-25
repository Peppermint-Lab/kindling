//go:build darwin

package runtime

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestAppleGuestConfigIncludesPortEnv(t *testing.T) {
	inst := Instance{
		ID:   uuid.New(),
		Port: 3000,
		Env:  []string{"FOO=bar"},
	}

	cfg := appleGuestConfig(inst)

	if cfg.Port != 3000 {
		t.Fatalf("expected port 3000, got %d", cfg.Port)
	}

	if !slices.Contains(cfg.Env, "FOO=bar") {
		t.Fatalf("expected original env to be preserved, got %v", cfg.Env)
	}

	if !slices.Contains(cfg.Env, "PORT=3000") {
		t.Fatalf("expected PORT env var to be injected, got %v", cfg.Env)
	}
}

func TestAppleGuestConfigPreservesExistingPortEnv(t *testing.T) {
	inst := Instance{
		ID:   uuid.New(),
		Port: 3000,
		Env:  []string{"PORT=8080"},
	}

	cfg := appleGuestConfig(inst)

	if !slices.Contains(cfg.Env, "PORT=8080") {
		t.Fatalf("expected existing PORT env var to be preserved, got %v", cfg.Env)
	}

	if slices.Contains(cfg.Env, "PORT=3000") {
		t.Fatalf("expected runtime not to override explicit PORT, got %v", cfg.Env)
	}
}
