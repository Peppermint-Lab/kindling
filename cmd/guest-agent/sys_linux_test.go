//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestSplitBlockDevicePartition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		device     string
		wantParent string
		wantPart   string
		wantOK     bool
	}{
		{name: "virtio disk partition", device: "/dev/vdb1", wantParent: "/dev/vdb", wantPart: "1", wantOK: true},
		{name: "nvme partition", device: "/dev/nvme0n1p2", wantParent: "/dev/nvme0n1", wantPart: "2", wantOK: true},
		{name: "whole disk", device: "/dev/vdb", wantOK: false},
		{name: "blank", device: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parent, part, ok := splitBlockDevicePartition(tt.device)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if parent != tt.wantParent {
				t.Fatalf("parent = %q, want %q", parent, tt.wantParent)
			}
			if part != tt.wantPart {
				t.Fatalf("partition = %q, want %q", part, tt.wantPart)
			}
		})
	}
}

func TestGrowPersistentVolumeFilesystemUsesGrowpartForPartitions(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	growpartLog := filepath.Join(tmp, "growpart.log")
	resizeLog := filepath.Join(tmp, "resize2fs.log")
	writeExecutable(t, filepath.Join(tmp, "growpart"), "#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$2\" > \""+growpartLog+"\"\n")
	writeExecutable(t, filepath.Join(tmp, "resize2fs"), "#!/bin/sh\nprintf '%s\\n' \"$1\" > \""+resizeLog+"\"\n")

	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := growPersistentVolumeFilesystem("/dev/vdb1"); err != nil {
		t.Fatalf("growPersistentVolumeFilesystem: %v", err)
	}

	if got := strings.TrimSpace(readFile(t, growpartLog)); got != "/dev/vdb 1" {
		t.Fatalf("growpart args = %q, want %q", got, "/dev/vdb 1")
	}
	if got := strings.TrimSpace(readFile(t, resizeLog)); got != "/dev/vdb1" {
		t.Fatalf("resize2fs args = %q, want %q", got, "/dev/vdb1")
	}
}

func TestGrowPersistentVolumeFilesystemSkipsGrowpartForWholeDisk(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	growpartLog := filepath.Join(tmp, "growpart.log")
	resizeLog := filepath.Join(tmp, "resize2fs.log")
	writeExecutable(t, filepath.Join(tmp, "growpart"), "#!/bin/sh\nprintf 'unexpected\\n' > \""+growpartLog+"\"\n")
	writeExecutable(t, filepath.Join(tmp, "resize2fs"), "#!/bin/sh\nprintf '%s\\n' \"$1\" > \""+resizeLog+"\"\n")

	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := growPersistentVolumeFilesystem("/dev/vdb"); err != nil {
		t.Fatalf("growPersistentVolumeFilesystem: %v", err)
	}

	if _, err := os.Stat(growpartLog); !os.IsNotExist(err) {
		t.Fatalf("growpart should not be called, stat err = %v", err)
	}
	if got := strings.TrimSpace(readFile(t, resizeLog)); got != "/dev/vdb" {
		t.Fatalf("resize2fs args = %q, want %q", got, "/dev/vdb")
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
