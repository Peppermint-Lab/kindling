package usage

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

const (
	TrafficKindAppEdge         = "app_edge"
	TrafficKindControlPlaneAPI = "control_plane_api"
)

// IncrementServerHTTPUsageRollup records server-level traffic into the current minute bucket.
func IncrementServerHTTPUsageRollup(
	ctx context.Context,
	q *queries.Queries,
	serverID pgtype.UUID,
	trafficKind string,
	statusCode int,
	bytesIn, bytesOut int64,
) {
	if !serverID.Valid {
		return
	}
	var n2, n4, n5 int64
	switch {
	case statusCode >= 200 && statusCode < 400:
		n2 = 1
	case statusCode >= 400 && statusCode < 500:
		n4 = 1
	default:
		n5 = 1
	}
	bucket := time.Now().UTC().Truncate(time.Minute)
	if err := q.ServerHTTPUsageRollupIncrement(ctx, queries.ServerHTTPUsageRollupIncrementParams{
		ServerID:     serverID,
		TrafficKind:  trafficKind,
		BucketStart:  pgtype.Timestamptz{Time: bucket, Valid: true},
		RequestCount: 1,
		Status2xx:    n2,
		Status4xx:    n4,
		Status5xx:    n5,
		BytesIn:      bytesIn,
		BytesOut:     bytesOut,
	}); err != nil && ctx.Err() == nil {
		slog.Warn("ServerHTTPUsageRollupIncrement", "traffic_kind", trafficKind, "error", err)
	}
}
