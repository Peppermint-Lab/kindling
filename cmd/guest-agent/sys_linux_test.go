//go:build linux

package main

import "testing"

func TestResolveAppRootPrefersVirtioFSApp(t *testing.T) {
	got := resolveAppRoot([]string{"/app", "/mnt/rootfs", "/mnt/rootfs/rootfs"}, map[string]bool{
		"/app/bin/sh":               true,
		"/mnt/rootfs/bin/sh":        true,
		"/mnt/rootfs/rootfs/bin/sh": true,
	})
	if got != "/app" {
		t.Fatalf("resolveAppRoot = %q, want /app", got)
	}
}

func TestResolveAppRootFallsBackToMountedDiskRoot(t *testing.T) {
	got := resolveAppRoot([]string{"/app", "/mnt/rootfs", "/mnt/rootfs/rootfs"}, map[string]bool{
		"/mnt/rootfs/bin/sh": true,
	})
	if got != "/mnt/rootfs" {
		t.Fatalf("resolveAppRoot = %q, want /mnt/rootfs", got)
	}
}

func TestResolveAppRootSupportsOCIBundleRootfs(t *testing.T) {
	got := resolveAppRoot([]string{"/app", "/mnt/rootfs", "/mnt/rootfs/rootfs"}, map[string]bool{
		"/mnt/rootfs/rootfs/bin/sh": true,
	})
	if got != "/mnt/rootfs/rootfs" {
		t.Fatalf("resolveAppRoot = %q, want /mnt/rootfs/rootfs", got)
	}
}
