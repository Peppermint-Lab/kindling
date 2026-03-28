package main

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	crunrt "github.com/kindlingvm/kindling/internal/runtime"
)

type startupRecoveryQueryStub struct {
	rows         []queries.Deployment
	retainedRows []queries.DeploymentInstanceRetainedStateByServerIDRow
	err          error
}

func (s startupRecoveryQueryStub) DeploymentFindRecoverableByServerID(_ context.Context, _ pgtype.UUID) ([]queries.Deployment, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

func (s startupRecoveryQueryStub) DeploymentInstanceRetainedStateByServerID(_ context.Context, _ pgtype.UUID) ([]queries.DeploymentInstanceRetainedStateByServerIDRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.retainedRows, nil
}

type startupRecoverySchedulerStub struct {
	ids []uuid.UUID
}

func (s *startupRecoverySchedulerStub) ScheduleNow(id uuid.UUID) {
	s.ids = append(s.ids, id)
}

type retainedStateRuntimeStub struct {
	stateDir        string
	recoveryResult  crunrt.RetainedStateRecovery
	recoveryErr     error
	gotInstanceIDs  []uuid.UUID
	gotTemplateRefs []string
}

func (s *retainedStateRuntimeStub) StateDir() string { return s.stateDir }

func (s *retainedStateRuntimeStub) DurableFastWakeEnabled() bool { return s.stateDir != "" }

func (s *retainedStateRuntimeStub) RecoverRetainedState(_ context.Context, keepInstanceIDs []uuid.UUID, keepTemplateRefs []string) (crunrt.RetainedStateRecovery, error) {
	s.gotInstanceIDs = append([]uuid.UUID(nil), keepInstanceIDs...)
	s.gotTemplateRefs = append([]string(nil), keepTemplateRefs...)
	return s.recoveryResult, s.recoveryErr
}

func TestQueueStartupRecoverySchedulesRecoverableDeployments(t *testing.T) {
	serverID := uuid.New()
	first := uuid.New()
	second := uuid.New()

	scheduler := &startupRecoverySchedulerStub{}
	routeChanges := 0

	queued, err := queueStartupRecovery(
		context.Background(),
		startupRecoveryQueryStub{
			rows: []queries.Deployment{
				{ID: pgtype.UUID{Bytes: first, Valid: true}},
				{ID: pgtype.UUID{Bytes: second, Valid: true}},
				{},
			},
		},
		serverID,
		scheduler,
		func() { routeChanges++ },
	)
	if err != nil {
		t.Fatalf("queueStartupRecovery returned error: %v", err)
	}
	if queued != 2 {
		t.Fatalf("queued = %d, want 2", queued)
	}
	if len(scheduler.ids) != 2 {
		t.Fatalf("scheduled %d deployments, want 2", len(scheduler.ids))
	}
	if scheduler.ids[0] != first || scheduler.ids[1] != second {
		t.Fatalf("scheduled IDs = %v, want [%s %s]", scheduler.ids, first, second)
	}
	if routeChanges != 1 {
		t.Fatalf("routeChanges = %d, want 1", routeChanges)
	}
}

func TestQueueStartupRecoveryReturnsQueryErrors(t *testing.T) {
	scheduler := &startupRecoverySchedulerStub{}
	wantErr := errors.New("boom")

	queued, err := queueStartupRecovery(
		context.Background(),
		startupRecoveryQueryStub{err: wantErr},
		uuid.New(),
		scheduler,
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if queued != 0 {
		t.Fatalf("queued = %d, want 0", queued)
	}
	if len(scheduler.ids) != 0 {
		t.Fatalf("scheduled %d deployments, want 0", len(scheduler.ids))
	}
}

func TestQueueStartupRecoveryNoopsWithoutServerOrScheduler(t *testing.T) {
	queued, err := queueStartupRecovery(
		context.Background(),
		startupRecoveryQueryStub{
			rows: []queries.Deployment{{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}}},
		},
		uuid.Nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("queueStartupRecovery returned error: %v", err)
	}
	if queued != 0 {
		t.Fatalf("queued = %d, want 0", queued)
	}
}

func TestRecoverWorkerRetainedStatePassesExpectedReferences(t *testing.T) {
	serverID := uuid.New()
	keepInstance := uuid.New()
	rt := &retainedStateRuntimeStub{
		stateDir: "/data/kindling-runtime/cloud-hypervisor",
	}
	keepTemplateDir := "/data/kindling-runtime/cloud-hypervisor/templates/template-1"

	err := recoverWorkerRetainedState(context.Background(), startupRecoveryQueryStub{
		retainedRows: []queries.DeploymentInstanceRetainedStateByServerIDRow{
			{
				DeploymentInstanceID: pgtype.UUID{Bytes: keepInstance, Valid: true},
				SnapshotRef:          pgtype.Text{String: keepTemplateDir, Valid: true},
			},
		},
	}, serverID, rt)
	if err != nil {
		t.Fatalf("recoverWorkerRetainedState: %v", err)
	}
	if len(rt.gotInstanceIDs) != 1 || rt.gotInstanceIDs[0] != keepInstance {
		t.Fatalf("gotInstanceIDs = %v, want [%s]", rt.gotInstanceIDs, keepInstance)
	}
	if len(rt.gotTemplateRefs) != 1 || rt.gotTemplateRefs[0] != keepTemplateDir {
		t.Fatalf("gotTemplateRefs = %v, want [%s]", rt.gotTemplateRefs, keepTemplateDir)
	}
}
