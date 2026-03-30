//go:build linux

package runtime

import "testing"

func TestDescribeHostRuntimeForceCrun(t *testing.T) {
	t.Parallel()
	d := DescribeHostRuntime(HostRuntimeConfig{ForceRuntime: "crun"})
	if d.Backend != BackendCrun {
		t.Fatalf("backend=%q", d.Backend)
	}
	if d.LinuxRemoteVmPlacementEligible {
		t.Fatal("linux remote vm placement should be false for crun")
	}
}

func TestDescribeHostRuntimeForceCloudHypervisor(t *testing.T) {
	t.Parallel()
	d := DescribeHostRuntime(HostRuntimeConfig{ForceRuntime: "cloud-hypervisor"})
	if d.Backend != BackendCloudHypervisor {
		t.Fatalf("backend=%q", d.Backend)
	}
	if !d.LinuxRemoteVmPlacementEligible {
		t.Fatal("linux remote vm placement should be true for forced CH")
	}
}
