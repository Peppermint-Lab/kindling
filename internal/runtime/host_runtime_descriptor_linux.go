//go:build linux

package runtime

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

func hostRuntimeDescriptorForPlatform(cfg HostRuntimeConfig) HostRuntimeDescriptor {
	arch := HostArch()
	switch strings.ToLower(strings.TrimSpace(cfg.ForceRuntime)) {
	case "crun", "oci", "docker":
		slog.Info("runtime override, using crun", "force_runtime", cfg.ForceRuntime)
		return HostRuntimeDescriptor{
			Backend:                        BackendCrun,
			Arch:                           arch,
			LinuxRemoteVmPlacementEligible: false,
			MacRemoteVmPlacementEligible:   false,
		}
	case BackendCloudHypervisor, "ch":
		slog.Info("runtime override, using cloud-hypervisor", "force_runtime", cfg.ForceRuntime)
		return HostRuntimeDescriptor{
			Backend:                        BackendCloudHypervisor,
			Arch:                           arch,
			LinuxRemoteVmPlacementEligible: true,
			MacRemoteVmPlacementEligible:   false,
		}
	}
	if _, err := os.Stat("/dev/kvm"); err == nil {
		if _, err := exec.LookPath("cloud-hypervisor"); err == nil {
			slog.Info("KVM + cloud-hypervisor detected, using cloud-hypervisor runtime")
			return HostRuntimeDescriptor{
				Backend:                        BackendCloudHypervisor,
				Arch:                           arch,
				LinuxRemoteVmPlacementEligible: true,
				MacRemoteVmPlacementEligible:   false,
			}
		}
		slog.Info("KVM available but cloud-hypervisor missing, falling back to crun")
	}
	return HostRuntimeDescriptor{
		Backend:                        BackendCrun,
		Arch:                           arch,
		LinuxRemoteVmPlacementEligible: false,
		MacRemoteVmPlacementEligible:   false,
	}
}
