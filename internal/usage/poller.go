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

// RunResourcePoller samples ResourceStats from the local runtime for each running
// deployment instance on this server and writes instance_usage_samples rows.
func RunResourcePoller(ctx context.Context, q *queries.Queries, serverID uuid.UUID, rt runtime.Runtime, every time.Duration) {
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
			if err := pollOnce(ctx, q, serverID, rt); err != nil && ctx.Err() == nil {
				slog.Debug("usage poll", "error", err)
			}
		}
	}
}

func pollOnce(ctx context.Context, q *queries.Queries, serverID uuid.UUID, rt runtime.Runtime) error {
	srv := pgtype.UUID{Bytes: serverID, Valid: true}
	rows, err := q.DeploymentInstancesRunningForUsageOnServer(ctx, srv)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, row := range rows {
		if !row.ID.Valid {
			continue
		}
		instID := uuid.UUID(row.ID.Bytes)
		stats, err := rt.ResourceStats(ctx, instID)
		if err != nil {
			if err == runtime.ErrInstanceNotRunning {
				continue
			}
			slog.Debug("ResourceStats", "instance_id", instID, "error", err)
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
		}
	}
	return nil
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
