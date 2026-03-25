//go:build darwin

package runtime

import "log/slog"

// NewDetectedRuntime returns the best runtime for this host.
// On macOS: Docker (images are built with docker, run with docker).
// Apple VZ requires a kernel + initramfs and is for future native microVM support.
func NewDetectedRuntime() Runtime {
	slog.Info("macOS detected, using Docker runtime")
	return NewCrunRuntime() // falls back to Docker when crun isn't available
}
