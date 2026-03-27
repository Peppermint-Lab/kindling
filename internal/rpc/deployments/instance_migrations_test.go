package deployments

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestParseLiveMigrationWorkerMetadata(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"runtime":                  "cloud-hypervisor",
		"live_migration_enabled":   true,
		"cloud_hypervisor_version": "cloud-hypervisor 46.0",
		"shared_rootfs_dir":        "/mnt/kindling-rootfs",
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := parseLiveMigrationWorkerMetadata(queries.ServerComponentStatus{
		Component: "worker",
		Metadata:  payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Runtime != "cloud-hypervisor" {
		t.Fatalf("Runtime = %q, want cloud-hypervisor", meta.Runtime)
	}
	if !meta.LiveMigrationEnabled {
		t.Fatal("LiveMigrationEnabled = false, want true")
	}
	if meta.CloudHypervisorVersion != "cloud-hypervisor 46.0" {
		t.Fatalf("CloudHypervisorVersion = %q", meta.CloudHypervisorVersion)
	}
	if meta.SharedRootfsDir != "/mnt/kindling-rootfs" {
		t.Fatalf("SharedRootfsDir = %q", meta.SharedRootfsDir)
	}
}

func TestValidateLiveMigrationSource(t *testing.T) {
	err := validateLiveMigrationSource(liveMigrationWorkerMetadata{
		Runtime:                "cloud-hypervisor",
		LiveMigrationEnabled:   true,
		CloudHypervisorVersion: "cloud-hypervisor 46.0",
		SharedRootfsDir:        "/mnt/kindling-rootfs",
	})
	if err != nil {
		t.Fatalf("validateLiveMigrationSource returned error: %v", err)
	}
	err = validateLiveMigrationSource(liveMigrationWorkerMetadata{
		Runtime:              "cloud-hypervisor",
		LiveMigrationEnabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "shared rootfs") {
		t.Fatalf("validateLiveMigrationSource missing shared rootfs error = %v", err)
	}
}

func TestValidateLiveMigrationDestination(t *testing.T) {
	server := queries.Server{
		ID:         pgtype.UUID{Bytes: uuid.New(), Valid: true},
		InternalIp: "10.0.0.4",
		Status:     "active",
	}
	source := liveMigrationWorkerMetadata{
		Runtime:                "cloud-hypervisor",
		LiveMigrationEnabled:   true,
		CloudHypervisorVersion: "cloud-hypervisor 46.0",
		SharedRootfsDir:        "/mnt/kindling-rootfs",
	}
	err := validateLiveMigrationDestination(source, server, liveMigrationWorkerMetadata{
		Runtime:                "cloud-hypervisor",
		LiveMigrationEnabled:   true,
		CloudHypervisorVersion: "cloud-hypervisor 46.0",
		SharedRootfsDir:        "/mnt/kindling-rootfs",
	})
	if err != nil {
		t.Fatalf("validateLiveMigrationDestination returned error: %v", err)
	}
	err = validateLiveMigrationDestination(source, server, liveMigrationWorkerMetadata{
		Runtime:                "cloud-hypervisor",
		LiveMigrationEnabled:   true,
		CloudHypervisorVersion: "cloud-hypervisor 45.0",
		SharedRootfsDir:        "/mnt/kindling-rootfs",
	})
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("validateLiveMigrationDestination version mismatch error = %v", err)
	}
	err = validateLiveMigrationDestination(source, server, liveMigrationWorkerMetadata{
		Runtime:                "cloud-hypervisor",
		LiveMigrationEnabled:   true,
		CloudHypervisorVersion: "cloud-hypervisor 46.0",
		SharedRootfsDir:        "/other/rootfs",
	})
	if err == nil || !strings.Contains(err.Error(), "shared rootfs") {
		t.Fatalf("validateLiveMigrationDestination shared rootfs mismatch error = %v", err)
	}
}

func TestValidateLiveMigrationProjectVolume(t *testing.T) {
	if err := validateLiveMigrationProjectVolume(queries.ProjectVolume{}, pgx.ErrNoRows); err != nil {
		t.Fatalf("expected no project volume to allow live migration, got %v", err)
	}
	if err := validateLiveMigrationProjectVolume(queries.ProjectVolume{}, nil); !errors.Is(err, errLiveMigrationPersistentVolume) {
		t.Fatalf("expected persistent volume error, got %v", err)
	}
}
