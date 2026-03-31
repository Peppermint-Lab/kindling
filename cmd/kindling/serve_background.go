package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/autoscale"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/githubapi"
	"github.com/kindlingvm/kindling/internal/preview"
	"github.com/kindlingvm/kindling/internal/reconciler"
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

const webhookPollingInterval = 5 * time.Minute // interval between GitHub commit polling sweeps

// runWebhookPollingLoop periodically compares each project's GitHub branch HEAD
// against the last successful production deployment. If GitHub is ahead, it
// triggers a new deployment. This serves as a fallback when webhooks are missed.
func runWebhookPollingLoop(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler, cfgMgr *config.Manager) {
	ticker := time.NewTicker(webhookPollingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runWebhookPollingOnce(ctx, databaseURL, q, deploymentReconciler, cfgMgr)
		}
	}
}

func runWebhookPollingOnce(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler, cfgMgr *config.Manager) {
	githubToken := ""
	if cfgMgr != nil && cfgMgr.Snapshot() != nil {
		githubToken = cfgMgr.Snapshot().GitHubToken
	}

	projects, err := q.ProjectFindAllWithGitHub(ctx)
	if err != nil {
		slog.Warn("webhook polling: list projects", "error", err)
		return
	}
	for _, proj := range projects {
		repo := strings.TrimSpace(proj.GithubRepository)
		if repo == "" {
			continue
		}

		// Resolve the default branch HEAD commit.
		headSHA, _, err := githubapi.ResolveCommit(ctx, nil, githubToken, repo, "")
		if err != nil {
			slog.Warn("webhook polling: resolve commit",
				"project_id", proj.ID, "repo", repo, "error", err)
			continue
		}

		// The default production branch to track (main or master).
		// Use "main" as the default since the webhook handler ignores non-main branches.
		defaultBranch := "main"

		// Find all services for this project and create deployments for each
		// (matching the webhook push handler behavior).
		services, err := q.ServiceListByProjectID(ctx, proj.ID)
		if err != nil {
			slog.Warn("webhook polling: list services",
				"project_id", proj.ID, "error", err)
			continue
		}
		for _, svc := range services {
			// Find the latest successful production deployment for this service.
			latestDep, err := q.DeploymentLatestSuccessfulForProject(ctx, queries.DeploymentLatestSuccessfulForProjectParams{
				ProjectID: proj.ID,
				ServiceID: svc.ID,
			})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("webhook polling: latest deployment",
					"project_id", proj.ID, "service_id", svc.ID, "error", err)
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) && latestDep.GithubCommit == headSHA {
				continue // already deployed
			}

			slog.Info("webhook polling: new commit detected, triggering deploy",
				"project_id", proj.ID, "service_id", svc.ID, "repo", repo,
				"current_sha", headSHA,
				"last_deployed_sha", func() string {
					if latestDep.GithubCommit != "" {
						return latestDep.GithubCommit
					}
					return "(none)"
				}())

			dep, err := q.DeploymentCreate(ctx, queries.DeploymentCreateParams{
				ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
				ProjectID:       proj.ID,
				ServiceID:       svc.ID,
				BuildID:         pgtype.UUID{Valid: false},
				ImageID:         pgtype.UUID{Valid: false},
				GithubCommit:    headSHA,
				GithubBranch:    defaultBranch,
				DeploymentKind:  "production",
			})
			if err != nil {
				slog.Warn("webhook polling: create deployment",
					"project_id", proj.ID, "service_id", svc.ID, "error", err)
				continue
			}
			if deploymentReconciler != nil {
				deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
			}
		}
	}
}
