package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

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
		PreviewBaseDomain                 *string `json:"preview_base_domain"`
		PreviewRetentionAfterCloseSeconds *int64  `json:"preview_retention_after_close_seconds"`
		PreviewIdleScaleSeconds           *int64  `json:"preview_idle_scale_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if req.PublicBaseURL != nil {
		if err := a.clusterSettingUpsertPublicBaseURL(r.Context(), *req.PublicBaseURL); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
	}
	if req.DashboardPublicHost != nil {
		if err := a.clusterSettingUpsertDashboardPublicHost(r.Context(), *req.DashboardPublicHost); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
	}
	if req.PreviewBaseDomain != nil {
		if err := a.q.ClusterSettingUpsert(r.Context(), queries.ClusterSettingUpsertParams{
			Key:   config.SettingPreviewBaseDomain,
			Value: strings.TrimSpace(*req.PreviewBaseDomain),
		}); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
	}
	if req.PreviewRetentionAfterCloseSeconds != nil {
		if err := a.q.ClusterSettingUpsert(r.Context(), queries.ClusterSettingUpsertParams{
			Key:   config.SettingPreviewRetentionAfterCloseSecs,
			Value: strconv.FormatInt(*req.PreviewRetentionAfterCloseSeconds, 10),
		}); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
	}
	if req.PreviewIdleScaleSeconds != nil {
		if err := a.q.ClusterSettingUpsert(r.Context(), queries.ClusterSettingUpsertParams{
			Key:   config.SettingPreviewIdleSeconds,
			Value: strconv.FormatInt(*req.PreviewIdleScaleSeconds, 10),
		}); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
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

func mergePreviewMeta(ctx context.Context, q *queries.Queries, out map[string]any) {
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
}
