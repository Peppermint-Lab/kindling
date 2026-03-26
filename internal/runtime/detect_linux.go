//go:build linux

package runtime

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// NewDetectedRuntime returns the best runtime for this host using cfg (DB-backed settings).
// Set cfg.ForceRuntime to "crun" to force the OCI path even on KVM hosts.
func NewDetectedRuntime(cfg HostRuntimeConfig) Runtime {
	adv := cfg.AdvertiseHost
	auth := cfg.PullAuth
	chPaths := cfg.CloudHypervisor

	switch strings.ToLower(strings.TrimSpace(cfg.ForceRuntime)) {
	case "crun", "oci", "docker":
		slog.Info("runtime override, using crun", "force_runtime", cfg.ForceRuntime)
		return NewCrunRuntime(adv, auth)
	}
	if _, err := os.Stat("/dev/kvm"); err == nil {
		if _, err := exec.LookPath("cloud-hypervisor"); err == nil {
			slog.Info("KVM + cloud-hypervisor detected, using cloud-hypervisor runtime")
			return NewCloudHypervisorRuntime(chPaths, adv, auth)
		}
		slog.Info("KVM available but cloud-hypervisor missing, falling back to crun")
	}
	return NewCrunRuntime(adv, auth)
}
