package rpc

import (
	"context"

	"github.com/kindlingvm/kindling/internal/rpc/settings"
)

// Re-export constants for backward compatibility with existing importers.
const ClusterSettingKeyPublicBaseURL = settings.ClusterSettingKeyPublicBaseURL
const ClusterSettingKeyDashboardPublicHost = settings.ClusterSettingKeyDashboardPublicHost

// NormalizePublicBaseURL delegates to the settings sub-package.
func NormalizePublicBaseURL(s string) string { return settings.NormalizePublicBaseURL(s) }

// NormalizeDashboardPublicHost delegates to the settings sub-package.
func NormalizeDashboardPublicHost(s string) string { return settings.NormalizeDashboardPublicHost(s) }

func (a *API) settingsHandler() *settings.Handler { return &settings.Handler{Q: a.q} }

func (a *API) publicBaseURL(ctx context.Context) (string, error) {
	return a.settingsHandler().PublicBaseURL(ctx)
}

func (a *API) clusterSettingUpsertPublicBaseURL(ctx context.Context, raw string) error {
	return a.settingsHandler().UpsertPublicBaseURL(ctx, raw)
}

func (a *API) dashboardPublicHost(ctx context.Context) (string, error) {
	return a.settingsHandler().DashboardPublicHost(ctx)
}

func (a *API) clusterSettingUpsertDashboardPublicHost(ctx context.Context, raw string) error {
	return a.settingsHandler().UpsertDashboardPublicHost(ctx, raw)
}
