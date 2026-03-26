package oci_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kindlingvm/kindling/internal/oci"
)

// TestExportImageRootfs_dockerHubAlpine pulls a small public image and unpacks it.
// Requires network, skopeo, and umoci. Skips when tools are missing or -short.
func TestExportImageRootfs_dockerHubAlpine(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	for _, bin := range []string{"skopeo", "umoci"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH: %v", bin, err)
		}
	}

	ctx := context.Background()
	dest := t.TempDir()
	outDir := filepath.Join(dest, "rootfs")
	// Fully qualified ref avoids ambiguous short names in some skopeo versions.
	ref := "docker.io/library/alpine:3.19"
	if err := oci.ExportImageRootfs(ctx, ref, outDir, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(outDir, "etc", "alpine-release"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("alpine-release: %s", string(b))
}
