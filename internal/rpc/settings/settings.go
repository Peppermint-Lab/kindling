// Package settings provides cluster settings handlers for the RPC layer.
package settings

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// ClusterSettingKeyPublicBaseURL is the cluster_settings key for the public API base URL.
const ClusterSettingKeyPublicBaseURL = "public_base_url"

// ClusterSettingKeyDashboardPublicHost is the hostname (no scheme) for the dashboard SPA
// (e.g. app.example.com). TLS and HTTP routing use this; API/webhooks use public_base_url.
const ClusterSettingKeyDashboardPublicHost = "dashboard_public_host"

// Handler provides cluster-settings helpers for route handlers.
type Handler struct {
	Q *queries.Queries
}

// NormalizePublicBaseURL trims whitespace and trailing slashes for stored URLs.
func NormalizePublicBaseURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "/")
	return s
}

// PublicBaseURL reads and normalises the stored base URL.
func (h *Handler) PublicBaseURL(ctx context.Context) (string, error) {
	v, err := h.Q.ClusterSettingGet(ctx, ClusterSettingKeyPublicBaseURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return NormalizePublicBaseURL(v), nil
}

// UpsertPublicBaseURL stores a normalised base URL.
func (h *Handler) UpsertPublicBaseURL(ctx context.Context, raw string) error {
	return h.Q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
		Key:   ClusterSettingKeyPublicBaseURL,
		Value: NormalizePublicBaseURL(raw),
	})
}

// NormalizeDashboardPublicHost returns a lowercase hostname, or empty if unset/invalid.
// Accepts bare hostnames or full URLs.
func NormalizeDashboardPublicHost(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return ""
		}
		s = u.Hostname()
	}
	return strings.ToLower(strings.TrimSuffix(s, "."))
}

// DashboardPublicHost reads and normalises the stored dashboard host.
func (h *Handler) DashboardPublicHost(ctx context.Context) (string, error) {
	v, err := h.Q.ClusterSettingGet(ctx, ClusterSettingKeyDashboardPublicHost)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return NormalizeDashboardPublicHost(v), nil
}

// UpsertDashboardPublicHost stores a normalised dashboard host.
func (h *Handler) UpsertDashboardPublicHost(ctx context.Context, raw string) error {
	n := NormalizeDashboardPublicHost(raw)
	if n == "" {
		// Clearing: delete would require a new query; store empty string as unset signal.
		return h.Q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
			Key:   ClusterSettingKeyDashboardPublicHost,
			Value: "",
		})
	}
	return h.Q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
		Key:   ClusterSettingKeyDashboardPublicHost,
		Value: n,
	})
}
