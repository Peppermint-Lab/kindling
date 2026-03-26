package deploy

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestEffectiveReplicaCount(t *testing.T) {
	wake := pgtype.Timestamptz{Valid: true, Time: time.Now()}
	proj := queries.Project{DesiredInstanceCount: 3, ScaledToZero: false}
	dep := queries.Deployment{}
	if got := effectiveReplicaCount(proj, dep); got != 3 {
		t.Fatalf("got %d want 3", got)
	}
	proj.ScaledToZero = true
	if got := effectiveReplicaCount(proj, dep); got != 0 {
		t.Fatalf("scaled_to_zero: got %d want 0", got)
	}
	dep.WakeRequestedAt = wake
	if got := effectiveReplicaCount(proj, dep); got != 3 {
		t.Fatalf("wake + scaled_to_zero: got %d want 3", got)
	}
	proj.ScaledToZero = false
	proj.DesiredInstanceCount = 0
	if got := effectiveReplicaCount(proj, dep); got != 1 {
		t.Fatalf("wake + desired 0: got %d want 1", got)
	}
	dep.WakeRequestedAt = pgtype.Timestamptz{}
	if got := effectiveReplicaCount(proj, dep); got != 0 {
		t.Fatalf("desired 0: got %d want 0", got)
	}
}

func TestRequiresExternalHealthCheck(t *testing.T) {
	if requiresExternalHealthCheck("apple-vz") {
		t.Fatal("expected apple-vz to rely on runtime readiness instead of external health checks")
	}

	if !requiresExternalHealthCheck("crun") {
		t.Fatal("expected crun runtime to keep external health checks")
	}
}
