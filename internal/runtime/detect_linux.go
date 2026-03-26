//go:build linux

package runtime

import (
	"log/slog"
	"os"
)

// NewDetectedRuntime returns the best runtime for this host.
// On Linux with KVM, we still use crun/docker today: Cloud Hypervisor is managed
// via internal/vmm and not yet wired to this Runtime interface.
// Native crun uses host networking (see patchBundleHostNetwork) and an ephemeral
// loopback port so reachable.runtime_url matches curl from the host like docker -p / Apple VZ.
func NewDetectedRuntime() Runtime {
	if _, err := os.Stat("/dev/kvm"); err == nil {
		slog.Info("KVM available; deploy runtime still uses crun/docker until cloud-hypervisor implements Runtime")
	}
	return NewCrunRuntime()
}
