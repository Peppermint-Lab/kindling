package runtime

import "strings"

// RemoteVMProductCapability names a user-visible remote VM feature (Milestone 2 contract).
// Keys are stable for JSON API and CLI output.
type RemoteVMProductCapability string

const (
	RemoteVMCapBrowserApp    RemoteVMProductCapability = "browser_app"
	RemoteVMCapTerminalShell RemoteVMProductCapability = "terminal_shell"
	RemoteVMCapSSHTCP        RemoteVMProductCapability = "ssh_tcp"
	RemoteVMCapExecCopy      RemoteVMProductCapability = "exec_copy"
	RemoteVMCapSuspendResume RemoteVMProductCapability = "suspend_resume"
	RemoteVMCapTemplateClone RemoteVMProductCapability = "template_clone"
	RemoteVMCapLiveMigration RemoteVMProductCapability = "live_migration"
)

// RemoteVMCapabilityEntry is one capability with supported vs currently available flags.
type RemoteVMCapabilityEntry struct {
	Supported bool `json:"supported"`
	Available bool `json:"available"`
}

// RemoteVMCapabilityMap is the API/CLI shape for a single remote VM.
type RemoteVMCapabilityMap map[string]RemoteVMCapabilityEntry

var allProductCaps = []RemoteVMProductCapability{
	RemoteVMCapBrowserApp,
	RemoteVMCapTerminalShell,
	RemoteVMCapSSHTCP,
	RemoteVMCapExecCopy,
	RemoteVMCapSuspendResume,
	RemoteVMCapTemplateClone,
	RemoteVMCapLiveMigration,
}

// supportedMaskForBackend returns which capabilities the backend implements today.
func supportedMaskForBackend(backend string) map[RemoteVMProductCapability]bool {
	b := strings.TrimSpace(backend)
	out := make(map[RemoteVMProductCapability]bool)
	switch b {
	case BackendCloudHypervisor, BackendAppleVZ:
		for _, c := range allProductCaps {
			out[c] = true
		}
		out[RemoteVMCapLiveMigration] = b == BackendCloudHypervisor
	case BackendCrun:
		out[RemoteVMCapBrowserApp] = true
		out[RemoteVMCapTerminalShell] = true
		out[RemoteVMCapSSHTCP] = true
		out[RemoteVMCapExecCopy] = true
		out[RemoteVMCapSuspendResume] = true
		out[RemoteVMCapTemplateClone] = true
		out[RemoteVMCapLiveMigration] = false
	default:
		for _, c := range allProductCaps {
			out[c] = false
		}
	}
	return out
}

// RemoteVMCapabilitiesForVM builds supported/available flags from persisted backend and observed state.
func RemoteVMCapabilitiesForVM(backend, observedState string) RemoteVMCapabilityMap {
	sup := supportedMaskForBackend(backend)
	running := strings.EqualFold(strings.TrimSpace(observedState), "running")
	m := make(RemoteVMCapabilityMap)
	for _, c := range allProductCaps {
		s := sup[c]
		// "Available" means usable right now given observed state (typically requires a running guest).
		avail := s && running
		m[string(c)] = RemoteVMCapabilityEntry{Supported: s, Available: avail}
	}
	return m
}

// RemoteVMCapabilitiesForTemplate records supported features for a template row (no “available” until a VM runs).
func RemoteVMCapabilitiesForTemplate(backend string) RemoteVMCapabilityMap {
	sup := supportedMaskForBackend(backend)
	m := make(RemoteVMCapabilityMap)
	for _, c := range allProductCaps {
		s := sup[c]
		m[string(c)] = RemoteVMCapabilityEntry{Supported: s, Available: false}
	}
	return m
}

// HostSupportedCapabilities returns capability keys supported on this worker backend (values true/false).
func HostSupportedCapabilities(backend string) map[string]bool {
	sup := supportedMaskForBackend(backend)
	out := make(map[string]bool, len(sup))
	for k, v := range sup {
		out[string(k)] = v
	}
	return out
}

// HostRuntimeDescriptorSupportedCaps returns supported flags for worker heartbeat metadata.
func HostRuntimeDescriptorSupportedCaps(d HostRuntimeDescriptor) map[string]bool {
	return HostSupportedCapabilities(d.Backend)
}
