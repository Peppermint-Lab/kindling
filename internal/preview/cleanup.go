package preview

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
)

// RunCleanupOnce acquires a cluster-wide advisory lock, reap preview environments
// past expires_at, and deletes preview_environments (CASCADE removes preview domains).
// A separate loop should have marked deployments stopped on PR close.
func RunCleanupOnce(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler) {
	leaderConn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		slog.Debug("preview cleanup: db connect", "error", err)
		return
	}
	defer leaderConn.Close(context.Background())

	qLeader := queries.New(leaderConn)
	acquired, err := qLeader.TrySessionAdvisoryLock(ctx, "kindling_preview_cleanup")
	if err != nil || !acquired {
		return
	}

	envs, err := q.PreviewEnvironmentsDueForCleanup(ctx)
	if err != nil {
		slog.Warn("preview cleanup: list due", "error", err)
		return
	}
	for _, pe := range envs {
		deps, err := q.DeploymentsByPreviewEnvironmentID(ctx, pe.ID)
		if err != nil {
			slog.Warn("preview cleanup: list deployments", "preview_env_id", pe.ID, "error", err)
			continue
		}
		if err := q.DeploymentsMarkStoppedByPreviewEnvironment(ctx, pe.ID); err != nil {
			slog.Warn("preview cleanup: mark stopped", "preview_env_id", pe.ID, "error", err)
		}
		for _, dep := range deps {
			deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
		}
		if err := q.PreviewEnvironmentDelete(ctx, pe.ID); err != nil {
			slog.Warn("preview cleanup: delete env", "preview_env_id", pe.ID, "error", err)
			continue
		}
		slog.Info("preview environment cleaned up", "preview_env_id", pe.ID, "project_id", pe.ProjectID)
	}
}

// RunIdleScaleDownOnce marks idle preview deployments scaled-to-zero (per deployment),
// mirroring production idle scale semantics.
func RunIdleScaleDownOnce(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler, idleSeconds int64) {
	if idleSeconds <= 0 {
		idleSeconds = 300
	}
	leaderConn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		slog.Debug("preview idle scaler: db connect", "error", err)
		return
	}
	defer leaderConn.Close(context.Background())

	qLeader := queries.New(leaderConn)
	acquired, err := qLeader.TrySessionAdvisoryLock(ctx, "kindling_preview_idle")
	if err != nil || !acquired {
		return
	}

	deps, err := q.DeploymentsFindPreviewForIdleScaleDown(ctx, idleSeconds)
	if err != nil {
		slog.Warn("preview idle: list", "error", err)
		return
	}
	for _, dep := range deps {
		if err := q.DeploymentPreviewMarkScaledToZero(ctx, dep.ID); err != nil {
			slog.Warn("preview idle: mark scaled", "deployment_id", dep.ID, "error", err)
			continue
		}
		deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
		slog.Info("preview idle scale-to-zero", "deployment_id", dep.ID, "project_id", dep.ProjectID)
	}
}
