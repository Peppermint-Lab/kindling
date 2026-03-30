//go:build !linux && !darwin

package runtime

import "log/slog"

func hostRuntimeDescriptorForPlatform(cfg HostRuntimeConfig) HostRuntimeDescriptor {
	_ = cfg
	slog.Info("unsupported GOOS for Kindling worker; defaulting to crun backend label")
	return HostRuntimeDescriptor{
		Backend:                        BackendCrun,
		Arch:                           HostArch(),
		LinuxRemoteVmPlacementEligible: false,
		MacRemoteVmPlacementEligible:   false,
	}
}
