//go:build linux

package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ensureBundleCrunIsolation ensures the OCI bundle has network and pid namespaces.
func ensureBundleCrunIsolation(bundleDir string) error {
	p := filepath.Join(bundleDir, "config.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("read bundle config: %w", err)
	}

	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fmt.Errorf("decode oci spec: %w", err)
	}

	linux, ok := spec["linux"].(map[string]any)
	if !ok {
		linux = map[string]any{}
		spec["linux"] = linux
	}
	nsRaw, ok := linux["namespaces"].([]any)
	if !ok {
		nsRaw = []any{}
	}

	hasNet, hasPID := false, false
	for _, e := range nsRaw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		switch t {
		case "network":
			hasNet = true
		case "pid":
			hasPID = true
		}
	}
	if !hasNet {
		nsRaw = append(nsRaw, map[string]any{"type": "network"})
	}
	if !hasPID {
		nsRaw = append(nsRaw, map[string]any{"type": "pid"})
	}
	linux["namespaces"] = nsRaw

	newRaw, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode oci spec: %w", err)
	}
	if err := os.WriteFile(p, newRaw, 0o644); err != nil {
		return fmt.Errorf("write bundle config: %w", err)
	}
	return nil
}
