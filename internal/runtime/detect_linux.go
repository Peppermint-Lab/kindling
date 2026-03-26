//go:build linux

package runtime

import (
	"log/slog"
	"os"
	"os/exec"
)

// NewDetectedRuntime returns the best runtime for this host.
// On Linux with KVM and Cloud Hypervisor installed, prefer a microVM runtime.
// Fall back to crun/docker when prerequisites are missing.
func NewDetectedRuntime() Runtime {
	if _, err := os.Stat("/dev/kvm"); err == nil {
		if _, err := exec.LookPath("cloud-hypervisor"); err == nil {
			slog.Info("KVM + cloud-hypervisor detected, using cloud-hypervisor runtime")
			return NewCloudHypervisorRuntime()
		}
		slog.Info("KVM available but cloud-hypervisor missing, falling back to crun/docker")
	}
	return NewCrunRuntime()
}
