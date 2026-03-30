//go:build darwin

package runtime

import "log/slog"

func hostRuntimeDescriptorForPlatform(cfg HostRuntimeConfig) HostRuntimeDescriptor {
	// ForceRuntime is honored for crun/docker only on Linux; on macOS the worker
	// runtime is Apple VZ. Deployments may still use different paths.
	_ = cfg
	slog.Info("macOS detected, using Apple Virtualization Framework runtime")
	return HostRuntimeDescriptor{
		Backend:                        BackendAppleVZ,
		Arch:                           HostArch(),
		LinuxRemoteVmPlacementEligible: false,
		MacRemoteVmPlacementEligible:   true,
	}
}
