package rpc

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// Usage dashboard duration constants.
const usageWindow6h = 6 * time.Hour          // 6-hour usage window
const usageWindow7d = 7 * 24 * time.Hour     // 7-day usage window
const usageWindow24h = 24 * time.Hour         // 24-hour usage window (default)
const usageCurrentLookback = 2 * time.Hour    // how far back to query for "current" resource usage
const usageHTTPRecentWindow = 15 * time.Minute // recent HTTP usage aggregation window

func (a *API) registerUsageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{id}/usage/current", a.getProjectUsageCurrent)
	mux.HandleFunc("GET /api/projects/{id}/usage/history", a.getProjectUsageHistory)
}

func parseUsageWindow(s string) time.Duration {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "1h":
		return time.Hour
	case "6h":
		return usageWindow6h
	case "7d", "168h":
		return usageWindow7d
	case "24h", "1d", "":
		return usageWindow24h
	default:
		return usageWindow24h
	}
}

func (a *API) getProjectUsageCurrent(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	ctx := r.Context()
	since := time.Now().UTC().Add(-usageCurrentLookback)
	latest, err := a.q.InstanceUsageLatestPerInstance(ctx, queries.InstanceUsageLatestPerInstanceParams{
		ProjectID: id,
		SampledAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "usage", err)
		return
	}

	type instOut struct {
		DeploymentInstanceID string   `json:"deployment_instance_id"`
		SampledAt            string   `json:"sampled_at,omitempty"`
		CPUPercent           *float64 `json:"cpu_percent,omitempty"`
		MemoryRssBytes       int64    `json:"memory_rss_bytes"`
		DiskReadBytes        int64    `json:"disk_read_bytes"`
		DiskWriteBytes       int64    `json:"disk_write_bytes"`
		Source               string   `json:"source"`
	}
	// Non-nil slice so JSON is [] not null (dashboard expects an array).
	instances := make([]instOut, 0)
	var memTotal int64
	var cpuSum float64
	var cpuN int
	for _, row := range latest {
		if !row.DeploymentInstanceID.Valid {
			continue
		}
		u := uuid.UUID(row.DeploymentInstanceID.Bytes).String()
		var cpuPtr *float64
		if row.CpuPercent.Valid {
			v := row.CpuPercent.Float64
			cpuPtr = &v
			cpuSum += v
			cpuN++
		}
		memTotal += row.MemoryRssBytes
		var ts string
		if row.SampledAt.Valid {
			ts = row.SampledAt.Time.UTC().Format(time.RFC3339)
		}
		instances = append(instances, instOut{
			DeploymentInstanceID: u,
			SampledAt:            ts,
			CPUPercent:           cpuPtr,
			MemoryRssBytes:       row.MemoryRssBytes,
			DiskReadBytes:        row.DiskReadBytes,
			DiskWriteBytes:       row.DiskWriteBytes,
			Source:               row.Source,
		})
	}
	var cpuAvg *float64
	if cpuN > 0 {
		v := cpuSum / float64(cpuN)
		cpuAvg = &v
	}

	httpFrom := time.Now().UTC().Add(-usageHTTPRecentWindow)
	httpTo := time.Now().UTC()
	httpRows, err := a.q.ProjectHTTPUsageRollupsAggregated(ctx, queries.ProjectHTTPUsageRollupsAggregatedParams{
		ProjectID:     id,
		BucketStart:   pgtype.Timestamptz{Time: httpFrom, Valid: true},
		BucketStart_2: pgtype.Timestamptz{Time: httpTo, Valid: true},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "usage_http", err)
		return
	}
	var httpReq, http2, http4, http5, httpIn, httpOut int64
	for _, h := range httpRows {
		httpReq += h.RequestCount
		http2 += h.Status2xx
		http4 += h.Status4xx
		http5 += h.Status5xx
		httpIn += h.BytesIn
		httpOut += h.BytesOut
	}

	out := map[string]any{
		"instances": instances,
		"summary": map[string]any{
			"memory_rss_bytes_total": memTotal,
			"cpu_percent_avg":        cpuAvg,
			"http_requests_15m":      httpReq,
			"http_status_2xx_15m":    http2,
			"http_status_4xx_15m":    http4,
			"http_status_5xx_15m":    http5,
			"http_bytes_in_15m":      httpIn,
			"http_bytes_out_15m":     httpOut,
		},
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getProjectUsageHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	win := parseUsageWindow(r.URL.Query().Get("window"))
	to := time.Now().UTC()
	from := to.Add(-win)
	ctx := r.Context()

	resRows, err := a.q.InstanceUsageSamplesByProjectWindow(ctx, queries.InstanceUsageSamplesByProjectWindowParams{
		ProjectID:   id,
		SampledAt:   pgtype.Timestamptz{Time: from, Valid: true},
		SampledAt_2: pgtype.Timestamptz{Time: to, Valid: true},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "usage_history", err)
		return
	}
	httpRows, err := a.q.ProjectHTTPUsageRollupsAggregated(ctx, queries.ProjectHTTPUsageRollupsAggregatedParams{
		ProjectID:     id,
		BucketStart:   pgtype.Timestamptz{Time: from, Valid: true},
		BucketStart_2: pgtype.Timestamptz{Time: to, Valid: true},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "usage_history_http", err)
		return
	}

	type resPt struct {
		BucketStart       string  `json:"bucket_start"`
		MemoryRssBytesMax int64   `json:"memory_rss_bytes_max"`
		CpuPercentAvg     float64 `json:"cpu_percent_avg"`
	}
	resource := make([]resPt, 0)
	for _, row := range resRows {
		var bs string
		if row.BucketStart.Valid {
			bs = row.BucketStart.Time.UTC().Format(time.RFC3339)
		}
		resource = append(resource, resPt{
			BucketStart:       bs,
			MemoryRssBytesMax: row.MemoryRssBytesMax,
			CpuPercentAvg:     row.CpuPercentAvg,
		})
	}

	type httpPt struct {
		BucketStart  string `json:"bucket_start"`
		RequestCount int64  `json:"request_count"`
		Status2xx    int64  `json:"status_2xx"`
		Status4xx    int64  `json:"status_4xx"`
		Status5xx    int64  `json:"status_5xx"`
		BytesIn      int64  `json:"bytes_in"`
		BytesOut     int64  `json:"bytes_out"`
	}
	httpSeries := make([]httpPt, 0)
	for _, row := range httpRows {
		var bs string
		if row.BucketStart.Valid {
			bs = row.BucketStart.Time.UTC().Format(time.RFC3339)
		}
		httpSeries = append(httpSeries, httpPt{
			BucketStart:  bs,
			RequestCount: row.RequestCount,
			Status2xx:    row.Status2xx,
			Status4xx:    row.Status4xx,
			Status5xx:    row.Status5xx,
			BytesIn:      row.BytesIn,
			BytesOut:     row.BytesOut,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"window":   win.String(),
		"resource": resource,
		"http":     httpSeries,
	})
}
