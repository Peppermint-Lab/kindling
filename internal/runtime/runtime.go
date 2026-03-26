// Package runtime provides the interface for running app instances.
// Two implementations: Cloud Hypervisor (KVM hosts) and crun (non-KVM).
// Detected automatically at startup via /dev/kvm.
package runtime

import (
	"context"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/oci"
)

// CloudHypervisorHostConfig holds Linux microVM binary and image paths (ignored on non-Linux).
type CloudHypervisorHostConfig struct {
	BinaryPath    string
	KernelPath    string
	InitramfsPath string
}

// HostRuntimeConfig wires DB-backed (or other) settings into runtime selection.
// Zero value means advertise host unset, auto-detect runtime, and no registry auth.
type HostRuntimeConfig struct {
	// ForceRuntime: "crun", "cloud-hypervisor", or empty for auto-detect (Linux).
	ForceRuntime string
	// AdvertiseHost rewrites loopback/0.0.0.0 published addresses for browser reachability.
	AdvertiseHost string
	// PullAuth is optional credentials for registry image pulls.
	PullAuth *oci.Auth
	// CloudHypervisor paths for the Linux microVM runtime.
	CloudHypervisor CloudHypervisorHostConfig
	// AppleKernelPath / AppleInitramfsPath override defaults under ~/.kindling on macOS.
	AppleKernelPath    string
	AppleInitramfsPath string
}

// Instance represents a running or pending app instance.
type Instance struct {
	ID       uuid.UUID
	ImageRef string // OCI image reference (e.g. "kindling/myapp:abc123")
	VCPUs    int
	MemoryMB int
	Port     int
	Env      []string // KEY=value pairs
}

// Runtime is the interface for starting and stopping app instances.
type Runtime interface {
	// Name returns the runtime name (e.g. "crun", "cloud-hypervisor").
	Name() string

	// Start starts an instance. Returns the IP address the instance is reachable at.
	Start(ctx context.Context, inst Instance) (ip string, err error)

	// Stop stops and cleans up an instance.
	Stop(ctx context.Context, id uuid.UUID) error

	// Healthy returns whether the instance's process is still running.
	Healthy(ctx context.Context, id uuid.UUID) bool

	// Logs returns recent stdout/stderr from the instance.
	Logs(ctx context.Context, id uuid.UUID) ([]string, error)

	// StopAll kills all running instances. Called during graceful shutdown.
	StopAll()
}

// Detect returns the best available runtime for this host.
func Detect() string {
	if _, err := os.Stat("/dev/kvm"); err == nil {
		slog.Info("KVM detected, using cloud-hypervisor runtime")
		return "cloud-hypervisor"
	}
	slog.Info("no KVM detected, using crun runtime")
	return "crun"
}
