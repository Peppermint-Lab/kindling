//go:build !linux && !darwin

package runtime

// NewDetectedRuntime returns a crun-labeled runtime on platforms without a dedicated worker runtime.
func NewDetectedRuntime(cfg HostRuntimeConfig) Runtime {
	return NewCrunRuntime(cfg.AdvertiseHost, cfg.PullAuth)
}
