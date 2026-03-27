// Package usage collects workload resource samples into Postgres.
package usage

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/runtime"
)

type PollerStatusReport struct {
	ObservedAt       time.Time
	LastSuccessAt    *time.Time
	LastErrorAt      *time.Time
	Status           string
	LastErrorMessage string
	Metadata         map[string]any
}

type PollerStatusReporter func(PollerStatusReport)

type pollResult struct {
	RunningInstances int
	SampledInstances int
	ErrorCount       int
	LastErrorMessage string
}

// RunResourcePoller samples ResourceStats from the local runtime for each running
// deployment instance on this server and writes instance_usage_samples rows.
func RunResourcePoller(ctx context.Context, q *queries.Queries, serverID uuid.UUID, rt runtime.Runtime, every time.Duration, report PollerStatusReporter) {
	if every <= 0 {
		every = 15 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now := time.Now().UTC()
			result, err := pollOnce(ctx, q, serverID, rt, now)
			if err != nil && ctx.Err() == nil {
				slog.Debug("usage poll", "error", err)
			}
			if report != nil && ctx.Err() == nil {
				meta := map[string]any{
					"runtime":           rt.Name(),
					"running_instances": result.RunningInstances,
					"sampled_instances": result.SampledInstances,
					"error_count":       result.ErrorCount,
				}
				rep := PollerStatusReport{
					ObservedAt: now,
					Status:     "healthy",
					Metadata:   meta,
				}
				if err != nil {
					rep.Status = "degraded"
					rep.LastErrorAt = &now
					rep.LastErrorMessage = err.Error()
				} else {
					rep.LastSuccessAt = &now
					if result.ErrorCount > 0 {
						rep.Status = "degraded"
						rep.LastErrorAt = &now
						rep.LastErrorMessage = result.LastErrorMessage
					}
				}
				report(rep)
			}
		}
	}
}

func pollOnce(ctx context.Context, q *queries.Queries, serverID uuid.UUID, rt runtime.Runtime, now time.Time) (pollResult, error) {
	result := pollResult{}
	srv := pgtype.UUID{Bytes: serverID, Valid: true}
	rows, err := q.DeploymentInstancesRunningForUsageOnServer(ctx, srv)
	if err != nil {
		return result, err
	}
	result.RunningInstances = len(rows)
	for _, row := range rows {
		if !row.ID.Valid {
			continue
		}
		instID := uuid.UUID(row.ID.Bytes)
		stats, err := rt.ResourceStats(ctx, instID)
		if err != nil {
			if err == runtime.ErrInstanceNotRunning {
				result.ErrorCount++
				if result.LastErrorMessage == "" {
					result.LastErrorMessage = err.Error()
				}
				continue
			}
			slog.Debug("ResourceStats", "instance_id", instID, "error", err)
			result.ErrorCount++
			if result.LastErrorMessage == "" {
				result.LastErrorMessage = err.Error()
			}
			continue
		}
		cpuPercent := computeCPUPercent(ctx, q, row.ID, now, stats)
		cp := pgtype.Float8{}
		if cpuPercent >= 0 {
			cp = pgtype.Float8{Float64: cpuPercent, Valid: true}
		}
		err = q.InstanceUsageSampleInsert(ctx, queries.InstanceUsageSampleInsertParams{
			ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
			ServerID:             srv,
			ProjectID:            row.ProjectID,
			DeploymentID:         row.DeploymentID,
			DeploymentInstanceID: row.ID,
			CpuNanosCumulative:   stats.CPUNanosCumulative,
			CpuPercent:           cp,
			MemoryRssBytes:       stats.MemoryRSSBytes,
			DiskReadBytes:        stats.DiskReadBytes,
			DiskWriteBytes:       stats.DiskWriteBytes,
			Source:               rt.Name(),
		})
		if err != nil {
			slog.Warn("InstanceUsageSampleInsert", "error", err)
			result.ErrorCount++
			if result.LastErrorMessage == "" {
				result.LastErrorMessage = err.Error()
			}
			continue
		}
		result.SampledInstances++
	}
	return result, nil
}

func computeCPUPercent(ctx context.Context, q *queries.Queries, deploymentInstanceID pgtype.UUID, sampledAt time.Time, stats runtime.ResourceStats) float64 {
	prev, err := q.InstanceUsageLastBefore(ctx, queries.InstanceUsageLastBeforeParams{
		DeploymentInstanceID: deploymentInstanceID,
		SampledAt:            pgtype.Timestamptz{Time: sampledAt, Valid: true},
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return -1
		}
		return -1
	}
	if !prev.SampledAt.Valid {
		return -1
	}
	dt := sampledAt.Sub(prev.SampledAt.Time)
	if dt <= 0 {
		return -1
	}
	dCPU := stats.CPUNanosCumulative - prev.CpuNanosCumulative
	if dCPU < 0 {
		return -1
	}
	// Fraction of one CPU core used over the interval (0..100+ for multi-core guests).
	pct := float64(dCPU) / float64(dt.Nanoseconds()) * 100
	// Cap display blow-ups from guest restarts / clock issues.
	if pct > 100*8 {
		pct = -1
	}
	return pct
}
