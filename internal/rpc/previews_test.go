package rpc

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestPreviewLifecycleState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC)

	if got := previewLifecycleState(queries.PreviewEnvironment{}, now); got != "active" {
		t.Fatalf("active preview state = %q", got)
	}

	closed := queries.PreviewEnvironment{
		ClosedAt:  pgtype.Timestamptz{Time: now.Add(-5 * time.Minute), Valid: true},
		ExpiresAt: pgtype.Timestamptz{Time: now.Add(10 * time.Minute), Valid: true},
	}
	if got := previewLifecycleState(closed, now); got != "closed" {
		t.Fatalf("closed preview state = %q", got)
	}

	cleanupDue := queries.PreviewEnvironment{
		ClosedAt:  pgtype.Timestamptz{Time: now.Add(-30 * time.Minute), Valid: true},
		ExpiresAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
	}
	if got := previewLifecycleState(cleanupDue, now); got != "cleanup_due" {
		t.Fatalf("cleanup-due preview state = %q", got)
	}
}
