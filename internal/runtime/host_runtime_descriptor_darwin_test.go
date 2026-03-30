//go:build darwin

package runtime

import "testing"

func TestDescribeHostRuntimeDarwinAppleVZ(t *testing.T) {
	t.Parallel()
	d := DescribeHostRuntime(HostRuntimeConfig{})
	if d.Backend != BackendAppleVZ {
		t.Fatalf("backend=%q", d.Backend)
	}
	if !d.MacRemoteVmPlacementEligible {
		t.Fatal("mac placement should be eligible on darwin")
	}
	if d.LinuxRemoteVmPlacementEligible {
		t.Fatal("linux placement should not be eligible on darwin")
	}
}
