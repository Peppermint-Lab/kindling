// Package audit provides helpers for durable cluster-global audit events.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// Action names for cluster_audit_events.action (stable contract; see docs/cluster-audit-events.md).
const (
	ActionServerDrain           = "server.drain"
	ActionServerActivate        = "server.activate"
	ActionClusterSettingsUpdate = "cluster.settings.update"
	ActionAuthProviderUpdate    = "auth.provider.update"
)

// RecordClusterEvent persists a platform-admin audit row. Failures are logged and do not return errors
// to callers (primary operation already succeeded).
func RecordClusterEvent(ctx context.Context, q *queries.Queries, userID uuid.UUID, r *http.Request, action, resourceType, resourceID string, details map[string]any) {
	if q == nil {
		return
	}
	var body []byte
	if len(details) > 0 {
		var err error
		body, err = json.Marshal(details)
		if err != nil {
			slog.Warn("cluster audit: marshal details", "action", action, "err", err)
			body = []byte(`{}`)
		}
	}
	var uid pgtype.UUID
	if userID != uuid.Nil {
		uid = pgtype.UUID{Bytes: userID, Valid: true}
	}
	ip := ""
	ua := ""
	if r != nil {
		ip = strings.TrimSpace(r.RemoteAddr)
		if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
			ip = fwd
		}
		ua = strings.TrimSpace(r.Header.Get("User-Agent"))
	}
	if err := q.ClusterAuditEventCreate(ctx, queries.ClusterAuditEventCreateParams{
		UserID:       uid,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		RequestIp:    ip,
		UserAgent:    ua,
		Details:      body,
	}); err != nil {
		slog.Warn("cluster audit: insert failed", "action", action, "err", err)
	}
}
