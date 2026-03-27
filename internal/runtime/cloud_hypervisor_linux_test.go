//go:build linux

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCloudHypervisorIPsAllocatesPointToPointPairs(t *testing.T) {
	host0, guest0, err := cloudHypervisorIPs(0)
	if err != nil {
		t.Fatal(err)
	}
	if host0 != "10.0.0.0" || guest0 != "10.0.0.1/31" {
		t.Fatalf("slot 0 = (%q, %q), want (10.0.0.0, 10.0.0.1/31)", host0, guest0)
	}

	host1, guest1, err := cloudHypervisorIPs(1)
	if err != nil {
		t.Fatal(err)
	}
	if host1 != "10.0.0.2" || guest1 != "10.0.0.3/31" {
		t.Fatalf("slot 1 = (%q, %q), want (10.0.0.2, 10.0.0.3/31)", host1, guest1)
	}
}

func TestCloudHypervisorTapNameUsesDeploymentID(t *testing.T) {
	id1 := mustUUID("11111111-2222-3333-4444-555555555555")
	id2 := mustUUID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	got1 := cloudHypervisorTapName(id1, 0)
	got2 := cloudHypervisorTapName(id2, 0)
	if got1 == got2 {
		t.Fatalf("tap names should differ across deployments: %q", got1)
	}
	if len(got1) > 15 || len(got2) > 15 {
		t.Fatalf("tap names must fit Linux interface limit: %q %q", got1, got2)
	}
}

func TestCloudHypervisorSupportsWarmLifecycle(t *testing.T) {
	rt := &CloudHypervisorRuntime{}
	if !rt.Supports(CapabilitySuspendResume) {
		t.Fatal("expected cloud-hypervisor to support suspend/resume")
	}
	if !rt.Supports(CapabilityWarmClone) {
		t.Fatal("expected cloud-hypervisor to support warm clone")
	}
	if !rt.Supports(CapabilityLiveMigration) {
		t.Fatal("expected cloud-hypervisor to support live migration")
	}
}

func TestCloudHypervisorDiskArgsAddsPersistentVolumeAsSecondDisk(t *testing.T) {
	args := cloudHypervisorDiskArgs("/tmp/rootfs.qcow2", &PersistentVolumeMount{
		HostPath:  "/data/volumes/vol-1.qcow2",
		MountPath: "/data",
	})

	if len(args) != 4 {
		t.Fatalf("len(args) = %d, want 4", len(args))
	}
	if args[0] != "--disk" || args[2] != "--disk" {
		t.Fatalf("unexpected disk flags: %#v", args)
	}
	if args[1] != "path=/tmp/rootfs.qcow2,direct=off" {
		t.Fatalf("root disk arg = %q", args[1])
	}
	if args[3] != "path=/data/volumes/vol-1.qcow2,direct=off" {
		t.Fatalf("persistent volume arg = %q", args[3])
	}
}

func TestEnsurePersistentVolumeSizeResizesQcow2(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "qemu-img.log")
	writeRuntimeExecutable(t, filepath.Join(tmp, "qemu-img"), "#!/bin/sh\nif [ \"$1\" = \"info\" ]; then\n  printf '{\"virtual-size\":1073741824}'\n  exit 0\nfi\nprintf '%s %s %s\\n' \"$1\" \"$2\" \"$3\" > \""+logPath+"\"\n")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	volumePath := filepath.Join(tmp, "volume.qcow2")
	if err := os.WriteFile(volumePath, []byte("qcow2"), 0o644); err != nil {
		t.Fatalf("write volume: %v", err)
	}

	if err := ensurePersistentVolumeSize(context.Background(), volumePath, 2); err != nil {
		t.Fatalf("ensurePersistentVolumeSize: %v", err)
	}

	if got := strings.TrimSpace(readRuntimeFile(t, logPath)); got != "resize "+volumePath+" 2G" {
		t.Fatalf("resize args = %q", got)
	}
}

func TestSharedRootfsPathHelpers(t *testing.T) {
	id := mustUUID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	got := sharedRootfsPathForID("/shared/rootfs", id)
	want := "/shared/rootfs/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/rootfs.qcow2"
	if got != want {
		t.Fatalf("sharedRootfsPathForID = %q, want %q", got, want)
	}
	if ref := sharedRootfsRefFromWorkDisk("/shared/rootfs", got); ref != want {
		t.Fatalf("sharedRootfsRefFromWorkDisk = %q, want %q", ref, want)
	}
	if ref := sharedRootfsRefFromWorkDisk("/shared/rootfs", "/tmp/other/rootfs.qcow2"); ref != "" {
		t.Fatalf("sharedRootfsRefFromWorkDisk outside shared dir = %q, want empty", ref)
	}
}

func mustUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}

func writeRuntimeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readRuntimeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
