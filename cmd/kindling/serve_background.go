package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/autoscale"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/preview"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/sandbox"
	"github.com/kindlingvm/kindling/internal/volumeops"
)

const buildRecoveryInterval = 1 * time.Minute     // interval for build orphan recovery sweep
const buildRecoveryStuckTimeout = 15 * time.Minute // must match internal/builder/buildStuckTimeout

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

// runBuildRecoveryLoop periodically finds builds orphaned by dead servers and
// resets them to 'pending' so the build reconciler can retry them. Only one
// server in the cluster runs this loop by acquiring a session advisory lock.
func runBuildRecoveryLoop(ctx context.Context, databaseURL string, q *queries.Queries, buildReconciler *reconciler.Scheduler) {
	ticker := time.NewTicker(buildRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runBuildRecoveryOnce(ctx, databaseURL, q, buildReconciler)
		}
	}
}

func runBuildRecoveryOnce(ctx context.Context, databaseURL string, q *queries.Queries, buildReconciler *reconciler.Scheduler) {
	leaderConn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		slog.Debug("build recovery: db connect", "error", err)
		return
	}
	defer leaderConn.Close(context.Background())

	qLeader := queries.New(leaderConn)
	acquired, err := qLeader.TrySessionAdvisoryLock(ctx, "kindling_build_recovery")
	if err != nil || !acquired {
		return
	}

	orphaned, err := q.BuildResetOrphaned(ctx)
	if err != nil {
		slog.Warn("build recovery: find orphaned builds", "error", err)
		return
	}
	for _, b := range orphaned {
		slog.Warn("build recovery: resetting orphaned build (dead server)",
			"build_id", b.ID, "processing_by", b.ProcessingBy)
		if _, err := q.BuildResetForRetry(ctx, b.ID); err != nil {
			slog.Warn("build recovery: reset orphaned build", "build_id", b.ID, "error", err)
			continue
		}
		if buildReconciler != nil {
			buildReconciler.ScheduleNow(uuid.UUID(b.ID.Bytes))
		}
	}

	stale, err := q.BuildResetStale(ctx, int64(buildRecoveryStuckTimeout/time.Second))
	if err != nil {
		slog.Warn("build recovery: find stale builds", "error", err)
		return
	}
	for _, b := range stale {
		slog.Warn("build recovery: resetting stale build (no server, timed out)",
			"build_id", b.ID, "status", b.Status)
		if _, err := q.BuildResetForRetry(ctx, b.ID); err != nil {
			slog.Warn("build recovery: reset stale build", "build_id", b.ID, "error", err)
			continue
		}
		if buildReconciler != nil {
			buildReconciler.ScheduleNow(uuid.UUID(b.ID.Bytes))
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

func runProjectAutoscaleLoop(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler) {
	ticker := time.NewTicker(periodicReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			autoscale.RunOnce(ctx, databaseURL, q, deploymentReconciler)
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

func runSandboxExpiryLoop(ctx context.Context, databaseURL string, q *queries.Queries, sandboxReconciler *reconciler.Scheduler) {
	ticker := time.NewTicker(periodicReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sandbox.RunExpiryOnce(ctx, databaseURL, q, sandboxReconciler)
		}
	}
}

func runSandboxIdleLoop(ctx context.Context, databaseURL string, q *queries.Queries, sandboxReconciler *reconciler.Scheduler) {
	ticker := time.NewTicker(periodicReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sandbox.RunIdleSuspendOnce(ctx, databaseURL, q, sandboxReconciler)
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
