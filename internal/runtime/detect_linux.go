//go:build linux

package runtime

import (
	"log/slog"
)

// NewDetectedRuntime returns the best runtime for this host using cfg (DB-backed settings).
// Set cfg.ForceRuntime to "crun" to force the OCI path even on KVM hosts.
func NewDetectedRuntime(cfg HostRuntimeConfig) Runtime {
	desc := DescribeHostRuntime(cfg)
	adv := cfg.AdvertiseHost
	auth := cfg.PullAuth
	chPaths := cfg.CloudHypervisor

	switch desc.Backend {
	case BackendCloudHypervisor:
		return NewCloudHypervisorRuntime(chPaths, adv, auth)
	case BackendAppleVZ:
		// Unreachable on Linux builds; fall through for safety.
		slog.Warn("unexpected apple-vz backend on linux build; using crun")
		return NewCrunRuntime(adv, auth)
	default:
		return NewCrunRuntime(adv, auth)
	}
}
