package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

const (
	serverHeartbeatExpected = 10 * time.Second
	componentHeartbeatEvery = 10 * time.Second
	usagePollerEvery        = 15 * time.Second
)

var serverComponentOrder = []string{"api", "edge", "worker", "usage_poller"}

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

func buildServerVolumeOut(volume queries.ProjectVolume, projectName string) serverVolumeOut {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		projectName = pgUUIDToString(volume.ProjectID)
	}
	return serverVolumeOut{
		ID:           pgUUIDToString(volume.ID),
		ProjectID:    pgUUIDToString(volume.ProjectID),
		ProjectName:  projectName,
		ServerID:     optionalUUIDString(volume.ServerID),
		AttachedVMID: optionalUUIDString(volume.AttachedVmID),
		MountPath:    volume.MountPath,
		SizeGB:       volume.SizeGb,
		Filesystem:   volume.Filesystem,
		Status:       volume.Status,
		Health:       volume.Health,
		LastError:    strings.TrimSpace(volume.LastError),
	}
}

func (a *API) serverOverviewRows(ctx context.Context) ([]serverSummaryOut, error) {
	servers, err := a.q.ServerFindAll(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := a.q.ServerComponentStatusFindAll(ctx)
	if err != nil {
		return nil, err
	}
	byServer := make(map[string][]queries.ServerComponentStatus, len(servers))
	for _, status := range statuses {
		byServer[pgUUIDToString(status.ServerID)] = append(byServer[pgUUIDToString(status.ServerID)], status)
	}

	now := time.Now().UTC()
	out := make([]serverSummaryOut, 0, len(servers))
	for _, server := range servers {
		instanceCount, err := a.q.DeploymentInstanceCountByServerID(ctx, server.ID)
		if err != nil {
			return nil, err
		}
		activeCount, err := a.q.DeploymentInstanceActiveCountByServerID(ctx, server.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, buildServerSummary(server, instanceCount, activeCount, 0, byServer[pgUUIDToString(server.ID)], now))
	}
	return out, nil
}

func (a *API) getServerDetails(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requirePlatformAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid server id")
		return
	}

	server, err := a.q.ServerFindByID(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "server not found")
		return
	}

	ctx := r.Context()
	instanceCount, err := a.q.DeploymentInstanceCountByServerID(ctx, id)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	activeCount, err := a.q.DeploymentInstanceActiveCountByServerID(ctx, id)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	statuses, err := a.q.ServerComponentStatusFindByServerID(ctx, id)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	rows, err := a.q.ServerInstanceUsageLatest(ctx, id)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
		return
	}
	volumesByServer, err := a.q.ProjectVolumeFindByServerID(ctx, id)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
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
			s := pgUUIDToString(row.VmID)
			vmID = &s
		}
		var migrationID *string
		if row.MigrationID.Valid {
			s := pgUUIDToString(row.MigrationID)
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
			DeploymentInstanceID: pgUUIDToString(row.DeploymentInstanceID),
			DeploymentID:         pgUUIDToString(row.DeploymentID),
			ProjectID:            pgUUIDToString(row.ProjectID),
			ProjectName:          row.ProjectName,
			VmID:                 vmID,
			Role:                 row.Role,
			Status:               row.Status,
			CreatedAt:            formatTS(row.CreatedAt),
			UpdatedAt:            formatTS(row.UpdatedAt),
			SampledAt:            formatTS(row.SampledAt),
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
		project, err := a.q.ProjectFirstByID(ctx, vol.ProjectID)
		switch {
		case err == nil:
			projectName = project.Name
		case err != nil && err != pgx.ErrNoRows:
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "server_details", err)
			return
		}
		volumes = append(volumes, buildServerVolumeOut(vol, projectName))
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

	writeJSON(w, http.StatusOK, serverDetailOut{
		Summary:   buildServerSummary(server, instanceCount, activeCount, runningCount, statuses, now),
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
		ObservedAt:       formatTS(row.ObservedAt),
		LastSuccessAt:    formatTS(row.LastSuccessAt),
		LastErrorAt:      formatTS(row.LastErrorAt),
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
