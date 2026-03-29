package rpc

import "testing"

func TestNormalizeSandboxAutoSuspendSeconds(t *testing.T) {
	t.Parallel()

	if got, err := normalizeSandboxAutoSuspendSeconds(0); err != nil || got != 0 {
		t.Fatalf("normalize 0 = (%d, %v), want (0, nil)", got, err)
	}
	if got, err := normalizeSandboxAutoSuspendSeconds(600); err != nil || got != 600 {
		t.Fatalf("normalize 600 = (%d, %v), want (600, nil)", got, err)
	}
	if _, err := normalizeSandboxAutoSuspendSeconds(-1); err == nil {
		t.Fatal("expected negative value to fail")
	}
}
