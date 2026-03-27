package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/preview"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/volumeops"
)

func runProjectVolumeOperationRecoveryLoop(ctx context.Context, q *queries.Queries, sched *reconciler.Scheduler) {
	if q == nil || sched == nil {
		return
	}
	ticker := time.NewTicker(volumeRecoveryInterval)
	defer ticker.Stop()
	for {
		if err := volumeops.QueueRecoverableOperations(ctx, q, sched); err != nil && ctx.Err() == nil {
			slog.Warn("project volume operation recovery sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runIdleScaleDownLoop periodically marks eligible projects as scaled_to_zero
// so the deployment reconciler can drain instances. Only one process holds the
// advisory lock per cluster at a time.
func runIdleScaleDownLoop(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler, cfgMgr *config.Manager) {
	ticker := time.NewTicker(periodicReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			idleSeconds := int64(300)
			if cfgMgr != nil && cfgMgr.Snapshot() != nil {
				idleSeconds = cfgMgr.Snapshot().ScaleToZeroIdleSeconds
			}
			runIdleScaleDownOnce(ctx, databaseURL, q, deploymentReconciler, idleSeconds)
		}
	}
}

func runPreviewCleanupLoop(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler) {
	ticker := time.NewTicker(periodicReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			preview.RunCleanupOnce(ctx, databaseURL, q, deploymentReconciler)
		}
	}
}

func runPreviewIdleScaleDownLoop(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler, cfgMgr *config.Manager) {
	ticker := time.NewTicker(periodicReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			idle := int64(300)
			if cfgMgr != nil && cfgMgr.Snapshot() != nil {
				idle = cfgMgr.Snapshot().PreviewIdleSeconds
			}
			preview.RunIdleScaleDownOnce(ctx, databaseURL, q, deploymentReconciler, idle)
		}
	}
}

func runIdleScaleDownOnce(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler, idleSeconds int64) {
	leaderConn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		slog.Debug("idle scaler: db connect", "error", err)
		return
	}
	defer leaderConn.Close(context.Background())

	qLeader := queries.New(leaderConn)
	acquired, err := qLeader.TrySessionAdvisoryLock(ctx, "kindling_idle_scaler")
	if err != nil || !acquired {
		return
	}
	// Lock released when connection closes (defer).

	projects, err := q.ProjectsFindForIdleScaleDown(ctx, idleSeconds)
	if err != nil {
		slog.Warn("idle scaler: list projects", "error", err)
		return
	}
	for _, p := range projects {
		if err := q.ProjectMarkScaledToZero(ctx, p.ID); err != nil {
			slog.Warn("idle scaler: mark scaled", "project_id", p.ID, "error", err)
			continue
		}
		dep, err := q.DeploymentLatestRunningByProjectID(ctx, p.ID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("idle scaler: latest deployment", "project_id", p.ID, "error", err)
			}
			continue
		}
		deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
		slog.Info("idle scale-to-zero", "project_id", p.ID, "deployment_id", dep.ID)
	}
}
