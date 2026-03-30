package sandbox

import (
	"net/netip"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	kruntime "github.com/kindlingvm/kindling/internal/runtime"
)

func TestEffectiveLinuxRemoteVmPlacementLegacyBackendOnly(t *testing.T) {
	t.Parallel()
	meta := workerMetadata{RemoteVmBackend: kruntime.BackendCloudHypervisor}
	if !meta.effectiveLinuxRemoteVmPlacement() {
		t.Fatal("legacy CH backend should imply linux placement")
	}
	f := false
	meta2 := workerMetadata{RemoteVmBackend: kruntime.BackendCrun, LinuxPlacementEligible: &f}
	if meta2.effectiveLinuxRemoteVmPlacement() {
		t.Fatal("explicit false placement should win")
	}
	tr := true
	meta3 := workerMetadata{RemoteVmBackend: kruntime.BackendCrun, LinuxPlacementEligible: &tr}
	if !meta3.effectiveLinuxRemoteVmPlacement() {
		t.Fatal("explicit true placement for crun worker (future M3) should be honored")
	}
}

func TestDecodeWorkerMetadataFallsBackToRuntime(t *testing.T) {
	t.Parallel()

	meta := decodeWorkerMetadata([]byte(`{"runtime":"apple-vz","remote_vm_enabled":true,"remote_vm_rosetta":false}`))
	if meta.RemoteVmBackend != "apple-vz" {
		t.Fatalf("remote vm backend = %q, want apple-vz", meta.RemoteVmBackend)
	}
	if !meta.RemoteVmEnabled {
		t.Fatal("remote_vm_enabled = false, want true")
	}
}

func TestSandboxEnvIncludesUserAndGitMetadata(t *testing.T) {
	t.Parallel()

	env := sandboxEnv(queries.RemoteVm{
		EnvJson: []byte(`{"FOO":"bar","BAZ":"qux"}`),
		GitRepo: "https://github.com/kindlingvm/kindling",
		GitRef:  "main",
	})
	got := map[string]bool{}
	for _, item := range env {
		got[item] = true
	}
	for _, want := range []string{
		"FOO=bar",
		"BAZ=qux",
		"KINDLING_REMOTE_VM_GIT_REPO=https://github.com/kindlingvm/kindling",
		"KINDLING_REMOTE_VM_GIT_REF=main",
	} {
		if !got[want] {
			t.Fatalf("missing env entry %q in %#v", want, env)
		}
	}
}

func TestSplitImageRef(t *testing.T) {
	t.Parallel()

	registry, repository, tag := splitImageRef("ghcr.io/acme/devbox:v2")
	if registry != "ghcr.io" || repository != "acme/devbox" || tag != "v2" {
		t.Fatalf("unexpected split: %q %q %q", registry, repository, tag)
	}
	registry, repository, tag = splitImageRef("alpine")
	if registry != "docker.io" || repository != "library/alpine" || tag != "latest" {
		t.Fatalf("default split mismatch: %q %q %q", registry, repository, tag)
	}
}

func TestParseRuntimeAddress(t *testing.T) {
	t.Parallel()

	addr, port, err := parseRuntimeAddress("http://127.0.0.1:3000")
	if err != nil {
		t.Fatalf("parseRuntimeAddress: %v", err)
	}
	if addr != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("addr = %s", addr)
	}
	if port != 3000 {
		t.Fatalf("port = %d", port)
	}
}

func TestSandboxPortFallsBackToDefault(t *testing.T) {
	t.Parallel()

	if got := sandboxPort(queries.RemoteVm{}); got != DefaultPublishedHTTPPort {
		t.Fatalf("sandboxPort default = %d, want %d", got, DefaultPublishedHTTPPort)
	}
	if got := sandboxPort(queries.RemoteVm{
		PublishedHttpPort: pgtype.Int4{Int32: 8080, Valid: true},
	}); got != 8080 {
		t.Fatalf("sandboxPort explicit = %d, want 8080", got)
	}
}
