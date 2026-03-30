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

	tpl := &queries.RemoteVmTemplate{HostGroup: sandbox.HostGroupMac}

	if got := resolveSandboxHostGroup("linux-remote-vm", tpl); got != sandbox.HostGroupLinux {
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

	if !sandboxDeleteCanBypassReconciler(queries.RemoteVm{}) {
		t.Fatal("expected sandbox without vm to bypass reconciler")
	}

	if sandboxDeleteCanBypassReconciler(queries.RemoteVm{
		VmID: pguuid.ToPgtype(uuidMustParse("11111111-1111-1111-1111-111111111111")),
	}) {
		t.Fatal("expected sandbox with vm to require reconciler cleanup")
	}
}

func TestSandboxToOutIncludesCapabilities(t *testing.T) {
	t.Parallel()
	sb := queries.RemoteVm{
		Backend:       "cloud-hypervisor",
		ObservedState: "running",
	}
	out := sandboxToOut(sb, nil)
	if out.Capabilities == nil {
		t.Fatal("expected capabilities")
	}
	ent := out.Capabilities["ssh_tcp"]
	if !ent.Supported || !ent.Available {
		t.Fatalf("ssh_tcp = %#v", ent)
	}
}

func TestSandboxRuntimeObservabilityReady(t *testing.T) {
	t.Parallel()

	if !sandboxRuntimeObservabilityReady(queries.RemoteVm{ObservedState: "running"}) {
		t.Fatal("expected running sandbox to expose logs and stats")
	}

	if sandboxRuntimeObservabilityReady(queries.RemoteVm{ObservedState: "pending"}) {
		t.Fatal("expected pending sandbox to skip logs and stats")
	}

	if sandboxRuntimeObservabilityReady(queries.RemoteVm{ObservedState: "stopped"}) {
		t.Fatal("expected stopped sandbox to skip logs and stats")
	}
}

func TestValidateSandboxProxyHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		host    string
		want    string
		wantErr bool
	}{
		{name: "private ipv4", host: "10.50.0.5", want: "10.50.0.5"},
		{name: "ipv6", host: "fd00::1", want: "fd00::1"},
		{name: "empty", host: "", wantErr: true},
		{name: "loopback", host: "127.0.0.1", wantErr: true},
		{name: "ipv6 loopback with brackets", host: "[::1]", wantErr: true},
		{name: "unspecified", host: "0.0.0.0", wantErr: true},
		{name: "localhost", host: "localhost", wantErr: true},
		{name: "localhost trailing dot", host: "localhost.", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := validateSandboxProxyHost(tt.host)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateSandboxProxyHost(%q) expected error", tt.host)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateSandboxProxyHost(%q): %v", tt.host, err)
			}
			if got != tt.want {
				t.Fatalf("validateSandboxProxyHost(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

func uuidMustParse(raw string) uuid.UUID {
	id, err := uuid.Parse(raw)
	if err != nil {
		panic(err)
	}
	return id
}
