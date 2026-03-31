// Package servers provides server management API handlers.
package servers

import (
	"context"
	"encoding/json"
	"net/netip"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/rpc/rpcutil"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

const (
	serverHeartbeatExpected = 10 * time.Second
	componentHeartbeatEvery = 10 * time.Second
	usagePollerEvery        = 15 * time.Second
)

var serverComponentOrder = []string{"api", "edge", "worker", "usage_poller"}

// Handler provides server management API handlers.
type Handler struct {
	Q *queries.Queries
}

// RegisterRoutes mounts server management routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
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

type serverSummaryOut struct {
	queries.Server
	InstanceCount        int64                `json:"instance_count"`
	ActiveInstanceCount  int64                `json:"active_instance_count"`
	RunningInstanceCount int64                `json:"running_instance_count"`
	Health               string               `json:"health"`
	HeartbeatHealth      string               `json:"heartbeat_health"`
	HeartbeatAgeSeconds  int64                `json:"heartbeat_age_seconds"`
	Runtime              string               `json:"runtime,omitempty"`
	EnabledComponents    []string             `json:"enabled_components"`
	Components           []serverComponentOut `json:"components"`
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

func redactServerSummary(summary serverSummaryOut) serverSummaryOut {
	summary.InternalIp = ""
	summary.IpRange = netip.Prefix{}
	summary.WireguardIp = netip.Addr{}
	summary.WireguardPublicKey = ""
	summary.WireguardEndpoint = ""
	summary.Runtime = ""
	summary.EnabledComponents = nil
	summary.Components = nil
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
	hasWorkload, err := h.orgHasWorkloadOnServer(ctx, id, p.OrganizationID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	if !hasWorkload {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "server not found")
		return
	}

	instanceCount, err := h.Q.DeploymentInstanceCountByServerIDForOrg(ctx, queries.DeploymentInstanceCountByServerIDForOrgParams{
		ServerID: id,
		OrgID:    p.OrganizationID,
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	activeCount, err := h.Q.DeploymentInstanceActiveCountByServerIDForOrg(ctx, queries.DeploymentInstanceActiveCountByServerIDForOrgParams{
		ServerID: id,
		OrgID:    p.OrganizationID,
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	statuses, err := h.Q.ServerComponentStatusFindByServerID(ctx, id)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	rows, err := h.Q.ServerInstanceUsageLatestForOrg(ctx, queries.ServerInstanceUsageLatestForOrgParams{
		ServerID: id,
		OrgID:    p.OrganizationID,
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	volumesByServer, err := h.Q.ProjectVolumeFindByServerIDForOrg(ctx, queries.ProjectVolumeFindByServerIDForOrgParams{
		ServerID: id,
		OrgID:    p.OrganizationID,
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}

	now := time.Now().UTC()
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
	if !p.PlatformAdmin {
		summary = redactServerSummary(summary)
	}

	rpcutil.WriteJSON(w, http.StatusOK, serverDetailOut{
		Summary:   summary,
		Instances: instances,
		Volumes:   volumes,
	})
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
