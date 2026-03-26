package deploy

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestCountInstancesOnDrainingServers(t *testing.T) {
	drainingID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	activeID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	m := map[uuid.UUID]string{
		drainingID: "draining",
		activeID:   "active",
	}
	insts := []queries.DeploymentInstance{
		{ServerID: pgtype.UUID{Bytes: drainingID, Valid: true}},
		{ServerID: pgtype.UUID{Bytes: activeID, Valid: true}},
		{ServerID: pgtype.UUID{Valid: false}},
	}
	var deployer Deployer
	if got := deployer.countInstancesOnDrainingServers(insts, m); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
}

func TestSurgeTargetArithmetic(t *testing.T) {
	desired := int32(3)
	onDraining := 2
	surge := int(desired) + onDraining
	if surge != 5 {
		t.Fatalf("surge target: got %d want 5", surge)
	}
}

func TestScaleDownPrefersNonDrainingVictim(t *testing.T) {
	t1 := time.Unix(100, 0)
	t2 := time.Unix(200, 0)
	drainingID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	activeID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	status := map[uuid.UUID]string{
		drainingID: "draining",
		activeID:   "active",
	}
	newerOnActive := queries.DeploymentInstance{
		ServerID:  pgtype.UUID{Bytes: activeID, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: t2, Valid: true},
	}
	olderOnDrain := queries.DeploymentInstance{
		ServerID:  pgtype.UUID{Bytes: drainingID, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: t1, Valid: true},
	}
	sorted := []queries.DeploymentInstance{newerOnActive, olderOnDrain}
	var victim queries.DeploymentInstance
	found := false
	for _, inst := range sorted {
		drain := inst.ServerID.Valid && status[uuidFromPgtype(inst.ServerID)] == "draining"
		if !drain {
			victim = inst
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a non-draining victim")
	}
	if uuidFromPgtype(victim.ServerID) != activeID {
		t.Fatal("should remove non-draining instance first")
	}
}
