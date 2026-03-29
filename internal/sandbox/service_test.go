package sandbox

import (
	"net/netip"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestDecodeWorkerMetadataFallsBackToRuntime(t *testing.T) {
	t.Parallel()

	meta := decodeWorkerMetadata([]byte(`{"runtime":"apple-vz","sandbox_enabled":true,"sandbox_rosetta":false}`))
	if meta.SandboxBackend != "apple-vz" {
		t.Fatalf("sandbox backend = %q, want apple-vz", meta.SandboxBackend)
	}
	if !meta.SandboxEnabled {
		t.Fatal("sandbox_enabled = false, want true")
	}
}

func TestSandboxEnvIncludesUserAndGitMetadata(t *testing.T) {
	t.Parallel()

	env := sandboxEnv(queries.Sandbox{
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
		"KINDLING_SANDBOX_GIT_REPO=https://github.com/kindlingvm/kindling",
		"KINDLING_SANDBOX_GIT_REF=main",
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

	if got := sandboxPort(queries.Sandbox{}); got != DefaultPublishedHTTPPort {
		t.Fatalf("sandboxPort default = %d, want %d", got, DefaultPublishedHTTPPort)
	}
	if got := sandboxPort(queries.Sandbox{
		PublishedHttpPort: pgtype.Int4{Int32: 8080, Valid: true},
	}); got != 8080 {
		t.Fatalf("sandboxPort explicit = %d, want 8080", got)
	}
}
