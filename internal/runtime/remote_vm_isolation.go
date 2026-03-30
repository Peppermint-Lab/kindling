package runtime

import (
	"fmt"
	"strings"
)

// Remote VM isolation policy (Milestone 3). Stored on remote_vms / remote_vm_templates.
const (
	RemoteVMIsolationBestAvailable  = "best_available"
	RemoteVMIsolationRequireMicrovm = "require_microvm"
)

// ParseRemoteVMIsolationPolicy validates API input.
func ParseRemoteVMIsolationPolicy(s string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "":
		return RemoteVMIsolationBestAvailable, nil
	case RemoteVMIsolationBestAvailable, RemoteVMIsolationRequireMicrovm:
		return v, nil
	default:
		return "", fmt.Errorf("invalid isolation_policy %q (allowed: %s, %s)", s, RemoteVMIsolationBestAvailable, RemoteVMIsolationRequireMicrovm)
	}
}

// NormalizeRemoteVMIsolationPolicy maps stored or legacy empty values to a canonical policy string.
func NormalizeRemoteVMIsolationPolicy(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case RemoteVMIsolationRequireMicrovm:
		return RemoteVMIsolationRequireMicrovm
	default:
		return RemoteVMIsolationBestAvailable
	}
}
