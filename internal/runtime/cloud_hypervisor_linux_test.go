//go:build linux

package runtime

import (
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

func mustUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}
