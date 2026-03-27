// Package runtime provides the interface for running app instances.
// Two implementations: Cloud Hypervisor (KVM hosts) and crun (non-KVM).
// Detected automatically at startup via /dev/kvm.
package runtime

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/oci"
)

// CloudHypervisorHostConfig holds Linux microVM binary and image paths (ignored on non-Linux).
type CloudHypervisorHostConfig struct {
	BinaryPath      string
	KernelPath      string
	InitramfsPath   string
	SharedRootfsDir string
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
	ID               uuid.UUID
	ImageRef         string // OCI image reference (e.g. "kindling/myapp:abc123")
	VCPUs            int
	MemoryMB         int
	Port             int
	Env              []string // KEY=value pairs
	PersistentVolume *PersistentVolumeMount
}

type PersistentVolumeMount struct {
	ID              uuid.UUID
	HostPath        string
	MountPath       string
	SizeGB          int
	Filesystem      string
	CreateIfMissing bool
}

// ResourceStats holds counters suitable for usage sampling (CPU cumulative nanoseconds, RSS, disk I/O).
type ResourceStats struct {
	CPUNanosCumulative int64
	MemoryRSSBytes     int64
	DiskReadBytes      int64
	DiskWriteBytes     int64
	CollectedAt        time.Time
}

type Capability string

const (
	CapabilitySuspendResume Capability = "suspend_resume"
	CapabilityWarmClone     Capability = "warm_clone"
	CapabilityLiveMigration Capability = "live_migration"
)

type StartMetadata struct {
	SnapshotRef     string
	SharedRootfsRef string
	CloneSourceVMID uuid.UUID
}

type MigrationMetadata struct {
	SharedRootfsRef string
	Version         string
}

type PreparedMigrationTarget struct {
	ReceiveAddr string
}

type SendMigrationRequest struct {
	DestinationURL string
	DowntimeMS     int64
	TimeoutSeconds int64
}

// Runtime is the interface for starting and stopping app instances.
type Runtime interface {
	// Name returns the runtime name (e.g. "crun", "cloud-hypervisor").
	Name() string

	// Supports returns whether this runtime implements an optional lifecycle capability.
	Supports(Capability) bool

	// Start starts an instance. Returns the IP address the instance is reachable at.
	Start(ctx context.Context, inst Instance) (ip string, err error)

	// Suspend releases active resources while preserving enough local state for a faster resume.
	Suspend(ctx context.Context, id uuid.UUID) error

	// Resume restarts a suspended instance from retained local state.
	Resume(ctx context.Context, id uuid.UUID) (ip string, err error)

	// CreateTemplate captures retained local state for future warm clones and returns its reference.
	CreateTemplate(ctx context.Context, id uuid.UUID) (snapshotRef string, err error)

	// StartClone starts a new instance from retained template state.
	StartClone(ctx context.Context, inst Instance, snapshotRef string, cloneSourceVMID uuid.UUID) (ip string, meta StartMetadata, err error)

	// MigrationMetadata returns live-migration metadata for a running instance when supported.
	MigrationMetadata(ctx context.Context, id uuid.UUID) (MigrationMetadata, error)

	// PrepareMigrationTarget starts a destination receiver for a future live migration.
	PrepareMigrationTarget(ctx context.Context, id uuid.UUID) (PreparedMigrationTarget, error)

	// SendMigration streams a running instance to a prepared destination.
	SendMigration(ctx context.Context, id uuid.UUID, req SendMigrationRequest) error

	// FinalizeMigrationTarget turns a prepared receiver into a normal running instance.
	FinalizeMigrationTarget(ctx context.Context, id uuid.UUID) (ip string, meta StartMetadata, err error)

	// AbortMigrationTarget tears down a prepared destination receiver.
	AbortMigrationTarget(ctx context.Context, id uuid.UUID) error

	// Stop stops and cleans up an instance.
	Stop(ctx context.Context, id uuid.UUID) error

	// Healthy returns whether the instance's process is still running.
	Healthy(ctx context.Context, id uuid.UUID) bool

	// Logs returns recent stdout/stderr from the instance.
	Logs(ctx context.Context, id uuid.UUID) ([]string, error)

	// StopAll kills all running instances. Called during graceful shutdown.
	StopAll()

	// ResourceStats samples CPU/memory/disk for a running instance (deployment_instance id).
	ResourceStats(ctx context.Context, id uuid.UUID) (ResourceStats, error)
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
