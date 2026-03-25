//go:build darwin

package runtime

import "log/slog"

// NewDetectedRuntime returns the best runtime for this host.
// On macOS: Apple Virtualization Framework with Linux microVMs.
func NewDetectedRuntime() Runtime {
	slog.Info("macOS detected, using Apple Virtualization Framework runtime")
	return NewAppleRuntime(AppleRuntimeConfig{})
}
