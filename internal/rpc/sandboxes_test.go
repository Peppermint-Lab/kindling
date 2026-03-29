package rpc

import (
	"testing"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/sandbox"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

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

func TestResolveSandboxHostGroup(t *testing.T) {
	t.Parallel()

	tpl := &queries.SandboxTemplate{HostGroup: sandbox.HostGroupMac}

	if got := resolveSandboxHostGroup("linux-sandbox", tpl); got != sandbox.HostGroupLinux {
		t.Fatalf("resolveSandboxHostGroup explicit = %q, want %q", got, sandbox.HostGroupLinux)
	}
	if got := resolveSandboxHostGroup("", tpl); got != sandbox.HostGroupMac {
		t.Fatalf("resolveSandboxHostGroup template = %q, want %q", got, sandbox.HostGroupMac)
	}
	if got := resolveSandboxHostGroup("", nil); got != sandbox.HostGroupLinux {
		t.Fatalf("resolveSandboxHostGroup default = %q, want %q", got, sandbox.HostGroupLinux)
	}
}

func TestSandboxDeleteCanBypassReconciler(t *testing.T) {
	t.Parallel()

	if !sandboxDeleteCanBypassReconciler(queries.Sandbox{}) {
		t.Fatal("expected sandbox without vm to bypass reconciler")
	}

	if sandboxDeleteCanBypassReconciler(queries.Sandbox{
		VmID: pguuid.ToPgtype(uuidMustParse("11111111-1111-1111-1111-111111111111")),
	}) {
		t.Fatal("expected sandbox with vm to require reconciler cleanup")
	}
}

func TestSandboxRuntimeObservabilityReady(t *testing.T) {
	t.Parallel()

	if !sandboxRuntimeObservabilityReady(queries.Sandbox{ObservedState: "running"}) {
		t.Fatal("expected running sandbox to expose logs and stats")
	}

	if sandboxRuntimeObservabilityReady(queries.Sandbox{ObservedState: "pending"}) {
		t.Fatal("expected pending sandbox to skip logs and stats")
	}

	if sandboxRuntimeObservabilityReady(queries.Sandbox{ObservedState: "stopped"}) {
		t.Fatal("expected stopped sandbox to skip logs and stats")
	}
}

func uuidMustParse(raw string) uuid.UUID {
	id, err := uuid.Parse(raw)
	if err != nil {
		panic(err)
	}
	return id
}
