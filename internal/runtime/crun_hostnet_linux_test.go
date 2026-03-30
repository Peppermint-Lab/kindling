//go:build linux

package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureBundleCrunIsolationAddsNetworkAndPIDNS(t *testing.T) {
	dir := t.TempDir()
	spec := map[string]any{
		"linux": map[string]any{
			"namespaces": []any{
				map[string]any{"type": "mount"},
				map[string]any{"type": "uts"},
			},
		},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureBundleCrunIsolation(dir); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	linux := got["linux"].(map[string]any)
	ns := linux["namespaces"].([]any)
	if len(ns) != 4 {
		t.Fatalf("namespaces len = %d want 4", len(ns))
	}
	var hasNet, hasPID bool
	for _, e := range ns {
		switch e.(map[string]any)["type"].(string) {
		case "network":
			hasNet = true
		case "pid":
			hasPID = true
		}
	}
	if !hasNet || !hasPID {
		t.Fatalf("missing network or pid ns: hasNet=%v hasPID=%v", hasNet, hasPID)
	}
}

func TestEnsureBundleCrunIsolationKeepsExistingNetworkNS(t *testing.T) {
	dir := t.TempDir()
	spec := map[string]any{
		"linux": map[string]any{
			"namespaces": []any{
				map[string]any{"type": "network"},
				map[string]any{"type": "pid"},
			},
		},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureBundleCrunIsolation(dir); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	ns := got["linux"].(map[string]any)["namespaces"].([]any)
	if len(ns) != 2 {
		t.Fatalf("unexpected duplicate namespaces: len=%d", len(ns))
	}
}

func TestCrunSupportsWarmLifecycle(t *testing.T) {
	rt := &CrunRuntime{}
	if !rt.Supports(CapabilitySuspendResume) {
		t.Fatal("expected crun runtime to support suspend/resume fallback")
	}
	if !rt.Supports(CapabilityWarmClone) {
		t.Fatal("expected crun runtime to support warm clone fallback")
	}
}
