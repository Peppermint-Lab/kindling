package rpc

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestBuildServerVolumeOut(t *testing.T) {
	t.Parallel()

	volumeID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	projectID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	serverID := uuid.MustParse("99999999-8888-7777-6666-555555555555")
	vmID := uuid.MustParse("12345678-1234-1234-1234-1234567890ab")

	out := buildServerVolumeOut(queries.ProjectVolume{
		ID:           pgtype.UUID{Bytes: volumeID, Valid: true},
		ProjectID:    pgtype.UUID{Bytes: projectID, Valid: true},
		ServerID:     pgtype.UUID{Bytes: serverID, Valid: true},
		AttachedVmID: pgtype.UUID{Bytes: vmID, Valid: true},
		MountPath:    "/data",
		SizeGb:       10,
		Filesystem:   "ext4",
		Status:       "attached",
		LastError:    " ",
	}, "demo")

	if out.ID != volumeID.String() {
		t.Fatalf("id = %q", out.ID)
	}
	if out.ProjectName != "demo" {
		t.Fatalf("project_name = %q", out.ProjectName)
	}
	if out.ServerID == nil || *out.ServerID != serverID.String() {
		t.Fatalf("server_id = %#v", out.ServerID)
	}
	if out.AttachedVMID == nil || *out.AttachedVMID != vmID.String() {
		t.Fatalf("attached_vm_id = %#v", out.AttachedVMID)
	}
	if out.LastError != "" {
		t.Fatalf("last_error = %q", out.LastError)
	}
}

func TestBuildServerSummaryHealthy(t *testing.T) {
	now := time.Now().UTC()
	server := queries.Server{
		ID:              pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Hostname:        "server-a",
		Status:          "active",
		LastHeartbeatAt: ts(now.Add(-5 * time.Second)),
	}
	rows := []queries.ServerComponentStatus{
		{
			ServerID:      server.ID,
			Component:     "worker",
			Status:        "healthy",
			ObservedAt:    ts(now.Add(-5 * time.Second)),
			LastSuccessAt: ts(now.Add(-5 * time.Second)),
			Metadata:      []byte(`{"runtime":"cloud-hypervisor"}`),
		},
		{
			ServerID:      server.ID,
			Component:     "usage_poller",
			Status:        "healthy",
			ObservedAt:    ts(now.Add(-10 * time.Second)),
			LastSuccessAt: ts(now.Add(-10 * time.Second)),
			Metadata:      []byte(`{"sampled_instances":2}`),
		},
	}

	summary := buildServerSummary(server, 2, 2, 2, rows, now)
	if summary.Health != "healthy" {
		t.Fatalf("expected healthy summary, got %q", summary.Health)
	}
	if summary.Runtime != "cloud-hypervisor" {
		t.Fatalf("expected runtime from metadata, got %q", summary.Runtime)
	}
	if summary.HeartbeatHealth != "healthy" {
		t.Fatalf("expected healthy heartbeat, got %q", summary.HeartbeatHealth)
	}
}

func TestBuildServerSummaryStaleUsagePoller(t *testing.T) {
	now := time.Now().UTC()
	server := queries.Server{
		ID:              pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Hostname:        "server-b",
		Status:          "active",
		LastHeartbeatAt: ts(now.Add(-5 * time.Second)),
	}
	rows := []queries.ServerComponentStatus{
		{
			ServerID:      server.ID,
			Component:     "worker",
			Status:        "healthy",
			ObservedAt:    ts(now.Add(-5 * time.Second)),
			LastSuccessAt: ts(now.Add(-5 * time.Second)),
		},
		{
			ServerID:      server.ID,
			Component:     "usage_poller",
			Status:        "healthy",
			ObservedAt:    ts(now.Add(-40 * time.Second)),
			LastSuccessAt: ts(now.Add(-40 * time.Second)),
		},
	}

	summary := buildServerSummary(server, 1, 1, 1, rows, now)
	if summary.Health != "stale" {
		t.Fatalf("expected stale summary, got %q", summary.Health)
	}
	if summary.Components[3].Health != "stale" {
		t.Fatalf("expected usage poller component to be stale, got %q", summary.Components[3].Health)
	}
}

func TestBuildServerSummaryUnknownWithoutSnapshots(t *testing.T) {
	now := time.Now().UTC()
	server := queries.Server{
		ID:              pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Hostname:        "server-c",
		Status:          "active",
		LastHeartbeatAt: ts(now.Add(-5 * time.Second)),
	}

	summary := buildServerSummary(server, 0, 0, 0, nil, now)
	if summary.Health != "unknown" {
		t.Fatalf("expected unknown summary, got %q", summary.Health)
	}
	for _, component := range summary.Components {
		if component.Enabled {
			t.Fatalf("expected no enabled components, got %+v", component)
		}
	}
}

func ts(v time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: v, Valid: true}
}
