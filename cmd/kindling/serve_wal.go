package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/listener"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/rpc"
)

// walDeps holds dependencies for WAL listener callbacks.
type walDeps struct {
	q                    *queries.Queries
	deploymentReconciler *reconciler.Scheduler
	buildReconciler      *reconciler.Scheduler
	vmReconciler         *reconciler.Scheduler
	domainReconciler     *reconciler.Scheduler
	serverReconciler     *reconciler.Scheduler
	migrationReconciler  *reconciler.Scheduler
	volumeOpReconciler   *reconciler.Scheduler
	dashboardEvents      *rpc.DashboardEventBroker
	publishDeployScopes  func(projectID uuid.UUID)
	notifyRouteChange    func()
	edgeEnabled          bool
}

func newWALListener(databaseURL string, deps walDeps) *listener.Listener {
	return listener.New(listener.Config{
		DatabaseURL: databaseURL,
		OnDeployment: func(ctx context.Context, id uuid.UUID) {
			if deps.deploymentReconciler != nil {
				deps.deploymentReconciler.ScheduleNow(id)
			}
			if dep, err := deps.q.DeploymentFirstByID(ctx, pgtype.UUID{Bytes: id, Valid: true}); err == nil {
				deps.publishDeployScopes(uuid.UUID(dep.ProjectID.Bytes))
			}
		},
		OnDeploymentInstance: func(ctx context.Context, instanceID uuid.UUID) {
			inst, err := deps.q.DeploymentInstanceFirstByID(ctx, pgtype.UUID{Bytes: instanceID, Valid: true})
			if err == nil && inst.DeploymentID.Valid && deps.deploymentReconciler != nil {
				deps.deploymentReconciler.ScheduleNow(uuid.UUID(inst.DeploymentID.Bytes))
			}
			if err == nil && inst.DeploymentID.Valid {
				if dep, err2 := deps.q.DeploymentFirstByID(ctx, inst.DeploymentID); err2 == nil {
					deps.publishDeployScopes(uuid.UUID(dep.ProjectID.Bytes))
				}
			}
			if deps.dashboardEvents != nil {
				deps.dashboardEvents.Publish(rpc.TopicServers)
			}
			if deps.edgeEnabled {
				deps.notifyRouteChange()
			}
		},
		OnProject: func(ctx context.Context, projectID uuid.UUID) {
			dep, err := deps.q.DeploymentLatestRunningByProjectID(ctx, pgtype.UUID{Bytes: projectID, Valid: true})
			if err == nil && deps.deploymentReconciler != nil {
				deps.deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
			}
			if deps.dashboardEvents != nil {
				deps.dashboardEvents.PublishMany(
					rpc.TopicProjects,
					rpc.TopicProject(projectID),
					rpc.TopicProjectDeployments(projectID),
				)
			}
		},
		OnBuild: func(ctx context.Context, id uuid.UUID) {
			if deps.buildReconciler != nil {
				deps.buildReconciler.ScheduleNow(id)
			}
			if b, err := deps.q.BuildFirstByID(ctx, pgtype.UUID{Bytes: id, Valid: true}); err == nil {
				deps.publishDeployScopes(uuid.UUID(b.ProjectID.Bytes))
			}
		},
		OnVM: func(ctx context.Context, id uuid.UUID) {
			if deps.vmReconciler != nil {
				deps.vmReconciler.ScheduleNow(id)
			}
			dep, err := deps.q.DeploymentFindByVMID(ctx, pgtype.UUID{Bytes: id, Valid: true})
			if err == nil {
				if deps.deploymentReconciler != nil {
					deps.deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
				}
				deps.publishDeployScopes(uuid.UUID(dep.ProjectID.Bytes))
			}
			if deps.dashboardEvents != nil {
				deps.dashboardEvents.Publish(rpc.TopicServers)
			}
			if deps.edgeEnabled {
				deps.notifyRouteChange()
			}
		},
		OnDomain: func(ctx context.Context, id uuid.UUID) {
			if deps.domainReconciler != nil {
				deps.domainReconciler.ScheduleNow(id)
			}
			if projID, err := deps.q.DomainProjectIDByDomainID(ctx, pgtype.UUID{Bytes: id, Valid: true}); err == nil && projID.Valid {
				deps.publishDeployScopes(uuid.UUID(projID.Bytes))
			}
			if deps.edgeEnabled && deps.domainReconciler == nil {
				deps.notifyRouteChange()
			}
		},
		OnServer: func(ctx context.Context, id uuid.UUID) {
			if deps.serverReconciler != nil {
				deps.serverReconciler.ScheduleNow(id)
			}
			if deps.dashboardEvents != nil {
				deps.dashboardEvents.Publish(rpc.TopicServers)
			}
			if deps.edgeEnabled && deps.serverReconciler == nil {
				deps.notifyRouteChange()
			}
		},
		OnInstanceMigration: func(ctx context.Context, id uuid.UUID) {
			if deps.migrationReconciler != nil {
				deps.migrationReconciler.ScheduleNow(id)
			}
			if deps.dashboardEvents != nil {
				deps.dashboardEvents.Publish(rpc.TopicServers)
			}
		},
		OnProjectVolumeOperation: func(ctx context.Context, id uuid.UUID) {
			if deps.volumeOpReconciler != nil {
				deps.volumeOpReconciler.ScheduleNow(id)
			}
			op, err := deps.q.ProjectVolumeOperationFindByID(ctx, pgtype.UUID{Bytes: id, Valid: true})
			if err != nil {
				return
			}
			vol, err := deps.q.ProjectVolumeFindByID(ctx, op.ProjectVolumeID)
			if err != nil {
				return
			}
			deps.publishDeployScopes(uuid.UUID(vol.ProjectID.Bytes))
			if deps.dashboardEvents != nil {
				deps.dashboardEvents.Publish(rpc.TopicServers)
			}
		},
	})
}

// startWALListener starts the WAL listener in a goroutine with exponential backoff.
func startWALListener(ctx context.Context, wal *listener.Listener) {
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			if err := wal.Start(ctx); err != nil && ctx.Err() == nil {
				slog.Error("WAL listener failed", "error", err, "retry_in", backoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < walBackoffMax {
					backoff *= 2
					if backoff > walBackoffMax {
						backoff = walBackoffMax
					}
				}
				continue
			}
			return
		}
	}()
	slog.Info("WAL listener started")
}
