//go:build linux

package runtime

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// NewDetectedRuntime returns the best runtime for this host.
// On Linux with KVM and Cloud Hypervisor installed, prefer a microVM runtime.
// Otherwise uses crun with skopeo/umoci (no Docker).
//
// Set KINDLING_RUNTIME=crun to force the crun path even on KVM hosts — useful when
// microVM networking or guest images need more tuning.
func NewDetectedRuntime() Runtime {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KINDLING_RUNTIME"))) {
	case "crun", "oci", "docker":
		slog.Info("KINDLING_RUNTIME override, using crun", "kindling_runtime", os.Getenv("KINDLING_RUNTIME"))
		return NewCrunRuntime()
	}
	if _, err := os.Stat("/dev/kvm"); err == nil {
		if _, err := exec.LookPath("cloud-hypervisor"); err == nil {
			slog.Info("KVM + cloud-hypervisor detected, using cloud-hypervisor runtime")
			return NewCloudHypervisorRuntime()
		}
		slog.Info("KVM available but cloud-hypervisor missing, falling back to crun")
	}
	return NewCrunRuntime()
}
