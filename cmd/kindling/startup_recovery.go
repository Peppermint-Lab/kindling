package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	crunrt "github.com/kindlingvm/kindling/internal/runtime"
)

type startupRecoveryQuery interface {
	DeploymentFindRecoverableByServerID(context.Context, pgtype.UUID) ([]queries.Deployment, error)
	DeploymentInstanceRetainedStateByServerID(context.Context, pgtype.UUID) ([]queries.DeploymentInstanceRetainedStateByServerIDRow, error)
	VMFirstByID(context.Context, pgtype.UUID) (queries.Vm, error)
	VMUpdateStatus(context.Context, queries.VMUpdateStatusParams) (queries.Vm, error)
}

type startupRecoveryScheduler interface {
	ScheduleNow(uuid.UUID)
}

func recoverWorkerRetainedState(ctx context.Context, q startupRecoveryQuery, serverID uuid.UUID, rt crunrt.DurableRetainedStateRuntime) error {
	if rt == nil || q == nil || serverID == uuid.Nil {
		return nil
	}

	rows, err := q.DeploymentInstanceRetainedStateByServerID(ctx, pgtype.UUID{Bytes: serverID, Valid: true})
	if err != nil {
		return fmt.Errorf("retained state rows: %w", err)
	}

	instanceIDs := make([]uuid.UUID, 0, len(rows))
	templateRefs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.DeploymentInstanceID.Valid {
			instanceIDs = append(instanceIDs, uuid.UUID(row.DeploymentInstanceID.Bytes))
		}
		if row.SnapshotRef.Valid && strings.TrimSpace(row.SnapshotRef.String) != "" {
			templateRefs = append(templateRefs, row.SnapshotRef.String)
		}
	}

	result, err := rt.RecoverRetainedState(ctx, instanceIDs, templateRefs)
	if err != nil {
		return fmt.Errorf("recover cloud-hypervisor retained state: %w", err)
	}
	slog.Info("cloud-hypervisor retained state recovered",
		"state_dir", result.StateDir,
		"instance_dirs_kept", result.InstanceDirsKept,
		"instance_dirs_pruned", result.InstanceDirsPruned,
		"template_dirs_kept", result.TemplateDirsKept,
		"template_dirs_pruned", result.TemplateDirsPruned,
	)

	for _, row := range rows {
		if !row.VmID.Valid {
			continue
		}
		vm, err := q.VMFirstByID(ctx, row.VmID)
		if err != nil || vm.Status != "suspending" {
			continue
		}
		if _, err := q.VMUpdateStatus(ctx, queries.VMUpdateStatusParams{
			ID:     row.VmID,
			Status: "suspended",
		}); err != nil {
			return fmt.Errorf("normalize retained vm %s to suspended: %w", uuid.UUID(row.VmID.Bytes), err)
		}
	}
	return nil
}

func queueStartupRecovery(
	ctx context.Context,
	q startupRecoveryQuery,
	serverID uuid.UUID,
	deployments startupRecoveryScheduler,
	notifyRouteChange func(),
) (int, error) {
	if q == nil || deployments == nil || serverID == uuid.Nil {
		return 0, nil
	}

	rows, err := q.DeploymentFindRecoverableByServerID(ctx, pgtype.UUID{Bytes: serverID, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("recoverable deployments: %w", err)
	}

	queued := 0
	for _, dep := range rows {
		if !dep.ID.Valid {
			continue
		}
		deployments.ScheduleNow(uuid.UUID(dep.ID.Bytes))
		queued++
	}
	if queued > 0 && notifyRouteChange != nil {
		notifyRouteChange()
	}
	return queued, nil
}
