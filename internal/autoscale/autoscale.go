// Package autoscale adjusts per-service replica targets from HTTP and CPU signals.
package autoscale

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
)

const (
	httpWindowMinutes              = 3
	httpScaleUpRequestsPerMinute   = 60.0
	cpuScaleUpPercentTotal         = 65.0
	httpScaleDownRequestsPerMinute = 25.0
	cpuScaleDownPercentTotal       = 35.0
	cpuLookback                    = 2 * time.Minute
	scaleDownCooldown              = 5 * time.Minute
)

type autoscaleStore interface {
	ProjectFindAll(ctx context.Context) ([]queries.Project, error)
	ServiceListByProjectID(ctx context.Context, projectID pgtype.UUID) ([]queries.Service, error)
	DeploymentLatestRunningByServiceID(ctx context.Context, serviceID pgtype.UUID) (queries.Deployment, error)
	ProjectHTTPUsageRollupsAggregatedByDeployment(ctx context.Context, arg queries.ProjectHTTPUsageRollupsAggregatedByDeploymentParams) ([]queries.ProjectHTTPUsageRollupsAggregatedByDeploymentRow, error)
	InstanceUsageLatestPerInstanceByDeployment(ctx context.Context, arg queries.InstanceUsageLatestPerInstanceByDeploymentParams) ([]queries.InstanceUsageLatestPerInstanceByDeploymentRow, error)
	ServiceSetDesiredInstanceCount(ctx context.Context, arg queries.ServiceSetDesiredInstanceCountParams) error
}

func nonZeroFloor(proj queries.Project) int32 {
	if proj.MaxInstanceCount <= 0 {
		return 0
	}
	if proj.MinInstanceCount > 1 {
		return proj.MinInstanceCount
	}
	return 1
}

func serviceDesiredReplicaCount(proj queries.Project, service *queries.Service) int32 {
	if service != nil && service.DesiredInstanceCount > 0 {
		return service.DesiredInstanceCount
	}
	return proj.DesiredInstanceCount
}

func shouldSkipAutoscale(service *queries.Service, avgRequestsPerMinute float64, hasCPU bool) bool {
	return service != nil && service.DesiredInstanceCount == 0 && avgRequestsPerMinute == 0 && !hasCPU
}

func clampCurrentTarget(proj queries.Project, service *queries.Service) int32 {
	if proj.MaxInstanceCount <= 0 {
		return 0
	}
	target := serviceDesiredReplicaCount(proj, service)
	floor := nonZeroFloor(proj)
	if target < floor {
		target = floor
	}
	if target > proj.MaxInstanceCount {
		target = proj.MaxInstanceCount
	}
	if target < 0 {
		return 0
	}
	return target
}

func targetFromMetrics(proj queries.Project, avgRequestsPerMinute, totalCPUPercent float64, hasCPU bool) int32 {
	if proj.MaxInstanceCount <= 0 {
		return 0
	}
	floor := nonZeroFloor(proj)
	httpTarget := int32(math.Ceil(avgRequestsPerMinute / httpScaleUpRequestsPerMinute))
	cpuTarget := int32(0)
	if hasCPU {
		cpuTarget = int32(math.Ceil(totalCPUPercent / cpuScaleUpPercentTotal))
	}
	target := floor
	if httpTarget > target {
		target = httpTarget
	}
	if cpuTarget > target {
		target = cpuTarget
	}
	if target > proj.MaxInstanceCount {
		target = proj.MaxInstanceCount
	}
	return target
}

func shouldStepDown(current int32, avgRequestsPerMinute, totalCPUPercent float64, hasCPU bool) bool {
	if current <= 1 {
		return false
	}
	httpOK := avgRequestsPerMinute < float64(current-1)*httpScaleDownRequestsPerMinute
	cpuOK := true
	if hasCPU {
		cpuOK = totalCPUPercent < float64(current-1)*cpuScaleDownPercentTotal
	}
	return httpOK && cpuOK
}

func shouldHoldScaleDown(proj queries.Project, now time.Time) bool {
	return proj.LastRequestAt.Valid && now.UTC().Sub(proj.LastRequestAt.Time.UTC()) < scaleDownCooldown
}

func ComputeDesiredInstanceCount(proj queries.Project, service *queries.Service, avgRequestsPerMinute, totalCPUPercent float64, hasCPU bool) int32 {
	current := clampCurrentTarget(proj, service)
	if proj.MaxInstanceCount <= 0 {
		return 0
	}
	floor := nonZeroFloor(proj)
	upTarget := targetFromMetrics(proj, avgRequestsPerMinute, totalCPUPercent, hasCPU)
	if upTarget > current {
		return upTarget
	}
	if current < floor {
		return floor
	}
	if current > floor && shouldStepDown(current, avgRequestsPerMinute, totalCPUPercent, hasCPU) {
		next := current - 1
		if next < floor {
			next = floor
		}
		return next
	}
	return current
}

