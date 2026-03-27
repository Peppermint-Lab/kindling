// Package serverreconcile implements reconciliation for server rows (drain / evacuate coordination).
package serverreconcile

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
)

// Handler coordinates server state with deployment reconciliation.
type Handler struct {
	q           *queries.Queries
	deployments *reconciler.Scheduler
	notifyRoute func()
}

// NewHandler returns a server reconcile handler.
func NewHandler(q *queries.Queries, deployments *reconciler.Scheduler, notifyRoute func()) *Handler {
	return &Handler{q: q, deployments: deployments, notifyRoute: notifyRoute}
}

// Reconcile reacts to server row changes. When a server is draining, it schedules
// every deployment that still has instances on that host and marks the server
// drained once no deployment_instances reference it.
func (h *Handler) Reconcile(ctx context.Context, serverID uuid.UUID) error {
	sid := pgtype.UUID{Bytes: serverID, Valid: true}

	srv, err := h.q.ServerFindByID(ctx, sid)
	if err != nil {
		return fmt.Errorf("server find: %w", err)
	}

	switch srv.Status {
	case "draining":
		depIDs, err := h.q.DeploymentIDsForInstancesOnServer(ctx, sid)
		if err != nil {
			return fmt.Errorf("deployments on server: %w", err)
		}
		for _, did := range depIDs {
			if !did.Valid {
				continue
			}
			h.deployments.ScheduleNow(uuid.UUID(did.Bytes))
		}
		n, err := h.q.DeploymentInstanceCountByServerID(ctx, sid)
		if err != nil {
			return fmt.Errorf("instance count on server: %w", err)
		}
		migrations, err := h.q.InstanceMigrationCountActiveByServerID(ctx, sid)
		if err != nil {
			return fmt.Errorf("active migrations on server: %w", err)
		}
		if n == 0 && migrations == 0 {
			if err := h.q.ServerSetDrained(ctx, sid); err != nil {
				return fmt.Errorf("server set drained: %w", err)
			}
			slog.Info("server drain complete", "server_id", serverID)
		}
	}

	if h.notifyRoute != nil {
		h.notifyRoute()
	}
	return nil
}
