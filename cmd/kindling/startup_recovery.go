package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

type startupRecoveryQuery interface {
	DeploymentFindRecoverableByServerID(context.Context, pgtype.UUID) ([]queries.Deployment, error)
}

type startupRecoveryScheduler interface {
	ScheduleNow(uuid.UUID)
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
