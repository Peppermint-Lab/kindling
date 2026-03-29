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

func TestNormalizeSandboxBaseImageRef(t *testing.T) {
	t.Parallel()

	if got, err := normalizeSandboxBaseImageRef(" docker.io/library/ubuntu:24.04 "); err != nil || got != "docker.io/library/ubuntu:24.04" {
		t.Fatalf("normalizeSandboxBaseImageRef = (%q, %v)", got, err)
	}
	if _, err := normalizeSandboxBaseImageRef("   "); err == nil {
		t.Fatal("expected empty image ref to fail")
	}
}
