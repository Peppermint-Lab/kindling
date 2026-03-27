//go:build linux

package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPatchBundleHostNetworkRemovesNetworkNS(t *testing.T) {
	dir := t.TempDir()
	spec := map[string]any{
		"linux": map[string]any{
			"namespaces": []any{
				map[string]any{"type": "mount"},
				map[string]any{"type": "network"},
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

	if err := patchBundleHostNetwork(dir); err != nil {
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
	if len(ns) != 2 {
		t.Fatalf("namespaces len = %d", len(ns))
	}
	for _, e := range ns {
		if e.(map[string]any)["type"].(string) == "network" {
			t.Fatal("network namespace should be removed")
		}
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
