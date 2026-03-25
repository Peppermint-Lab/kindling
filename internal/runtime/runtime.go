// Package runtime provides the interface for running app instances.
// Two implementations: Cloud Hypervisor (KVM hosts) and crun (non-KVM).
// Detected automatically at startup via /dev/kvm.
package runtime

import (
	"context"
	"log/slog"
	"os"

	"github.com/google/uuid"
)

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
