package rpc

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestNormalizeProjectVolumeMountPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "default", in: "", want: "/data"},
		{name: "keeps absolute path", in: "/data/uploads", want: "/data/uploads"},
		{name: "cleans path", in: "/data/../cache", want: "/cache"},
		{name: "rejects relative", in: "data", wantErr: true},
		{name: "rejects root", in: "/", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeProjectVolumeMountPath(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeProjectVolumeMountPath(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasCloudHypervisorWorker(t *testing.T) {
	t.Parallel()

	activeID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	crunID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	now := time.Now().UTC()

	servers := []queries.Server{
		{ID: activeID, Status: "active", LastHeartbeatAt: pgtype.Timestamptz{Time: now, Valid: true}},
		{ID: crunID, Status: "active", LastHeartbeatAt: pgtype.Timestamptz{Time: now, Valid: true}},
	}
	rows := []queries.ServerComponentStatus{
		{ServerID: activeID, Component: "worker", Status: "healthy", Metadata: []byte(`{"runtime":"cloud-hypervisor"}`)},
		{ServerID: crunID, Component: "worker", Status: "healthy", Metadata: []byte(`{"runtime":"crun"}`)},
	}

	if !hasCloudHypervisorWorker(rows, servers) {
		t.Fatal("expected a cloud-hypervisor worker to be detected")
	}

	servers[0].Status = "draining"
	if hasCloudHypervisorWorker(rows, servers) {
		t.Fatal("expected draining workers to be ignored")
	}
}

func TestValidatePersistentVolumeScalingBounds(t *testing.T) {
	t.Parallel()

	if err := validatePersistentVolumeScalingBounds(1, true); err != nil {
		t.Fatalf("max=1 should pass: %v", err)
	}
	if err := validatePersistentVolumeScalingBounds(3, false); err != nil {
		t.Fatalf("no volume should pass: %v", err)
	}
	err := validatePersistentVolumeScalingBounds(2, true)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if err.Error() != "persistent volumes require max_instance_count <= 1" {
		t.Fatalf("unexpected error = %q", err.Error())
	}
}
