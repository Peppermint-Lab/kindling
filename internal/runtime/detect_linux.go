//go:build linux

package runtime

import (
	"log/slog"
	"os"
)

// NewDetectedRuntime returns the best runtime for this host.
// On Linux: Cloud Hypervisor if KVM available, otherwise crun.
func NewDetectedRuntime() Runtime {
	if _, err := os.Stat("/dev/kvm"); err == nil {
		slog.Info("KVM detected, using cloud-hypervisor runtime")
		// TODO: return CloudHypervisorRuntime when it implements Runtime interface
		slog.Info("cloud-hypervisor runtime not yet migrated to Runtime interface, using crun")
	}
	return NewCrunRuntime()
}
