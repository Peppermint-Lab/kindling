// Package servers provides server management API handlers.
package servers

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/audit"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/rpc/rpcutil"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

const (
	serverHeartbeatExpected = 10 * time.Second
	componentHeartbeatEvery = 10 * time.Second
	usagePollerEvery        = 15 * time.Second
	hostMetricsEvery        = 10 * time.Second
	trafficRecentWindow     = 60 * time.Second
)

var serverComponentOrder = []string{"api", "edge", "worker", "usage_poller"}

// Handler provides server management API handlers.
type Handler struct {
	Q *queries.Queries
}

// RegisterRoutes mounts server management routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/platform/health/overview", h.getPlatformHealthOverview)
	mux.HandleFunc("GET /api/platform/servers", h.listPlatformServers)
	mux.HandleFunc("GET /api/platform/servers/{id}/details", h.getPlatformServerDetails)
	mux.HandleFunc("GET /api/servers", h.listServers)
	mux.HandleFunc("GET /api/servers/{id}/details", h.getServerDetails)
	mux.HandleFunc("POST /api/servers/{id}/drain", h.postServerDrain)
	mux.HandleFunc("POST /api/servers/{id}/activate", h.postServerActivate)
}

type serverComponentOut struct {
	Component        string         `json:"component"`
	Enabled          bool           `json:"enabled"`
	Health           string         `json:"health"`
	RawStatus        string         `json:"raw_status,omitempty"`
	ObservedAt       *string        `json:"observed_at,omitempty"`
	LastSuccessAt    *string        `json:"last_success_at,omitempty"`
	LastErrorAt      *string        `json:"last_error_at,omitempty"`
	LastErrorMessage string         `json:"last_error_message,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type serverHostMetricsOut struct {
	SampledAt            *string `json:"sampled_at,omitempty"`
	SampleAgeSeconds     *int64  `json:"sample_age_seconds,omitempty"`
	SampleHealth         string  `json:"sample_health"`
	CPUPercent           float64 `json:"cpu_percent"`
	LoadAvg1m            float64 `json:"load_avg_1m"`
	LoadAvg5m            float64 `json:"load_avg_5m"`
	LoadAvg15m           float64 `json:"load_avg_15m"`
	MemoryTotalBytes     int64   `json:"memory_total_bytes"`
	MemoryAvailableBytes int64   `json:"memory_available_bytes"`
	MemoryUsedBytes      int64   `json:"memory_used_bytes"`
	DiskTotalBytes       int64   `json:"disk_total_bytes"`
	DiskFreeBytes        int64   `json:"disk_free_bytes"`
	DiskUsedBytes        int64   `json:"disk_used_bytes"`
	DiskReadBytesPerSec  float64 `json:"disk_read_bytes_per_sec"`
	DiskWriteBytesPerSec float64 `json:"disk_write_bytes_per_sec"`
	StateDiskPath        string  `json:"state_disk_path,omitempty"`
	StateDiskTotalBytes  int64   `json:"state_disk_total_bytes"`
	StateDiskFreeBytes   int64   `json:"state_disk_free_bytes"`
	StateDiskUsedBytes   int64   `json:"state_disk_used_bytes"`
}

type serverTrafficOut struct {
	WindowSeconds              int64   `json:"window_seconds"`
	RequestCountRecent         int64   `json:"request_count_recent"`
	Status4xxRecent            int64   `json:"status_4xx_recent"`
	Status5xxRecent            int64   `json:"status_5xx_recent"`
	BytesInRecent              int64   `json:"bytes_in_recent"`
	BytesOutRecent             int64   `json:"bytes_out_recent"`
	RequestsPerSecond          float64 `json:"requests_per_second"`
	AppRequestsPerSecond       float64 `json:"app_requests_per_second"`
	ControlPlaneRequestsPerSec float64 `json:"control_plane_requests_per_second"`
}

type serverSummaryOut struct {
	queries.Server
	InstanceCount        int64                 `json:"instance_count"`
	ActiveInstanceCount  int64                 `json:"active_instance_count"`
	RunningInstanceCount int64                 `json:"running_instance_count"`
	Health               string                `json:"health"`
	HeartbeatHealth      string                `json:"heartbeat_health"`
	HeartbeatAgeSeconds  int64                 `json:"heartbeat_age_seconds"`
	Runtime              string                `json:"runtime,omitempty"`
	EnabledComponents    []string              `json:"enabled_components"`
	Components           []serverComponentOut  `json:"components"`
	HostMetrics          *serverHostMetricsOut `json:"host_metrics,omitempty"`
	Traffic              *serverTrafficOut     `json:"traffic,omitempty"`
}

type serverInstanceOut struct {
	DeploymentInstanceID string   `json:"deployment_instance_id"`
	DeploymentID         string   `json:"deployment_id"`
	ProjectID            string   `json:"project_id"`
	ProjectName          string   `json:"project_name"`
	VmID                 *string  `json:"vm_id,omitempty"`
	Role                 string   `json:"role"`
	Status               string   `json:"status"`
	CreatedAt            *string  `json:"created_at,omitempty"`
	UpdatedAt            *string  `json:"updated_at,omitempty"`
	SampledAt            *string  `json:"sampled_at,omitempty"`
	SampleAgeSeconds     *int64   `json:"sample_age_seconds,omitempty"`
	ResourceHealth       string   `json:"resource_health"`
	CPUPercent           *float64 `json:"cpu_percent,omitempty"`
	MemoryRssBytes       int64    `json:"memory_rss_bytes"`
	DiskReadBytes        int64    `json:"disk_read_bytes"`
	DiskWriteBytes       int64    `json:"disk_write_bytes"`
	Source               string   `json:"source,omitempty"`
	MigrationID          *string  `json:"migration_id,omitempty"`
	MigrationState       string   `json:"migration_state,omitempty"`
	MigrationFailure     string   `json:"migration_failure,omitempty"`
}

type serverVolumeOut struct {
	ID           string  `json:"id"`
	ProjectID    string  `json:"project_id"`
	ProjectName  string  `json:"project_name"`
	ServerID     *string `json:"server_id,omitempty"`
	AttachedVMID *string `json:"attached_vm_id,omitempty"`
	MountPath    string  `json:"mount_path"`
	SizeGB       int32   `json:"size_gb"`
	Filesystem   string  `json:"filesystem"`
	Status       string  `json:"status"`
	Health       string  `json:"health"`
	LastError    string  `json:"last_error,omitempty"`
}

type serverDetailOut struct {
	Summary   serverSummaryOut    `json:"summary"`
	Instances []serverInstanceOut `json:"instances"`
	Volumes   []serverVolumeOut   `json:"volumes"`
}

type controlPlaneHealthSummaryOut struct {
	HostCount                  int64   `json:"host_count"`
	UnhealthyHostCount         int64   `json:"unhealthy_host_count"`
	TotalRequestsPerSecond     float64 `json:"total_requests_per_second"`
	AppRequestsPerSecond       float64 `json:"app_requests_per_second"`
	ControlPlaneRequestsPerSec float64 `json:"control_plane_requests_per_second"`
	Status5xxRecent            int64   `json:"status_5xx_recent"`
	MemoryUsedBytes            int64   `json:"memory_used_bytes"`
	MemoryTotalBytes           int64   `json:"memory_total_bytes"`
	DiskUsedBytes              int64   `json:"disk_used_bytes"`
	DiskTotalBytes             int64   `json:"disk_total_bytes"`
	CPUPressurePercent         float64 `json:"cpu_pressure_percent"`
}

type controlPlaneHealthOverviewOut struct {
	Summary controlPlaneHealthSummaryOut `json:"summary"`
	Hosts   []serverSummaryOut           `json:"hosts"`
}

func redactServerSummary(summary serverSummaryOut) serverSummaryOut {
	summary.InternalIp = ""
	summary.IpRange = netip.Prefix{}
	summary.WireguardIp = netip.Addr{}
	summary.WireguardPublicKey = ""
	summary.WireguardEndpoint = ""
	summary.Runtime = ""
	summary.EnabledComponents = nil
	summary.Components = nil
	summary.HostMetrics = nil
	summary.Traffic = nil
	return summary
}

// BuildServerVolumeOut converts a volume and project name into the API output.
func BuildServerVolumeOut(volume queries.ProjectVolume, projectName string) serverVolumeOut {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		projectName = pguuid.ToString(volume.ProjectID)
	}
	return serverVolumeOut{
		ID:           pguuid.ToString(volume.ID),
		ProjectID:    pguuid.ToString(volume.ProjectID),
		ProjectName:  projectName,
		ServerID:     rpcutil.OptionalUUIDString(volume.ServerID),
		AttachedVMID: rpcutil.OptionalUUIDString(volume.AttachedVmID),
		MountPath:    volume.MountPath,
		SizeGB:       volume.SizeGb,
		Filesystem:   volume.Filesystem,
		Status:       volume.Status,
		Health:       volume.Health,
		LastError:    strings.TrimSpace(volume.LastError),
	}
}

func (h *Handler) orgHasWorkloadOnServer(ctx context.Context, serverID, orgID pgtype.UUID) (bool, error) {
	instanceCount, err := h.Q.DeploymentInstanceCountByServerIDForOrg(ctx, queries.DeploymentInstanceCountByServerIDForOrgParams{
		ServerID: serverID,
		OrgID:    orgID,
	})
	if err != nil {
		return false, err
	}
	if instanceCount > 0 {
		return true, nil
	}
	volumeCount, err := h.Q.ProjectVolumeCountByServerIDForOrg(ctx, queries.ProjectVolumeCountByServerIDForOrgParams{
		ServerID: serverID,
		OrgID:    orgID,
	})
	if err != nil {
		return false, err
	}
	return volumeCount > 0, nil
}

func (h *Handler) overviewRowsForOrg(ctx context.Context, orgID pgtype.UUID) ([]serverSummaryOut, error) {
	servers, err := h.Q.ServerFindAll(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := h.Q.ServerComponentStatusFindAll(ctx)
	if err != nil {
		return nil, err
	}
	byServer := make(map[string][]queries.ServerComponentStatus, len(servers))
	for _, status := range statuses {
		sid := pguuid.ToString(status.ServerID)
		byServer[sid] = append(byServer[sid], status)
	}

	now := time.Now().UTC()
	out := make([]serverSummaryOut, 0, len(servers))
	for _, server := range servers {
		instanceCount, err := h.Q.DeploymentInstanceCountByServerIDForOrg(ctx, queries.DeploymentInstanceCountByServerIDForOrgParams{
			ServerID: server.ID,
			OrgID:    orgID,
		})
		if err != nil {
			return nil, err
		}
		activeCount, err := h.Q.DeploymentInstanceActiveCountByServerIDForOrg(ctx, queries.DeploymentInstanceActiveCountByServerIDForOrgParams{
			ServerID: server.ID,
			OrgID:    orgID,
		})
		if err != nil {
			return nil, err
		}
		hasWorkload := instanceCount > 0
		if !hasWorkload {
			volumeCount, vErr := h.Q.ProjectVolumeCountByServerIDForOrg(ctx, queries.ProjectVolumeCountByServerIDForOrgParams{
				ServerID: server.ID,
				OrgID:    orgID,
			})
			if vErr != nil {
				return nil, vErr
			}
			hasWorkload = volumeCount > 0
		}
		if !hasWorkload {
			continue
		}
		out = append(out, buildServerSummary(server, instanceCount, activeCount, 0, byServer[pguuid.ToString(server.ID)], now))
	}
	return out, nil
}

// overviewRowsClusterWide lists every server with cluster-wide instance counts (ignores org scope).
func (h *Handler) overviewRowsClusterWide(ctx context.Context) ([]serverSummaryOut, error) {
	serverRows, err := h.Q.ServerFindAll(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := h.Q.ServerComponentStatusFindAll(ctx)
	if err != nil {
		return nil, err
	}
	byServer := make(map[string][]queries.ServerComponentStatus, len(serverRows))
	for _, status := range statuses {
		sid := pguuid.ToString(status.ServerID)
		byServer[sid] = append(byServer[sid], status)
	}

	now := time.Now().UTC()
	out := make([]serverSummaryOut, 0, len(serverRows))
	for _, server := range serverRows {
		instanceCount, err := h.Q.DeploymentInstanceCountByServerID(ctx, server.ID)
		if err != nil {
			return nil, err
		}
		activeCount, err := h.Q.DeploymentInstanceActiveCountByServerID(ctx, server.ID)
		if err != nil {
			return nil, err
		}
		runningCount, err := h.Q.DeploymentInstanceRunningActiveOnServerCount(ctx, server.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, buildServerSummary(server, instanceCount, activeCount, runningCount, byServer[pguuid.ToString(server.ID)], now))
	}
	return out, nil
}

func (h *Handler) listPlatformServers(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequirePlatformAdmin(w, p) {
		return
	}
	out, err := h.overviewRowsClusterWide(r.Context())
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "list_platform_servers", err)
		return
	}
	rpcutil.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) getPlatformHealthOverview(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequirePlatformAdmin(w, p) {
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()
	hosts, err := h.overviewRowsClusterWide(ctx)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "platform_health_overview", err)
		return
	}
	hostMetricRows, err := h.Q.ServerHostMetricsFindAll(ctx)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "platform_health_overview", err)
		return
	}
	trafficRows, err := h.Q.ServerHTTPUsageRollupsAggregatedRecent(ctx, queries.ServerHTTPUsageRollupsAggregatedRecentParams{
		BucketStart:   pgtype.Timestamptz{Time: now.Add(-trafficRecentWindow).Truncate(time.Minute), Valid: true},
		BucketStart_2: pgtype.Timestamptz{Time: now.Truncate(time.Minute), Valid: true},
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "platform_health_overview", err)
		return
	}

	metricsByServer := buildServerHostMetricsMap(hostMetricRows, now)
	trafficByServer := buildServerTrafficMap(trafficRows, now)

	filtered := make([]serverSummaryOut, 0, len(hosts))
	summary := controlPlaneHealthSummaryOut{}
	for _, host := range hosts {
		if !isControlPlaneHost(host.EnabledComponents) {
			continue
		}
		metrics := defaultServerHostMetricsOut()
		if m, ok := metricsByServer[pguuid.ToString(host.ID)]; ok {
			metrics = m
		}
		traffic := defaultServerTrafficOut()
		if t, ok := trafficByServer[pguuid.ToString(host.ID)]; ok {
			traffic = t
		}
		host.HostMetrics = &metrics
		host.Traffic = &traffic
		filtered = append(filtered, host)

		summary.HostCount++
		if isUnhealthyHost(host, metrics) {
			summary.UnhealthyHostCount++
		}
		summary.TotalRequestsPerSecond += traffic.RequestsPerSecond
		summary.AppRequestsPerSecond += traffic.AppRequestsPerSecond
		summary.ControlPlaneRequestsPerSec += traffic.ControlPlaneRequestsPerSec
		summary.Status5xxRecent += traffic.Status5xxRecent
		summary.MemoryUsedBytes += metrics.MemoryUsedBytes
		summary.MemoryTotalBytes += metrics.MemoryTotalBytes
		summary.DiskUsedBytes += metrics.DiskUsedBytes
		summary.DiskTotalBytes += metrics.DiskTotalBytes
		if metrics.CPUPercent > summary.CPUPressurePercent {
			summary.CPUPressurePercent = metrics.CPUPercent
		}
	}

	rpcutil.WriteJSON(w, http.StatusOK, controlPlaneHealthOverviewOut{
		Summary: summary,
		Hosts:   filtered,
	})
}

func (h *Handler) listServers(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequireOrgAdmin(w, p) {
		return
	}
	out, err := h.overviewRowsForOrg(r.Context(), p.OrganizationID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "list_servers", err)
		return
	}
	if !p.PlatformAdmin {
		for i := range out {
			out[i] = redactServerSummary(out[i])
		}
	}
	rpcutil.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) postServerDrain(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequirePlatformAdmin(w, p) {
		return
	}
	if r.Method != http.MethodPost {
		rpcutil.WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	id, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid server id")
		return
	}
	srv, err := h.Q.ServerFindByID(r.Context(), id)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "server not found")
		return
	}
	if srv.Status != "active" {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_state", "only active servers can be drained")
		return
	}
	if err := h.Q.ServerSetDraining(r.Context(), id); err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_drain", err)
		return
	}
	audit.RecordClusterEvent(r.Context(), h.Q, p.UserID, r, audit.ActionServerDrain, "server", id.String(), map[string]any{
		"hostname": strings.TrimSpace(srv.Hostname),
	})
	rpcutil.WriteJSON(w, http.StatusOK, map[string]string{"status": "draining"})
}

func (h *Handler) postServerActivate(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequirePlatformAdmin(w, p) {
		return
	}
	if r.Method != http.MethodPost {
		rpcutil.WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	id, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid server id")
		return
	}
	srv, err := h.Q.ServerFindByID(r.Context(), id)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "server not found")
		return
	}
	if srv.Status != "draining" && srv.Status != "drained" {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_state", "only draining or drained servers can be reactivated")
		return
	}
	if err := h.Q.ServerSetActive(r.Context(), id); err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_activate", err)
		return
	}
	audit.RecordClusterEvent(r.Context(), h.Q, p.UserID, r, audit.ActionServerActivate, "server", id.String(), map[string]any{
		"hostname":     strings.TrimSpace(srv.Hostname),
		"prior_status": srv.Status,
	})
	rpcutil.WriteJSON(w, http.StatusOK, map[string]string{"status": "active"})
}

func (h *Handler) getServerDetails(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequireOrgAdmin(w, p) {
		return
	}
	h.writeServerDetails(w, r, false, p.OrganizationID, true)
}

func (h *Handler) getPlatformServerDetails(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequirePlatformAdmin(w, p) {
		return
	}
	h.writeServerDetails(w, r, true, pgtype.UUID{}, false)
}

func (h *Handler) writeServerDetails(w http.ResponseWriter, r *http.Request, clusterWide bool, orgID pgtype.UUID, redact bool) {
	id, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid server id")
		return
	}

	server, err := h.Q.ServerFindByID(r.Context(), id)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "server not found")
		return
	}

	ctx := r.Context()
	if !clusterWide {
		hasWorkload, wErr := h.orgHasWorkloadOnServer(ctx, id, orgID)
		if wErr != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", wErr)
			return
		}
		if !hasWorkload {
			rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "server not found")
			return
		}
	}

	var (
		instanceCount   int64
		activeCount     int64
		usageRows       []queries.ServerInstanceUsageLatestRow
		volumesByServer []queries.ProjectVolume
		hostMetrics     = defaultServerHostMetricsOut()
		traffic         = defaultServerTrafficOut()
	)
	if clusterWide {
		instanceCount, err = h.Q.DeploymentInstanceCountByServerID(ctx, id)
		if err != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
		activeCount, err = h.Q.DeploymentInstanceActiveCountByServerID(ctx, id)
		if err != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
		usageRows, err = h.Q.ServerInstanceUsageLatest(ctx, id)
		if err != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
		volumesByServer, err = h.Q.ProjectVolumeFindByServerID(ctx, id)
		if err != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
	} else {
		instanceCount, err = h.Q.DeploymentInstanceCountByServerIDForOrg(ctx, queries.DeploymentInstanceCountByServerIDForOrgParams{
			ServerID: id,
			OrgID:    orgID,
		})
		if err != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
		activeCount, err = h.Q.DeploymentInstanceActiveCountByServerIDForOrg(ctx, queries.DeploymentInstanceActiveCountByServerIDForOrgParams{
			ServerID: id,
			OrgID:    orgID,
		})
		if err != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
		var rowsOrg []queries.ServerInstanceUsageLatestForOrgRow
		rowsOrg, err = h.Q.ServerInstanceUsageLatestForOrg(ctx, queries.ServerInstanceUsageLatestForOrgParams{
			ServerID: id,
			OrgID:    orgID,
		})
		if err != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
		usageRows = usageLatestForOrgRowsToLatest(rowsOrg)
		volumesByServer, err = h.Q.ProjectVolumeFindByServerIDForOrg(ctx, queries.ProjectVolumeFindByServerIDForOrgParams{
			ServerID: id,
			OrgID:    orgID,
		})
		if err != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
	}

	statuses, err := h.Q.ServerComponentStatusFindByServerID(ctx, id)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}

	now := time.Now().UTC()
	if row, hostErr := h.Q.ServerHostMetricsFindByServerID(ctx, id); hostErr == nil {
		hostMetrics = buildServerHostMetricsOut(row, now)
	} else if hostErr != pgx.ErrNoRows {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", hostErr)
		return
	}
	trafficRows, err := h.Q.ServerHTTPUsageRollupsAggregatedRecent(ctx, queries.ServerHTTPUsageRollupsAggregatedRecentParams{
		BucketStart:   pgtype.Timestamptz{Time: now.Add(-trafficRecentWindow).Truncate(time.Minute), Valid: true},
		BucketStart_2: pgtype.Timestamptz{Time: now.Truncate(time.Minute), Valid: true},
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	if t, ok := buildServerTrafficMap(trafficRows, now)[pguuid.ToString(id)]; ok {
		traffic = t
	}
	instances, runningCount := buildServerInstancesFromUsageLatest(usageRows, now)

	slices.SortFunc(instances, func(a, b serverInstanceOut) int {
		if x := compareResourceHealth(a.ResourceHealth, b.ResourceHealth); x != 0 {
			return x
		}
		if x := compareInstanceRole(a.Role, b.Role); x != 0 {
			return x
		}
		if x := compareStatus(a.Status, b.Status); x != 0 {
			return x
		}
		if a.ProjectName != b.ProjectName {
			if a.ProjectName < b.ProjectName {
				return -1
			}
			return 1
		}
		if a.DeploymentInstanceID < b.DeploymentInstanceID {
			return -1
		}
		if a.DeploymentInstanceID > b.DeploymentInstanceID {
			return 1
		}
		return 0
	})

	volumes := make([]serverVolumeOut, 0, len(volumesByServer))
	for _, vol := range volumesByServer {
		projectName := ""
		project, err := h.Q.ProjectFirstByID(ctx, vol.ProjectID)
		switch {
		case err == nil:
			projectName = project.Name
		case err != nil && err != pgx.ErrNoRows:
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
		volumes = append(volumes, BuildServerVolumeOut(vol, projectName))
	}
	slices.SortFunc(volumes, func(a, b serverVolumeOut) int {
		if a.ProjectName != b.ProjectName {
			if a.ProjectName < b.ProjectName {
				return -1
			}
			return 1
		}
		if a.MountPath != b.MountPath {
			if a.MountPath < b.MountPath {
				return -1
			}
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})

	summary := buildServerSummary(server, instanceCount, activeCount, runningCount, statuses, now)
	summary.HostMetrics = &hostMetrics
	summary.Traffic = &traffic
	if redact {
		summary = redactServerSummary(summary)
	}

	rpcutil.WriteJSON(w, http.StatusOK, serverDetailOut{
		Summary:   summary,
		Instances: instances,
		Volumes:   volumes,
	})
}

func usageLatestForOrgRowsToLatest(rows []queries.ServerInstanceUsageLatestForOrgRow) []queries.ServerInstanceUsageLatestRow {
	out := make([]queries.ServerInstanceUsageLatestRow, len(rows))
	for i, r := range rows {
		out[i] = queries.ServerInstanceUsageLatestRow{
			DeploymentInstanceID:         r.DeploymentInstanceID,
			DeploymentID:                 r.DeploymentID,
			ProjectID:                    r.ProjectID,
			ProjectName:                  r.ProjectName,
			VmID:                         r.VmID,
			Role:                         r.Role,
			Status:                       r.Status,
			CreatedAt:                    r.CreatedAt,
			UpdatedAt:                    r.UpdatedAt,
			MigrationID:                  r.MigrationID,
			MigrationState:               r.MigrationState,
			MigrationDestinationServerID: r.MigrationDestinationServerID,
			MigrationFailureMessage:      r.MigrationFailureMessage,
			SampledAt:                    r.SampledAt,
			CpuPercent:                   r.CpuPercent,
			MemoryRssBytes:               r.MemoryRssBytes,
			DiskReadBytes:                r.DiskReadBytes,
			DiskWriteBytes:               r.DiskWriteBytes,
			Source:                       r.Source,
		}
	}
	return out
}

func buildServerInstancesFromUsageLatest(rows []queries.ServerInstanceUsageLatestRow, now time.Time) ([]serverInstanceOut, int64) {
	instances := make([]serverInstanceOut, 0, len(rows))
	runningCount := int64(0)
	for _, row := range rows {
		if row.Role == "active" && row.Status == "running" {
			runningCount++
		}
		var cpu *float64
		if row.CpuPercent.Valid {
			v := row.CpuPercent.Float64
			cpu = &v
		}
		var vmID *string
		if row.VmID.Valid {
			s := pguuid.ToString(row.VmID)
			vmID = &s
		}
		var migrationID *string
		if row.MigrationID.Valid {
			s := pguuid.ToString(row.MigrationID)
			migrationID = &s
		}
		resourceHealth := "missing"
		var sampleAgeSeconds *int64
		if row.SampledAt.Valid {
			age := ageSeconds(now, row.SampledAt.Time)
			sampleAgeSeconds = &age
			resourceHealth = "fresh"
			if now.Sub(row.SampledAt.Time) > 2*usagePollerEvery {
				resourceHealth = "stale"
			}
		}
		instances = append(instances, serverInstanceOut{
			DeploymentInstanceID: pguuid.ToString(row.DeploymentInstanceID),
			DeploymentID:         pguuid.ToString(row.DeploymentID),
			ProjectID:            pguuid.ToString(row.ProjectID),
			ProjectName:          row.ProjectName,
			VmID:                 vmID,
			Role:                 row.Role,
			Status:               row.Status,
			CreatedAt:            rpcutil.FormatTS(row.CreatedAt),
			UpdatedAt:            rpcutil.FormatTS(row.UpdatedAt),
			SampledAt:            rpcutil.FormatTS(row.SampledAt),
			SampleAgeSeconds:     sampleAgeSeconds,
			ResourceHealth:       resourceHealth,
			CPUPercent:           cpu,
			MemoryRssBytes:       row.MemoryRssBytes,
			DiskReadBytes:        row.DiskReadBytes,
			DiskWriteBytes:       row.DiskWriteBytes,
			Source:               row.Source,
			MigrationID:          migrationID,
			MigrationState:       row.MigrationState,
			MigrationFailure:     row.MigrationFailureMessage,
		})
	}
	return instances, runningCount
}

func buildServerSummary(server queries.Server, instanceCount, activeCount, runningCount int64, rows []queries.ServerComponentStatus, now time.Time) serverSummaryOut {
	statusByComponent := make(map[string]queries.ServerComponentStatus, len(rows))
	for _, row := range rows {
		statusByComponent[row.Component] = row
	}

	components := make([]serverComponentOut, 0, len(serverComponentOrder))
	enabledComponents := make([]string, 0, len(rows))
	runtimeName := ""
	overall := "unknown"
	heartbeatHealth := deriveHeartbeatHealth(server, now)
	if server.Status == "dead" {
		overall = "stale"
	}
	if heartbeatHealth == "stale" {
		overall = "stale"
	}

	healthySeen := false
	degradedSeen := false
	for _, component := range serverComponentOrder {
		row, ok := statusByComponent[component]
		if !ok {
			components = append(components, serverComponentOut{
				Component: component,
				Enabled:   false,
				Health:    "unknown",
			})
			continue
		}
		enabledComponents = append(enabledComponents, component)
		out := buildComponentOut(row, now)
		components = append(components, out)
		if out.Health == "stale" {
			overall = "stale"
		} else if out.Health == "degraded" {
			degradedSeen = true
		} else if out.Health == "healthy" {
			healthySeen = true
		}
		if runtimeName == "" {
			if v, ok := out.Metadata["runtime"].(string); ok && v != "" {
				runtimeName = v
			}
		}
	}

	if overall != "stale" {
		switch {
		case degradedSeen:
			overall = "degraded"
		case healthySeen && heartbeatHealth == "healthy":
			overall = "healthy"
		case heartbeatHealth == "healthy" && len(enabledComponents) == 0:
			overall = "unknown"
		case heartbeatHealth == "healthy":
			overall = "healthy"
		default:
			overall = "unknown"
		}
	}

	return serverSummaryOut{
		Server:               server,
		InstanceCount:        instanceCount,
		ActiveInstanceCount:  activeCount,
		RunningInstanceCount: runningCount,
		Health:               overall,
		HeartbeatHealth:      heartbeatHealth,
		HeartbeatAgeSeconds:  ageSeconds(now, server.LastHeartbeatAt.Time),
		Runtime:              runtimeName,
		EnabledComponents:    enabledComponents,
		Components:           components,
	}
}

func buildComponentOut(row queries.ServerComponentStatus, now time.Time) serverComponentOut {
	metadata := decodeStatusMetadata(row.Metadata)
	health := deriveComponentHealth(row, now)
	return serverComponentOut{
		Component:        row.Component,
		Enabled:          true,
		Health:           health,
		RawStatus:        row.Status,
		ObservedAt:       rpcutil.FormatTS(row.ObservedAt),
		LastSuccessAt:    rpcutil.FormatTS(row.LastSuccessAt),
		LastErrorAt:      rpcutil.FormatTS(row.LastErrorAt),
		LastErrorMessage: row.LastErrorMessage,
		Metadata:         metadata,
	}
}

func defaultServerHostMetricsOut() serverHostMetricsOut {
	return serverHostMetricsOut{SampleHealth: "missing"}
}

func buildServerHostMetricsMap(rows []queries.ServerHostMetric, now time.Time) map[string]serverHostMetricsOut {
	out := make(map[string]serverHostMetricsOut, len(rows))
	for _, row := range rows {
		out[pguuid.ToString(row.ServerID)] = buildServerHostMetricsOut(row, now)
	}
	return out
}

func buildServerHostMetricsOut(row queries.ServerHostMetric, now time.Time) serverHostMetricsOut {
	out := serverHostMetricsOut{
		SampledAt:            rpcutil.FormatTS(row.SampledAt),
		CPUPercent:           row.CpuPercent,
		LoadAvg1m:            row.LoadAvg1m,
		LoadAvg5m:            row.LoadAvg5m,
		LoadAvg15m:           row.LoadAvg15m,
		MemoryTotalBytes:     row.MemoryTotalBytes,
		MemoryAvailableBytes: row.MemoryAvailableBytes,
		MemoryUsedBytes:      row.MemoryUsedBytes,
		DiskTotalBytes:       row.DiskTotalBytes,
		DiskFreeBytes:        row.DiskFreeBytes,
		DiskUsedBytes:        row.DiskUsedBytes,
		DiskReadBytesPerSec:  row.DiskReadBytesPerSec,
		DiskWriteBytesPerSec: row.DiskWriteBytesPerSec,
		StateDiskPath:        row.StateDiskPath,
		StateDiskTotalBytes:  row.StateDiskTotalBytes,
		StateDiskFreeBytes:   row.StateDiskFreeBytes,
		StateDiskUsedBytes:   row.StateDiskUsedBytes,
		SampleHealth:         "missing",
	}
	if row.SampledAt.Valid {
		age := ageSeconds(now, row.SampledAt.Time)
		out.SampleAgeSeconds = &age
		out.SampleHealth = "fresh"
		if now.Sub(row.SampledAt.Time) > 2*hostMetricsEvery {
			out.SampleHealth = "stale"
		}
	}
	return out
}

func defaultServerTrafficOut() serverTrafficOut {
	return serverTrafficOut{WindowSeconds: int64(trafficRecentWindow.Seconds())}
}

func buildServerTrafficMap(rows []queries.ServerHTTPUsageRollupsAggregatedRecentRow, now time.Time) map[string]serverTrafficOut {
	type trafficAccumulator struct {
		requestTotal   float64
		requestApp     float64
		requestControl float64
		status4xx      float64
		status5xx      float64
		bytesIn        float64
		bytesOut       float64
	}
	windowStart := now.Add(-trafficRecentWindow)
	accByServer := make(map[string]*trafficAccumulator)
	for _, row := range rows {
		if !row.BucketStart.Valid {
			continue
		}
		weight := bucketOverlapWeight(row.BucketStart.Time, windowStart, now)
		if weight <= 0 {
			continue
		}
		key := pguuid.ToString(row.ServerID)
		acc := accByServer[key]
		if acc == nil {
			acc = &trafficAccumulator{}
			accByServer[key] = acc
		}
		req := float64(row.RequestCount) * weight
		acc.requestTotal += req
		acc.status4xx += float64(row.Status4xx) * weight
		acc.status5xx += float64(row.Status5xx) * weight
		acc.bytesIn += float64(row.BytesIn) * weight
		acc.bytesOut += float64(row.BytesOut) * weight
		switch row.TrafficKind {
		case "app_edge":
			acc.requestApp += req
		case "control_plane_api":
			acc.requestControl += req
		}
	}

	out := make(map[string]serverTrafficOut, len(accByServer))
	for key, acc := range accByServer {
		windowSeconds := trafficRecentWindow.Seconds()
		out[key] = serverTrafficOut{
			WindowSeconds:              int64(windowSeconds),
			RequestCountRecent:         int64(math.Round(acc.requestTotal)),
			Status4xxRecent:            int64(math.Round(acc.status4xx)),
			Status5xxRecent:            int64(math.Round(acc.status5xx)),
			BytesInRecent:              int64(math.Round(acc.bytesIn)),
			BytesOutRecent:             int64(math.Round(acc.bytesOut)),
			RequestsPerSecond:          acc.requestTotal / windowSeconds,
			AppRequestsPerSecond:       acc.requestApp / windowSeconds,
			ControlPlaneRequestsPerSec: acc.requestControl / windowSeconds,
		}
	}
	return out
}

func bucketOverlapWeight(bucketStart, windowStart, windowEnd time.Time) float64 {
	bucketEnd := bucketStart.Add(time.Minute)
	start := bucketStart
	if start.Before(windowStart) {
		start = windowStart
	}
	end := bucketEnd
	if end.After(windowEnd) {
		end = windowEnd
	}
	if !end.After(start) {
		return 0
	}
	return end.Sub(start).Seconds() / time.Minute.Seconds()
}

func isControlPlaneHost(enabled []string) bool {
	for _, component := range enabled {
		if component == "api" || component == "edge" {
			return true
		}
	}
	return false
}

func isUnhealthyHost(host serverSummaryOut, metrics serverHostMetricsOut) bool {
	return host.Health != "healthy" || metrics.SampleHealth == "stale" || metrics.SampleHealth == "missing"
}

func decodeStatusMetadata(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func deriveHeartbeatHealth(server queries.Server, now time.Time) string {
	if server.Status == "dead" {
		return "stale"
	}
	if !server.LastHeartbeatAt.Valid {
		return "unknown"
	}
	if now.Sub(server.LastHeartbeatAt.Time) > 2*serverHeartbeatExpected {
		return "stale"
	}
	return "healthy"
}

func deriveComponentHealth(row queries.ServerComponentStatus, now time.Time) string {
	staleAfter := componentHeartbeatEvery
	if row.Component == "usage_poller" {
		staleAfter = usagePollerEvery
	}
	if row.ObservedAt.Valid && now.Sub(row.ObservedAt.Time) > 2*staleAfter {
		return "stale"
	}
	if row.Component == "usage_poller" && row.LastSuccessAt.Valid && now.Sub(row.LastSuccessAt.Time) > 2*staleAfter {
		return "stale"
	}
	if row.Status == "degraded" {
		return "degraded"
	}
	return "healthy"
}

func compareResourceHealth(a, b string) int {
	return compareByOrder(a, b, []string{"stale", "missing", "fresh"})
}

func compareInstanceRole(a, b string) int {
	return compareByOrder(a, b, []string{"active", "warm_pool", "template"})
}

func compareStatus(a, b string) int {
	return compareByOrder(a, b, []string{"failed", "starting", "pending", "running", "stopped"})
}

func compareByOrder(a, b string, order []string) int {
	ai := len(order)
	bi := len(order)
	for i, item := range order {
		if a == item {
			ai = i
		}
		if b == item {
			bi = i
		}
	}
	switch {
	case ai < bi:
		return -1
	case ai > bi:
		return 1
	default:
		return 0
	}
}

func ageSeconds(now, ts time.Time) int64 {
	if ts.IsZero() {
		return 0
	}
	if now.Before(ts) {
		return 0
	}
	return int64(now.Sub(ts).Seconds())
}
