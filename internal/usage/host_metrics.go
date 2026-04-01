// Package usage collects workload and host resource samples into Postgres.
package usage

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

const defaultHostMetricsPollerInterval = 10 * time.Second

type HostMetricsSample struct {
	SampledAt            time.Time
	CPUPercent           float64
	LoadAvg1m            float64
	LoadAvg5m            float64
	LoadAvg15m           float64
	MemoryTotalBytes     int64
	MemoryAvailableBytes int64
	MemoryUsedBytes      int64
	DiskTotalBytes       int64
	DiskFreeBytes        int64
	DiskUsedBytes        int64
	DiskReadBytesPerSec  float64
	DiskWriteBytesPerSec float64
	StateDiskPath        string
	StateDiskTotalBytes  int64
	StateDiskFreeBytes   int64
	StateDiskUsedBytes   int64
}

// RunHostMetricsPoller samples host-level control-plane metrics for this server.
func RunHostMetricsPoller(
	ctx context.Context,
	q *queries.Queries,
	serverID uuid.UUID,
	every time.Duration,
	stateDirFn func() string,
) {
	if !HostMetricsSupported() {
		return
	}
	if every <= 0 {
		every = defaultHostMetricsPollerInterval
	}

	tick := time.NewTicker(every)
	defer tick.Stop()

	var prev *hostMetricsCollectorState
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			stateDir := ""
			if stateDirFn != nil {
				stateDir = strings.TrimSpace(stateDirFn())
			}
			sample, nextPrev, err := collectHostMetrics(prev, stateDir)
			if err != nil {
				if ctx.Err() == nil {
					slog.Debug("host metrics poll", "error", err)
				}
				continue
			}
			prev = &nextPrev
			if err := q.ServerHostMetricsUpsert(ctx, queries.ServerHostMetricsUpsertParams{
				ServerID:             pgtype.UUID{Bytes: serverID, Valid: true},
				SampledAt:            pgtype.Timestamptz{Time: sample.SampledAt.UTC(), Valid: true},
				CpuPercent:           sample.CPUPercent,
				LoadAvg1m:            sample.LoadAvg1m,
				LoadAvg5m:            sample.LoadAvg5m,
				LoadAvg15m:           sample.LoadAvg15m,
				MemoryTotalBytes:     sample.MemoryTotalBytes,
				MemoryAvailableBytes: sample.MemoryAvailableBytes,
				MemoryUsedBytes:      sample.MemoryUsedBytes,
				DiskTotalBytes:       sample.DiskTotalBytes,
				DiskFreeBytes:        sample.DiskFreeBytes,
				DiskUsedBytes:        sample.DiskUsedBytes,
				DiskReadBytesPerSec:  sample.DiskReadBytesPerSec,
				DiskWriteBytesPerSec: sample.DiskWriteBytesPerSec,
				StateDiskPath:        sample.StateDiskPath,
				StateDiskTotalBytes:  sample.StateDiskTotalBytes,
				StateDiskFreeBytes:   sample.StateDiskFreeBytes,
				StateDiskUsedBytes:   sample.StateDiskUsedBytes,
			}); err != nil && ctx.Err() == nil {
				slog.Warn("ServerHostMetricsUpsert", "error", err)
			}
		}
	}
}
