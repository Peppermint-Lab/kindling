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
)

func nonZeroFloor(proj queries.Project) int32 {
	if proj.MaxInstanceCount <= 0 {
		return 0
	}
	if proj.MinInstanceCount > 1 {
		return proj.MinInstanceCount
	}
	return 1
}

func clampCurrentTarget(proj queries.Project) int32 {
	if proj.MaxInstanceCount <= 0 {
		return 0
	}
	target := proj.DesiredInstanceCount
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

func ComputeDesiredInstanceCount(proj queries.Project, avgRequestsPerMinute, totalCPUPercent float64, hasCPU bool) int32 {
	current := clampCurrentTarget(proj)
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

func RunOnce(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler) {
	leaderConn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		slog.Debug("project autoscaler: db connect", "error", err)
		return
	}
	defer leaderConn.Close(context.Background())

	qLeader := queries.New(leaderConn)
	acquired, err := qLeader.TrySessionAdvisoryLock(ctx, "kindling_project_autoscaler")
	if err != nil || !acquired {
		return
	}

	projects, err := q.ProjectFindAll(ctx)
	if err != nil {
		slog.Warn("project autoscaler: list projects", "error", err)
		return
	}

	now := time.Now().UTC()
	httpWindowEnd := now.Truncate(time.Minute)
	httpWindowStart := httpWindowEnd.Add(-httpWindowMinutes * time.Minute)
	httpWindowInclusiveEnd := httpWindowEnd.Add(-time.Nanosecond)
	cpuSince := now.Add(-cpuLookback)

	for _, proj := range projects {
		if proj.MaxInstanceCount <= 0 || proj.ScaledToZero {
			continue
		}
		dep, err := q.DeploymentLatestRunningByProjectID(ctx, proj.ID)
		if err != nil {
			continue
		}
		if dep.WakeRequestedAt.Valid {
			continue
		}

		httpRows, err := q.ProjectHTTPUsageRollupsAggregated(ctx, queries.ProjectHTTPUsageRollupsAggregatedParams{
			ProjectID:     proj.ID,
			BucketStart:   pgtype.Timestamptz{Time: httpWindowStart, Valid: true},
			BucketStart_2: pgtype.Timestamptz{Time: httpWindowInclusiveEnd, Valid: true},
		})
		if err != nil {
			slog.Warn("project autoscaler: http usage", "project_id", proj.ID, "error", err)
			continue
		}
		var requestCount int64
		for _, row := range httpRows {
			requestCount += row.RequestCount
		}
		avgRPM := float64(requestCount) / float64(httpWindowMinutes)

		cpuRows, err := q.InstanceUsageLatestPerInstance(ctx, queries.InstanceUsageLatestPerInstanceParams{
			ProjectID: proj.ID,
			SampledAt: pgtype.Timestamptz{Time: cpuSince, Valid: true},
		})
		if err != nil {
			slog.Warn("project autoscaler: cpu usage", "project_id", proj.ID, "error", err)
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

		current := clampCurrentTarget(proj)
		next := ComputeDesiredInstanceCount(proj, avgRPM, totalCPU, hasCPU)
		if next == current {
			continue
		}

		if _, err := q.ProjectSetDesiredInstanceCount(ctx, queries.ProjectSetDesiredInstanceCountParams{
			ID:                   proj.ID,
			DesiredInstanceCount: next,
		}); err != nil {
			slog.Warn("project autoscaler: update target", "project_id", proj.ID, "error", err)
			continue
		}
		scheduleDeploymentNow(deploymentReconciler, dep)
		slog.Info(
			"project autoscaled",
			"project_id", proj.ID,
			"deployment_id", dep.ID,
			"from", current,
			"to", next,
			"avg_rpm", avgRPM,
			"total_cpu_percent", totalCPU,
		)
	}
}
