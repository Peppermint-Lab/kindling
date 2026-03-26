//go:build linux

package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// patchBundleHostNetwork removes the network namespace from the OCI config so crun
// joins the host network. Otherwise 127.0.0.1:<port> on the host would not reach
// the container's loopback (see Apple VZ / docker -p parity for raw runtime URL).
func patchBundleHostNetwork(bundleDir string) error {
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
		return nil
	}
	nsRaw, ok := linux["namespaces"].([]any)
	if !ok {
		return nil
	}

	var out []any
	for _, e := range nsRaw {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		t, _ := m["type"].(string)
		if t == "network" {
			continue
		}
		out = append(out, e)
	}
	linux["namespaces"] = out

	newRaw, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode oci spec: %w", err)
	}
	if err := os.WriteFile(p, newRaw, 0o644); err != nil {
		return fmt.Errorf("write bundle config: %w", err)
	}
	return nil
}