func scheduleDeploymentNow(deploymentReconciler *reconciler.Scheduler, dep queries.Deployment) {
	if deploymentReconciler == nil || !dep.ID.Valid {
		return
	}
	deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
}

func runAutoscaleSweep(ctx context.Context, q autoscaleStore, deploymentReconciler *reconciler.Scheduler, now time.Time) {
	if q == nil {
		return
	}
	now = now.UTC()
	httpWindowEnd := now.Truncate(time.Minute)
	httpWindowStart := httpWindowEnd.Add(-httpWindowMinutes * time.Minute)
	httpWindowInclusiveEnd := httpWindowEnd.Add(-time.Nanosecond)
	cpuSince := now.Add(-cpuLookback)

	projects, err := q.ProjectFindAll(ctx)
	if err != nil {
		slog.Warn("project autoscaler: list projects", "error", err)
		return
	}

	for _, proj := range projects {
		if proj.MaxInstanceCount <= 0 || proj.ScaledToZero {
			continue
		}
		services, err := q.ServiceListByProjectID(ctx, proj.ID)
		if err != nil {
			slog.Warn("project autoscaler: list services", "project_id", proj.ID, "error", err)
			continue
		}
		if len(services) == 0 {
			continue
		}
		for i := range services {
			svc := &services[i]
			dep, err := q.DeploymentLatestRunningByServiceID(ctx, svc.ID)
			if err != nil {
				continue
			}

			httpRows, err := q.ProjectHTTPUsageRollupsAggregatedByDeployment(ctx, queries.ProjectHTTPUsageRollupsAggregatedByDeploymentParams{
				ProjectID:     proj.ID,
				DeploymentID:  dep.ID,
				BucketStart:   pgtype.Timestamptz{Time: httpWindowStart, Valid: true},
				BucketStart_2: pgtype.Timestamptz{Time: httpWindowInclusiveEnd, Valid: true},
			})
			if err != nil {
				slog.Warn("project autoscaler: http usage", "project_id", proj.ID, "service_id", svc.ID, "error", err)
				continue
			}
			var requestCount int64
			for _, row := range httpRows {
				requestCount += row.RequestCount
			}
			avgRPM := float64(requestCount) / float64(httpWindowMinutes)

			cpuRows, err := q.InstanceUsageLatestPerInstanceByDeployment(ctx, queries.InstanceUsageLatestPerInstanceByDeploymentParams{
				ProjectID: proj.ID,
				ID:        dep.ID,
				SampledAt: pgtype.Timestamptz{Time: cpuSince, Valid: true},
			})
			if err != nil {
				slog.Warn("project autoscaler: cpu usage", "project_id", proj.ID, "service_id", svc.ID, "error", err)
				continue
			}
			var totalCPU float64
			hasCPU := false
			for _, row := range cpuRows {
				if row.CpuPercent.Valid {
					totalCPU += row.CpuPercent.Float64
					hasCPU = true
				}
			}
			if shouldSkipAutoscale(svc, avgRPM, hasCPU) {
				continue
			}

			current := clampCurrentTarget(proj, svc)
			next := ComputeDesiredInstanceCount(proj, svc, avgRPM, totalCPU, hasCPU)
			if next < current && shouldHoldScaleDown(proj, now) {
				continue
			}
			if next == current {
				continue
			}

			if err := q.ServiceSetDesiredInstanceCount(ctx, queries.ServiceSetDesiredInstanceCountParams{
				ID:                   svc.ID,
				DesiredInstanceCount: next,
			}); err != nil {
				slog.Warn("project autoscaler: update service target", "project_id", proj.ID, "service_id", svc.ID, "error", err)
				continue
			}
			scheduleDeploymentNow(deploymentReconciler, dep)
			slog.Info(
				"service autoscaled",
				"project_id", proj.ID,
				"service_id", svc.ID,
				"deployment_id", dep.ID,
				"from", current,
				"to", next,
				"avg_rpm", avgRPM,
				"total_cpu_percent", totalCPU,
			)
		}
	}
}

func RunOnce(ctx context.Context, databaseURL string, deploymentReconciler *reconciler.Scheduler) {
	leaderConn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		slog.Debug("project autoscaler: db connect", "error", err)
		return
	}
	defer leaderConn.Close(context.Background())

	qLeader := queries.New(leaderConn)
	acquired, err := qLeader.TrySessionAdvisoryLock(ctx, "kindling_project_autoscaler")
	if err != nil {
		slog.Warn("project autoscaler: advisory lock", "error", err)
		return
	}
	if !acquired {
		return
	}

	runAutoscaleSweep(ctx, qLeader, deploymentReconciler, time.Now())
}
