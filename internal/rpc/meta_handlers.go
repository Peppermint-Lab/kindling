package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/audit"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func (a *API) getMeta(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requirePlatformAdmin(w, p) {
		return
	}
	base, err := a.publicBaseURL(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	dash, err := a.dashboardPublicHost(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	out := map[string]any{
		"public_base_url":            base,
		"public_base_url_configured": base != "",
		"webhook_path":               "/webhooks/github",
	}
	if dash != "" {
		out["dashboard_public_host"] = dash
	}
	mergePreviewMeta(r.Context(), a.q, out)
	writeJSON(w, http.StatusOK, out)
}

func (a *API) putMeta(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requirePlatformAdmin(w, p) {
		return
	}
	var req struct {
		PublicBaseURL                     *string `json:"public_base_url"`
		DashboardPublicHost               *string `json:"dashboard_public_host"`
		ServiceBaseDomain                 *string `json:"service_base_domain"`
		PreviewBaseDomain                 *string `json:"preview_base_domain"`
		PreviewRetentionAfterCloseSeconds *int64  `json:"preview_retention_after_close_seconds"`
		PreviewIdleScaleSeconds           *int64  `json:"preview_idle_scale_seconds"`
		ScaleToZeroIdleSeconds            *int64  `json:"scale_to_zero_idle_seconds"`
		ColdStartTimeoutSeconds           *int64  `json:"cold_start_timeout_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	ctx := r.Context()
	changed := make([]string, 0, 8)
	if req.PublicBaseURL != nil {
		before, err := a.publicBaseURL(ctx)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		after := NormalizePublicBaseURL(*req.PublicBaseURL)
		if err := a.clusterSettingUpsertPublicBaseURL(ctx, *req.PublicBaseURL); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		changed = appendChangedSetting(changed, "public_base_url", before, after)
	}
	if req.DashboardPublicHost != nil {
		before, err := a.dashboardPublicHost(ctx)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		after := NormalizeDashboardPublicHost(*req.DashboardPublicHost)
		if err := a.clusterSettingUpsertDashboardPublicHost(ctx, *req.DashboardPublicHost); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		changed = appendChangedSetting(changed, "dashboard_public_host", before, after)
	}
	if req.ServiceBaseDomain != nil {
		before, err := clusterSettingValue(ctx, a.q, config.SettingServiceBaseDomain)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		after := strings.TrimSpace(*req.ServiceBaseDomain)
		if err := a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
			Key:   config.SettingServiceBaseDomain,
			Value: after,
		}); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		changed = appendChangedSetting(changed, "service_base_domain", before, after)
	}
	if req.PreviewBaseDomain != nil {
		before, err := clusterSettingValue(ctx, a.q, config.SettingPreviewBaseDomain)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		after := strings.TrimSpace(*req.PreviewBaseDomain)
		if err := a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
			Key:   config.SettingPreviewBaseDomain,
			Value: after,
		}); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		changed = appendChangedSetting(changed, "preview_base_domain", before, after)
	}
	if req.PreviewRetentionAfterCloseSeconds != nil {
		before, err := clusterSettingValue(ctx, a.q, config.SettingPreviewRetentionAfterCloseSecs)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		after := strconv.FormatInt(*req.PreviewRetentionAfterCloseSeconds, 10)
		if err := a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
			Key:   config.SettingPreviewRetentionAfterCloseSecs,
			Value: after,
		}); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		changed = appendChangedSetting(changed, "preview_retention_after_close_seconds", before, after)
	}
	if req.PreviewIdleScaleSeconds != nil {
		before, err := clusterSettingValue(ctx, a.q, config.SettingPreviewIdleSeconds)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		after := strconv.FormatInt(*req.PreviewIdleScaleSeconds, 10)
		if err := a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
			Key:   config.SettingPreviewIdleSeconds,
			Value: after,
		}); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		changed = appendChangedSetting(changed, "preview_idle_scale_seconds", before, after)
	}
	if req.ScaleToZeroIdleSeconds != nil {
		before, err := clusterSettingValue(ctx, a.q, config.SettingScaleToZeroIdleSeconds)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		after := strconv.FormatInt(*req.ScaleToZeroIdleSeconds, 10)
		if err := a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
			Key:   config.SettingScaleToZeroIdleSeconds,
			Value: after,
		}); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		changed = appendChangedSetting(changed, "scale_to_zero_idle_seconds", before, after)
	}
	if req.ColdStartTimeoutSeconds != nil {
		before, err := clusterSettingValue(ctx, a.q, config.SettingColdStartTimeout)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		after := (time.Duration(*req.ColdStartTimeoutSeconds) * time.Second).String()
		if err := a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
			Key:   config.SettingColdStartTimeout,
			Value: after,
		}); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
		changed = appendChangedSetting(changed, "cold_start_timeout_seconds", before, after)
	}
	base, err := a.publicBaseURL(ctx)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	dash, err := a.dashboardPublicHost(ctx)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	out := map[string]any{
		"public_base_url":            base,
		"public_base_url_configured": base != "",
		"webhook_path":               "/webhooks/github",
	}
	if dash != "" {
		out["dashboard_public_host"] = dash
	}
	mergePreviewMeta(ctx, a.q, out)
	if len(changed) > 0 {
		audit.RecordClusterEvent(ctx, a.q, p.UserID, r, audit.ActionClusterSettingsUpdate, "cluster", "", map[string]any{
			"changed": changed,
		})
	}

	writeJSON(w, http.StatusOK, out)
}

func mergePreviewMeta(ctx context.Context, q *queries.Queries, out map[string]any) {
	sb := ""
	if v, err := q.ClusterSettingGet(ctx, config.SettingServiceBaseDomain); err == nil {
		sb = strings.TrimSpace(v)
	}
	out["service_base_domain"] = sb

	pb := ""
	if v, err := q.ClusterSettingGet(ctx, config.SettingPreviewBaseDomain); err == nil {
		pb = strings.TrimSpace(v)
	}
	out["preview_base_domain"] = pb
	ret := int64(3600)
	if v, err := q.ClusterSettingGet(ctx, config.SettingPreviewRetentionAfterCloseSecs); err == nil {
		v = strings.TrimSpace(v)
		if v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				ret = n
			}
		}
	}
	out["preview_retention_after_close_seconds"] = ret
	idle := int64(300)
	if v, err := q.ClusterSettingGet(ctx, config.SettingPreviewIdleSeconds); err == nil {
		v = strings.TrimSpace(v)
		if v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				idle = n
			}
		}
	}
	out["preview_idle_scale_seconds"] = idle

	productionIdle := int64(300)
	if v, err := q.ClusterSettingGet(ctx, config.SettingScaleToZeroIdleSeconds); err == nil {
		v = strings.TrimSpace(v)
		if v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				productionIdle = n
			}
		}
	}
	out["scale_to_zero_idle_seconds"] = productionIdle

	coldStartSeconds := int64((2 * time.Minute) / time.Second)
	if v, err := q.ClusterSettingGet(ctx, config.SettingColdStartTimeout); err == nil {
		v = strings.TrimSpace(v)
		if v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				coldStartSeconds = int64(d / time.Second)
			}
		}
	}
	out["cold_start_timeout_seconds"] = coldStartSeconds
}

func clusterSettingValue(ctx context.Context, q *queries.Queries, key string) (string, error) {
	v, err := q.ClusterSettingGet(ctx, key)
	if err == nil {
		return strings.TrimSpace(v), nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return "", err
}

func appendChangedSetting(changed []string, label, before, after string) []string {
	if before != after {
		return append(changed, label)
	}
	return changed
}
