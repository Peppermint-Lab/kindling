//go:build darwin

package runtime

import "log/slog"

// NewDetectedRuntime returns the best runtime for this host.
// On macOS: Apple Virtualization Framework with Linux microVMs.
func NewDetectedRuntime(cfg HostRuntimeConfig) Runtime {
	slog.Info("macOS detected, using Apple Virtualization Framework runtime")
	return NewAppleRuntime(AppleRuntimeConfig{
		KernelPath:    cfg.AppleKernelPath,
		InitramfsPath: cfg.AppleInitramfsPath,
		PullAuth:      cfg.PullAuth,
	})
}
