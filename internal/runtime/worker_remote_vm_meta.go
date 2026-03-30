package runtime

import "strings"

// BuildWorkerRemoteVMMetadata assembles worker component JSON for remote VM placement and capabilities.
// Callers should merge the returned map into the heartbeat payload (e.g. under worker metadata).
func BuildWorkerRemoteVMMetadata(rt Runtime, cfg HostRuntimeConfig) map[string]any {
	desc := DescribeHostRuntime(cfg)
	meta := map[string]any{
		"runtime":                            desc.Backend,
		"remote_vm_backend":                  desc.Backend,
		"remote_vm_arch":                     desc.Arch,
		"remote_vm_linux_placement_eligible": desc.LinuxRemoteVmPlacementEligible,
		"remote_vm_mac_placement_eligible":   desc.MacRemoteVmPlacementEligible,
		"remote_vm_capabilities":             HostRuntimeDescriptorSupportedCaps(desc),
	}
	// Product continuity: remote_vm_enabled means this worker satisfies current linux or mac remote VM placement rules.
	meta["remote_vm_enabled"] = desc.LinuxRemoteVmPlacementEligible || desc.MacRemoteVmPlacementEligible

	meta["remote_vm_rosetta"] = false
	meta["remote_vm_capacity"] = 1

	if rt.Name() == BackendCloudHypervisor {
		meta["live_migration_enabled"] = rt.Supports(CapabilityLiveMigration)
		if chrt, ok := rt.(DurableRetainedStateRuntime); ok {
			if v := strings.TrimSpace(chrt.StateDir()); v != "" {
				meta["state_dir"] = v
			}
			meta["durable_fast_wake_enabled"] = chrt.DurableFastWakeEnabled()
		}
	}
	return meta
}
