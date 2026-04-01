package servers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

func TestBuildServerVolumeOut(t *testing.T) {
	t.Parallel()

	volumeID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	projectID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	serverID := uuid.MustParse("99999999-8888-7777-6666-555555555555")
	vmID := uuid.MustParse("12345678-1234-1234-1234-1234567890ab")

	out := BuildServerVolumeOut(queries.ProjectVolume{
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

func TestBuildServerInstancesFromUsageLatest_RunningCount(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	instID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	depID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	projID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	rows := []queries.ServerInstanceUsageLatestRow{
		{
			DeploymentInstanceID: pgtype.UUID{Bytes: instID, Valid: true},
			DeploymentID:         pgtype.UUID{Bytes: depID, Valid: true},
			ProjectID:            pgtype.UUID{Bytes: projID, Valid: true},
			ProjectName:          "p1",
			Role:                 "active",
			Status:               "running",
			MemoryRssBytes:       1024,
		},
		{
			DeploymentInstanceID: pgtype.UUID{Bytes: uuid.MustParse("44444444-4444-4444-4444-444444444444"), Valid: true},
			DeploymentID:         pgtype.UUID{Bytes: depID, Valid: true},
			ProjectID:            pgtype.UUID{Bytes: projID, Valid: true},
			ProjectName:          "p1",
			Role:                 "active",
			Status:               "stopped",
			MemoryRssBytes:       0,
		},
	}
	instances, running := buildServerInstancesFromUsageLatest(rows, now)
	if running != 1 {
		t.Fatalf("running = %d, want 1", running)
	}
	if len(instances) != 2 {
		t.Fatalf("len(instances) = %d", len(instances))
	}
}

func TestUsageLatestForOrgRowsToLatestPreservesFields(t *testing.T) {
	t.Parallel()
	instID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	orgRow := queries.ServerInstanceUsageLatestForOrgRow{
		DeploymentInstanceID: pgtype.UUID{Bytes: instID, Valid: true},
		ProjectName:          "roundtrip",
		Role:                 "active",
		Status:               "running",
	}
	out := usageLatestForOrgRowsToLatest([]queries.ServerInstanceUsageLatestForOrgRow{orgRow})
	if len(out) != 1 {
		t.Fatalf("len = %d", len(out))
	}
	if out[0].ProjectName != "roundtrip" || out[0].Role != "active" {
		t.Fatalf("unexpected %+v", out[0])
	}
	if pguuid.ToString(out[0].DeploymentInstanceID) != instID.String() {
		t.Fatalf("id mismatch")
	}
}
