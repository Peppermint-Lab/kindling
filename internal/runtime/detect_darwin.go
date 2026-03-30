//go:build darwin

package runtime

// NewDetectedRuntime returns the best runtime for this host.
// On macOS: Apple Virtualization Framework with Linux microVMs.
func NewDetectedRuntime(cfg HostRuntimeConfig) Runtime {
	_ = DescribeHostRuntime(cfg)
	return NewAppleRuntime(AppleRuntimeConfig{
		KernelPath:    cfg.AppleKernelPath,
		InitramfsPath: cfg.AppleInitramfsPath,
		PullAuth:      cfg.PullAuth,
	})
}
