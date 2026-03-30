package runtime

import stdruntime "runtime"

// BackendName constants for worker, API, and persisted remote VM rows.
const (
	BackendCloudHypervisor = "cloud-hypervisor"
	BackendCrun            = "crun"
	BackendAppleVZ         = "apple-vz"
)

// HostRuntimeDescriptor is the canonical outcome of host runtime selection from
// HostRuntimeConfig. All callers should use DescribeHostRuntime rather than
// duplicating KVM / cloud-hypervisor probe logic.
type HostRuntimeDescriptor struct {
	// Backend is the runtime implementation name (e.g. BackendCloudHypervisor).
	Backend string
	// Arch is the worker host architecture (GOARCH).
	Arch string
	// LinuxRemoteVmPlacementEligible is true when this worker may accept new
	// linux-remote-vm placements under current product rules (Cloud Hypervisor
	// microVMs and, on Milestone 3, crun fallback workers).
	LinuxRemoteVmPlacementEligible bool
	// MacRemoteVmPlacementEligible is true when this worker may accept mac-remote-vm placements.
	MacRemoteVmPlacementEligible bool
}

// DescribeHostRuntime returns the authoritative runtime choice for this process.
func DescribeHostRuntime(cfg HostRuntimeConfig) HostRuntimeDescriptor {
	return hostRuntimeDescriptorForPlatform(cfg)
}

// HostArch returns runtime.GOARCH for use in descriptors and metadata.
func HostArch() string {
	return stdruntime.GOARCH
}
