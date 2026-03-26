package rpc

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

// NormalizePublicBaseURL trims whitespace and trailing slashes for stored URLs.
func NormalizePublicBaseURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "/")
	return s
}

func (a *API) publicBaseURL(ctx context.Context) (string, error) {
	v, err := a.q.ClusterSettingGet(ctx, ClusterSettingKeyPublicBaseURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return NormalizePublicBaseURL(v), nil
}

func (a *API) clusterSettingUpsertPublicBaseURL(ctx context.Context, raw string) error {
	return a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
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

func (a *API) dashboardPublicHost(ctx context.Context) (string, error) {
	v, err := a.q.ClusterSettingGet(ctx, ClusterSettingKeyDashboardPublicHost)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return NormalizeDashboardPublicHost(v), nil
}

func (a *API) clusterSettingUpsertDashboardPublicHost(ctx context.Context, raw string) error {
	n := NormalizeDashboardPublicHost(raw)
	if n == "" {
		// Clearing: delete would require a new query; store empty string as unset signal.
		return a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
			Key:   ClusterSettingKeyDashboardPublicHost,
			Value: "",
		})
	}
	return a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
		Key:   ClusterSettingKeyDashboardPublicHost,
		Value: n,
	})
}
