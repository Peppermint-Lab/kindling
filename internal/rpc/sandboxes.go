package rpc

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
	kruntime "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/sandbox"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

type sandboxOut struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	HostGroup          string            `json:"host_group"`
	Backend            string            `json:"backend,omitempty"`
	Arch               string            `json:"arch,omitempty"`
	DesiredState       string            `json:"desired_state"`
	ObservedState      string            `json:"observed_state"`
	ServerID           *string           `json:"server_id,omitempty"`
	VmID               *string           `json:"vm_id,omitempty"`
	TemplateID         *string           `json:"template_id,omitempty"`
	BaseImageRef       string            `json:"base_image_ref"`
	Vcpu               int32             `json:"vcpu"`
	MemoryMb           int32             `json:"memory_mb"`
	DiskGb             int32             `json:"disk_gb"`
	Env                map[string]string `json:"env,omitempty"`
	GitRepo            string            `json:"git_repo,omitempty"`
	GitRef             string            `json:"git_ref,omitempty"`
	AutoSuspendSeconds int64             `json:"auto_suspend_seconds"`
	LastUsedAt         *string           `json:"last_used_at,omitempty"`
	ExpiresAt          *string           `json:"expires_at,omitempty"`
	PublishedHTTPPort  *int32            `json:"published_http_port,omitempty"`
	RuntimeURL         string            `json:"runtime_url,omitempty"`
	SSHHostPublicKey   string            `json:"ssh_host_public_key,omitempty"`
	FailureMessage     string            `json:"failure_message,omitempty"`
	CreatedAt          *string           `json:"created_at,omitempty"`
	UpdatedAt          *string           `json:"updated_at,omitempty"`
	PublishedPorts     []sandboxPortOut  `json:"published_ports,omitempty"`
}

type sandboxPortOut struct {
	TargetPort     int32  `json:"target_port"`
	Protocol       string `json:"protocol"`
	Visibility     string `json:"visibility"`
	PublicHostname string `json:"public_hostname,omitempty"`
}

type sandboxTemplateOut struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	HostGroup       string  `json:"host_group"`
	Backend         string  `json:"backend,omitempty"`
	Arch            string  `json:"arch,omitempty"`
	SourceSandboxID *string `json:"source_sandbox_id,omitempty"`
	ServerID        *string `json:"server_id,omitempty"`
	BaseImageRef    string  `json:"base_image_ref"`
	SnapshotRef     string  `json:"snapshot_ref,omitempty"`
	Vcpu            int32   `json:"vcpu"`
	MemoryMb        int32   `json:"memory_mb"`
	DiskGb          int32   `json:"disk_gb"`
	Status          string  `json:"status"`
	FailureMessage  string  `json:"failure_message,omitempty"`
	CreatedAt       *string `json:"created_at,omitempty"`
	UpdatedAt       *string `json:"updated_at,omitempty"`
}

func sandboxToOut(sb queries.Sandbox, ports []queries.SandboxPublishedPort) sandboxOut {
	out := sandboxOut{
		ID:                 pguuid.ToString(sb.ID),
		Name:               sb.Name,
		HostGroup:          sb.HostGroup,
		Backend:            sb.Backend,
		Arch:               sb.Arch,
		DesiredState:       sb.DesiredState,
		ObservedState:      sb.ObservedState,
		ServerID:           optionalUUIDString(sb.ServerID),
		VmID:               optionalUUIDString(sb.VmID),
		TemplateID:         optionalUUIDString(sb.TemplateID),
		BaseImageRef:       sb.BaseImageRef,
		Vcpu:               sb.Vcpu,
		MemoryMb:           sb.MemoryMb,
		DiskGb:             sb.DiskGb,
		GitRepo:            sb.GitRepo,
		GitRef:             sb.GitRef,
		AutoSuspendSeconds: sb.AutoSuspendSeconds,
		LastUsedAt:         formatTS(sb.LastUsedAt),
		ExpiresAt:          formatTS(sb.ExpiresAt),
		RuntimeURL:         sb.RuntimeUrl,
		SSHHostPublicKey:   strings.TrimSpace(sb.SshHostPublicKey),
		FailureMessage:     strings.TrimSpace(sb.FailureMessage),
		CreatedAt:          formatTS(sb.CreatedAt),
		UpdatedAt:          formatTS(sb.UpdatedAt),
	}
	if sb.PublishedHttpPort.Valid {
		v := sb.PublishedHttpPort.Int32
		out.PublishedHTTPPort = &v
	}
	if len(sb.EnvJson) > 0 {
		var env map[string]string
		if err := json.Unmarshal(sb.EnvJson, &env); err == nil && len(env) > 0 {
			out.Env = env
		}
	}
	if len(ports) > 0 {
		out.PublishedPorts = make([]sandboxPortOut, 0, len(ports))
		for _, port := range ports {
			out.PublishedPorts = append(out.PublishedPorts, sandboxPortOut{
				TargetPort:     port.TargetPort,
				Protocol:       port.Protocol,
				Visibility:     port.Visibility,
				PublicHostname: port.PublicHostname,
			})
		}
	}
	return out
}

func normalizeSandboxAutoSuspendSeconds(v int64) (int64, error) {
	if v < 0 {
		return 0, errors.New("auto_suspend_seconds must be >= 0")
	}
	return v, nil
}

func normalizeSandboxBaseImageRef(v string) (string, error) {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return "", errors.New("base_image_ref is required")
	}
	return trimmed, nil
}

func sandboxDeleteCanBypassReconciler(sb queries.Sandbox) bool {
	return !sb.VmID.Valid
}

func sandboxRuntimeObservabilityReady(sb queries.Sandbox) bool {
	return strings.EqualFold(strings.TrimSpace(sb.ObservedState), "running")
}

func resolveSandboxHostGroup(requested string, template *queries.SandboxTemplate) string {
	if trimmed := strings.TrimSpace(requested); trimmed != "" {
		return trimmed
	}
	if template != nil {
		if trimmed := strings.TrimSpace(template.HostGroup); trimmed != "" {
			return trimmed
		}
	}
	return sandbox.HostGroupLinux
}

func sandboxTemplateToOut(tpl queries.SandboxTemplate) sandboxTemplateOut {
	return sandboxTemplateOut{
		ID:              pguuid.ToString(tpl.ID),
		Name:            tpl.Name,
		HostGroup:       tpl.HostGroup,
		Backend:         tpl.Backend,
		Arch:            tpl.Arch,
		SourceSandboxID: optionalUUIDString(tpl.SourceSandboxID),
		ServerID:        optionalUUIDString(tpl.ServerID),
		BaseImageRef:    tpl.BaseImageRef,
		SnapshotRef:     tpl.SnapshotRef,
		Vcpu:            tpl.Vcpu,
		MemoryMb:        tpl.MemoryMb,
		DiskGb:          tpl.DiskGb,
		Status:          tpl.Status,
		FailureMessage:  strings.TrimSpace(tpl.FailureMessage),
		CreatedAt:       formatTS(tpl.CreatedAt),
		UpdatedAt:       formatTS(tpl.UpdatedAt),
	}
}

func (a *API) listSandboxes(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}

	limit := int32(50)
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	offset := int32(0)
	if q := r.URL.Query().Get("offset"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n >= 0 {
			offset = int32(n)
		}
	}

	rows, err := a.q.SandboxListByOrg(r.Context(), p.OrganizationID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_sandboxes", err)
		return
	}

	total := int32(len(rows))
	if offset >= total {
		writeJSON(w, http.StatusOK, map[string]any{"items": []sandboxOut{}, "total": total, "limit": limit, "offset": offset})
		return
	}

	end := offset + limit
	if end > total {
		end = total
	}

	out := make([]sandboxOut, 0, end-offset)
	for _, row := range rows[offset:end] {
		ports, _ := a.q.SandboxPublishedPortsBySandboxID(r.Context(), row.ID)
		out = append(out, sandboxToOut(row, ports))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total, "limit": limit, "offset": offset})
}

func (a *API) createSandbox(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	var req struct {
		Name               string            `json:"name"`
		HostGroup          string            `json:"host_group"`
		BaseImageRef       string            `json:"base_image_ref"`
		TemplateID         string            `json:"template_id"`
		Vcpu               int32             `json:"vcpu"`
		MemoryMb           int32             `json:"memory_mb"`
		DiskGb             int32             `json:"disk_gb"`
		Env                map[string]string `json:"env"`
		GitRepo            string            `json:"git_repo"`
		GitRef             string            `json:"git_ref"`
		AutoSuspendSeconds int64             `json:"auto_suspend_seconds"`
		ExpiresAt          *string           `json:"expires_at"`
		PublishedHTTPPort  *int32            `json:"published_http_port"`
		DesiredState       string            `json:"desired_state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid json body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "name is required")
		return
	}

	desiredState := strings.TrimSpace(req.DesiredState)
	if desiredState == "" {
		desiredState = "running"
	}
	envJSON, _ := json.Marshal(req.Env)
	expiresAt, err := parseOptionalTime(req.ExpiresAt)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "expires_at must be RFC3339")
		return
	}
	templateID := pgtype.UUID{}
	var template *queries.SandboxTemplate
	if strings.TrimSpace(req.TemplateID) != "" {
		id, err := parseUUID(req.TemplateID)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "invalid template_id")
			return
		}
		tpl, err := a.q.SandboxTemplateFirstByIDAndOrg(r.Context(), queries.SandboxTemplateFirstByIDAndOrgParams{
			ID:    id,
			OrgID: p.OrganizationID,
		})
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "not_found", "sandbox template not found")
			return
		}
		templateID = id
		template = &tpl
	}
	hostGroup := resolveSandboxHostGroup(req.HostGroup, template)

	baseImageRef := strings.TrimSpace(req.BaseImageRef)
	if template != nil && baseImageRef == "" {
		baseImageRef = template.BaseImageRef
	}
	if baseImageRef == "" {
		baseImageRef = sandbox.DefaultBaseImageRef
	}
	vcpu := req.Vcpu
	if template != nil && vcpu <= 0 {
		vcpu = template.Vcpu
	}
	if vcpu <= 0 {
		vcpu = 2
	}
	memoryMb := req.MemoryMb
	if template != nil && memoryMb <= 0 {
		memoryMb = template.MemoryMb
	}
	if memoryMb <= 0 {
		memoryMb = 2048
	}
	diskGb := req.DiskGb
	if template != nil && diskGb <= 0 {
		diskGb = template.DiskGb
	}
	if diskGb <= 0 {
		diskGb = 10
	}
	autoSuspend, err := normalizeSandboxAutoSuspendSeconds(req.AutoSuspendSeconds)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	row, err := a.q.SandboxCreate(r.Context(), queries.SandboxCreateParams{
		ID:                 pgtype.UUID{Bytes: uuid.New(), Valid: true},
		OrgID:              p.OrganizationID,
		Name:               name,
		HostGroup:          hostGroup,
		Backend:            "",
		Arch:               "",
		DesiredState:       desiredState,
		ObservedState:      "pending",
		ServerID:           pgtype.UUID{},
		VmID:               pgtype.UUID{},
		TemplateID:         templateID,
		BaseImageRef:       baseImageRef,
		Vcpu:               vcpu,
		MemoryMb:           memoryMb,
		DiskGb:             diskGb,
		EnvJson:            envJSON,
		GitRepo:            strings.TrimSpace(req.GitRepo),
		GitRef:             strings.TrimSpace(req.GitRef),
		AutoSuspendSeconds: autoSuspend,
		LastUsedAt:         pgtype.Timestamptz{},
		ExpiresAt:          expiresAt,
		PublishedHttpPort:  optionalInt4(req.PublishedHTTPPort),
		RuntimeUrl:         "",
		SshHostPublicKey:   "",
		FailureMessage:     "",
		CreatedByUserID:    pgtype.UUID{Bytes: p.UserID, Valid: p.UserID != uuid.Nil},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_sandbox", err)
		return
	}
	if a.sandboxReconciler != nil {
		a.sandboxReconciler.ScheduleNow(uuid.UUID(row.ID.Bytes))
	}
	writeJSON(w, http.StatusCreated, sandboxToOut(row, nil))
}

func (a *API) getSandbox(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox id")
		return
	}
	row, err := a.q.SandboxFirstByIDAndOrg(r.Context(), queries.SandboxFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	ports, _ := a.q.SandboxPublishedPortsBySandboxID(r.Context(), row.ID)
	writeJSON(w, http.StatusOK, sandboxToOut(row, ports))
}

func (a *API) patchSandbox(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox id")
		return
	}
	sb, err := a.q.SandboxFirstByIDAndOrg(r.Context(), queries.SandboxFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	var req struct {
		AutoSuspendSeconds *int64  `json:"auto_suspend_seconds"`
		BaseImageRef       *string `json:"base_image_ref"`
		Vcpu               *int32  `json:"vcpu"`
		MemoryMb           *int32  `json:"memory_mb"`
		DiskGb             *int32  `json:"disk_gb"`
		ExpiresAt          *string `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid json body")
		return
	}
	if req.AutoSuspendSeconds == nil && req.BaseImageRef == nil && req.Vcpu == nil && req.MemoryMb == nil && req.DiskGb == nil && req.ExpiresAt == nil {
		writeJSON(w, http.StatusOK, sandboxToOut(sb, nil))
		return
	}

	baseImageRef := sb.BaseImageRef
	vcpu := sb.Vcpu
	memoryMb := sb.MemoryMb
	diskGb := sb.DiskGb
	autoSuspend := sb.AutoSuspendSeconds
	expiresAt := sb.ExpiresAt

	configChangeRequested := req.BaseImageRef != nil || req.Vcpu != nil || req.MemoryMb != nil || req.DiskGb != nil || req.ExpiresAt != nil
	if configChangeRequested && sb.ObservedState == "running" {
		writeAPIError(w, http.StatusConflict, "sandbox_running", "stop the sandbox before editing image, resources, or expiry")
		return
	}
	if req.BaseImageRef != nil {
		baseImageRef, err = normalizeSandboxBaseImageRef(*req.BaseImageRef)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}
	if req.Vcpu != nil {
		if *req.Vcpu <= 0 {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "vcpu must be > 0")
			return
		}
		vcpu = *req.Vcpu
	}
	if req.MemoryMb != nil {
		if *req.MemoryMb <= 0 {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "memory_mb must be > 0")
			return
		}
		memoryMb = *req.MemoryMb
	}
	if req.DiskGb != nil {
		if *req.DiskGb <= 0 {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "disk_gb must be > 0")
			return
		}
		diskGb = *req.DiskGb
	}
	if req.AutoSuspendSeconds != nil {
		autoSuspend, err = normalizeSandboxAutoSuspendSeconds(*req.AutoSuspendSeconds)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}
	if req.ExpiresAt != nil {
		expiresAt, err = parseOptionalTime(req.ExpiresAt)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "expires_at must be RFC3339")
			return
		}
	}
	sb, err = a.q.SandboxUpdateSettings(r.Context(), queries.SandboxUpdateSettingsParams{
		ID:                 sb.ID,
		BaseImageRef:       baseImageRef,
		Vcpu:               vcpu,
		MemoryMb:           memoryMb,
		DiskGb:             diskGb,
		AutoSuspendSeconds: autoSuspend,
		ExpiresAt:          expiresAt,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "update_sandbox", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxToOut(sb, nil))
}

func (a *API) deleteSandbox(w http.ResponseWriter, r *http.Request) {
	a.setSandboxDesiredState(w, r, "deleted")
}

func (a *API) startSandbox(w http.ResponseWriter, r *http.Request) {
	a.setSandboxDesiredState(w, r, "running")
}

func (a *API) stopSandbox(w http.ResponseWriter, r *http.Request) {
	a.setSandboxDesiredState(w, r, "stopped")
}

func (a *API) setSandboxDesiredState(w http.ResponseWriter, r *http.Request, state string) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox id")
		return
	}
	row, err := a.q.SandboxFirstByIDAndOrg(r.Context(), queries.SandboxFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	row, err = a.q.SandboxUpdateDesiredState(r.Context(), queries.SandboxUpdateDesiredStateParams{
		ID:           row.ID,
		DesiredState: state,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "sandbox_state", err)
		return
	}
	if state == "deleted" && sandboxDeleteCanBypassReconciler(row) {
		row, err = a.q.SandboxMarkDeleted(r.Context(), row.ID)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "sandbox_state", err)
			return
		}
		writeJSON(w, http.StatusOK, sandboxToOut(row, nil))
		return
	}
	if a.sandboxReconciler != nil {
		a.sandboxReconciler.ScheduleNow(uuid.UUID(row.ID.Bytes))
	}
	writeJSON(w, http.StatusOK, sandboxToOut(row, nil))
}

func (a *API) createSandboxTemplate(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox id")
		return
	}
	sb, err := a.q.SandboxFirstByIDAndOrg(r.Context(), queries.SandboxFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = sb.Name + "-template"
	}
	tpl, err := a.q.SandboxTemplateCreate(r.Context(), queries.SandboxTemplateCreateParams{
		ID:              pgtype.UUID{Bytes: uuid.New(), Valid: true},
		OrgID:           p.OrganizationID,
		Name:            name,
		HostGroup:       sb.HostGroup,
		Backend:         sb.Backend,
		Arch:            sb.Arch,
		SourceSandboxID: sb.ID,
		ServerID:        sb.ServerID,
		BaseImageRef:    sb.BaseImageRef,
		SnapshotRef:     "",
		Vcpu:            sb.Vcpu,
		MemoryMb:        sb.MemoryMb,
		DiskGb:          sb.DiskGb,
		Status:          "pending",
		FailureMessage:  "",
		CreatedByUserID: pgtype.UUID{Bytes: p.UserID, Valid: p.UserID != uuid.Nil},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_sandbox_template", err)
		return
	}
	if a.sandboxTplReconciler != nil {
		a.sandboxTplReconciler.ScheduleNow(uuid.UUID(tpl.ID.Bytes))
	}
	if a.sandboxReconciler != nil {
		a.sandboxReconciler.ScheduleNow(uuid.UUID(sb.ID.Bytes))
	}
	writeJSON(w, http.StatusCreated, sandboxTemplateToOut(tpl))
}

func (a *API) listSandboxTemplates(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	rows, err := a.q.SandboxTemplateListByOrg(r.Context(), p.OrganizationID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_sandbox_templates", err)
		return
	}
	out := make([]sandboxTemplateOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, sandboxTemplateToOut(row))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getSandboxTemplate(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox template id")
		return
	}
	row, err := a.q.SandboxTemplateFirstByIDAndOrg(r.Context(), queries.SandboxTemplateFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox template not found")
		return
	}
	writeJSON(w, http.StatusOK, sandboxTemplateToOut(row))
}

func (a *API) deleteSandboxTemplate(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox template id")
		return
	}
	row, err := a.q.SandboxTemplateFirstByIDAndOrg(r.Context(), queries.SandboxTemplateFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox template not found")
		return
	}
	row, err = a.q.SandboxTemplateMarkDeleted(r.Context(), row.ID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_sandbox_template", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxTemplateToOut(row))
}

func (a *API) cloneSandboxTemplate(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox template id")
		return
	}
	tpl, err := a.q.SandboxTemplateFirstByIDAndOrg(r.Context(), queries.SandboxTemplateFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox template not found")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = tpl.Name + "-clone"
	}
	row, err := a.q.SandboxCreate(r.Context(), queries.SandboxCreateParams{
		ID:                 pgtype.UUID{Bytes: uuid.New(), Valid: true},
		OrgID:              p.OrganizationID,
		Name:               name,
		HostGroup:          tpl.HostGroup,
		Backend:            tpl.Backend,
		Arch:               tpl.Arch,
		DesiredState:       "running",
		ObservedState:      "pending",
		ServerID:           tpl.ServerID,
		VmID:               pgtype.UUID{},
		TemplateID:         tpl.ID,
		BaseImageRef:       tpl.BaseImageRef,
		Vcpu:               tpl.Vcpu,
		MemoryMb:           tpl.MemoryMb,
		DiskGb:             tpl.DiskGb,
		EnvJson:            []byte(`{}`),
		GitRepo:            "",
		GitRef:             "",
		AutoSuspendSeconds: 0,
		LastUsedAt:         pgtype.Timestamptz{},
		ExpiresAt:          pgtype.Timestamptz{},
		PublishedHttpPort:  pgtype.Int4{},
		RuntimeUrl:         "",
		FailureMessage:     "",
		CreatedByUserID:    pgtype.UUID{Bytes: p.UserID, Valid: p.UserID != uuid.Nil},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "clone_sandbox_template", err)
		return
	}
	if a.sandboxReconciler != nil {
		a.sandboxReconciler.ScheduleNow(uuid.UUID(row.ID.Bytes))
	}
	writeJSON(w, http.StatusCreated, sandboxToOut(row, nil))
}

func (a *API) publishSandboxHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox id")
		return
	}
	sb, err := a.q.SandboxFirstByIDAndOrg(r.Context(), queries.SandboxFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	var req struct {
		TargetPort int32  `json:"target_port"`
		Hostname   string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid json body")
		return
	}
	if req.TargetPort <= 0 {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "target_port is required")
		return
	}
	hostname := strings.TrimSpace(req.Hostname)
	if hostname == "" {
		hostname = a.defaultSandboxHostname(sb)
	}
	if _, err := a.q.SandboxPublishedPortUpsert(r.Context(), queries.SandboxPublishedPortUpsertParams{
		ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SandboxID:      sb.ID,
		TargetPort:     req.TargetPort,
		Protocol:       "http",
		Visibility:     "public",
		PublicHostname: hostname,
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "publish_sandbox_http", err)
		return
	}
	sb, err = a.q.SandboxUpdatePublishPort(r.Context(), queries.SandboxUpdatePublishPortParams{
		ID:                sb.ID,
		PublishedHttpPort: pgtype.Int4{Int32: req.TargetPort, Valid: true},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "publish_sandbox_http", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxToOut(sb, nil))
}

func (a *API) execSandbox(w http.ResponseWriter, r *http.Request) {
	if !a.requireValidSandboxProxyIfPresent(w, r) {
		return
	}
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	sb, ok := a.requireSandboxAccess(w, r, p)
	if !ok {
		return
	}
	if a.proxySandboxHTTPRequest(w, r, sb) {
		return
	}
	access, ok := a.sandboxGuestAccess(w, sb)
	if !ok {
		return
	}
	var req struct {
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
		Env  []string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid json body")
		return
	}
	if len(req.Argv) == 0 {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "argv is required")
		return
	}
	a.recordSandboxAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "exec", "started", nil, "")
	out, err := access.ExecGuest(r.Context(), uuid.UUID(sb.ID.Bytes), req.Argv, req.Cwd, req.Env)
	if err != nil {
		a.recordSandboxAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "exec", "failed", nil, err.Error())
		writeAPIErrorFromErr(w, http.StatusConflict, "sandbox_exec", err)
		return
	}
	_ = a.q.SandboxUpdateLastUsedAt(r.Context(), sb.ID)
	a.recordSandboxAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "exec", "ended", &out.ExitCode, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"exit_code": out.ExitCode,
		"output":    out.Output,
	})
}

func (a *API) copyIntoSandbox(w http.ResponseWriter, r *http.Request) {
	if !a.requireValidSandboxProxyIfPresent(w, r) {
		return
	}
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	sb, ok := a.requireSandboxAccess(w, r, p)
	if !ok {
		return
	}
	if a.proxySandboxHTTPRequest(w, r, sb) {
		return
	}
	access, ok := a.sandboxGuestAccess(w, sb)
	if !ok {
		return
	}
	targetPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if targetPath == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "path query parameter is required")
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_body", "could not read request body")
		return
	}
	if err := access.WriteGuestFile(r.Context(), uuid.UUID(sb.ID.Bytes), targetPath, data); err != nil {
		a.recordSandboxAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "copy_in", "failed", nil, err.Error())
		writeAPIErrorFromErr(w, http.StatusConflict, "sandbox_copy_in", err)
		return
	}
	_ = a.q.SandboxUpdateLastUsedAt(r.Context(), sb.ID)
	a.recordSandboxAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "copy_in", "ended", nil, "")
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *API) copyOutOfSandbox(w http.ResponseWriter, r *http.Request) {
	if !a.requireValidSandboxProxyIfPresent(w, r) {
		return
	}
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	sb, ok := a.requireSandboxAccess(w, r, p)
	if !ok {
		return
	}
	if a.proxySandboxHTTPRequest(w, r, sb) {
		return
	}
	access, ok := a.sandboxGuestAccess(w, sb)
	if !ok {
		return
	}
	sourcePath := strings.TrimSpace(r.URL.Query().Get("path"))
	if sourcePath == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "path query parameter is required")
		return
	}
	data, err := access.ReadGuestFile(r.Context(), uuid.UUID(sb.ID.Bytes), sourcePath)
	if err != nil {
		a.recordSandboxAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "copy_out", "failed", nil, err.Error())
		writeAPIErrorFromErr(w, http.StatusConflict, "sandbox_copy_out", err)
		return
	}
	_ = a.q.SandboxUpdateLastUsedAt(r.Context(), sb.ID)
	a.recordSandboxAccessEvent(r.Context(), uuid.UUID(sb.ID.Bytes), p.UserID, "copy_out", "ended", nil, "")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *API) sandboxLogs(w http.ResponseWriter, r *http.Request) {
	if !a.requireValidSandboxProxyIfPresent(w, r) {
		return
	}
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	sb, ok := a.requireSandboxAccess(w, r, p)
	if !ok {
		return
	}
	if !sandboxRuntimeObservabilityReady(sb) {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	if a.proxySandboxHTTPRequest(w, r, sb) {
		return
	}
	rt, ok := a.sandboxLocalRuntime(w, sb)
	if !ok {
		return
	}
	logs, err := rt.Logs(r.Context(), uuid.UUID(sb.ID.Bytes))
	if err != nil {
		if errors.Is(err, kruntime.ErrInstanceNotRunning) {
			writeJSON(w, http.StatusOK, []string{})
			return
		}
		writeAPIErrorFromErr(w, http.StatusConflict, "sandbox_logs", err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (a *API) sandboxStats(w http.ResponseWriter, r *http.Request) {
	if !a.requireValidSandboxProxyIfPresent(w, r) {
		return
	}
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	sb, ok := a.requireSandboxAccess(w, r, p)
	if !ok {
		return
	}
	if !sandboxRuntimeObservabilityReady(sb) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if a.proxySandboxHTTPRequest(w, r, sb) {
		return
	}
	rt, ok := a.sandboxLocalRuntime(w, sb)
	if !ok {
		return
	}
	stats, err := rt.ResourceStats(r.Context(), uuid.UUID(sb.ID.Bytes))
	if err != nil {
		if errors.Is(err, kruntime.ErrInstanceNotRunning) {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeAPIErrorFromErr(w, http.StatusConflict, "sandbox_stats", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cpu_nanos_cumulative": stats.CPUNanosCumulative,
		"memory_rss_bytes":     stats.MemoryRSSBytes,
		"disk_read_bytes":      stats.DiskReadBytes,
		"disk_write_bytes":     stats.DiskWriteBytes,
		"collected_at":         stats.CollectedAt.UTC().Format(time.RFC3339Nano),
	})
}

func (a *API) unpublishSandboxHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox id")
		return
	}
	sb, err := a.q.SandboxFirstByIDAndOrg(r.Context(), queries.SandboxFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.PublishedHttpPort.Valid {
		_ = a.q.SandboxPublishedPortDeleteBySandboxAndPort(r.Context(), queries.SandboxPublishedPortDeleteBySandboxAndPortParams{
			SandboxID:  sb.ID,
			TargetPort: sb.PublishedHttpPort.Int32,
		})
	}
	sb, err = a.q.SandboxUpdatePublishPort(r.Context(), queries.SandboxUpdatePublishPortParams{
		ID:                sb.ID,
		PublishedHttpPort: pgtype.Int4{},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "unpublish_sandbox_http", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxToOut(sb, nil))
}

func parseOptionalTime(raw *string) (pgtype.Timestamptz, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.Timestamptz{}, nil
	}
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(*raw))
	if err != nil {
		return pgtype.Timestamptz{}, err
	}
	return pgtype.Timestamptz{Time: ts.UTC(), Valid: true}, nil
}

func optionalInt4(v *int32) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *v, Valid: true}
}

func (a *API) requireSandboxAccess(w http.ResponseWriter, r *http.Request, p auth.Principal) (queries.Sandbox, bool) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox id")
		return queries.Sandbox{}, false
	}
	sb, err := a.q.SandboxFirstByIDAndOrg(r.Context(), queries.SandboxFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return queries.Sandbox{}, false
	}
	if sb.ObservedState != "running" {
		writeAPIError(w, http.StatusConflict, "sandbox_not_running", "sandbox must be running")
		return queries.Sandbox{}, false
	}
	return sb, true
}

func (a *API) sandboxLocalRuntime(w http.ResponseWriter, sb queries.Sandbox) (kruntime.Runtime, bool) {
	if a.sandboxSvc == nil || a.sandboxSvc.Runtime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "sandbox_runtime", "sandbox runtime is unavailable on this server")
		return nil, false
	}
	if sb.ServerID.Valid && a.sandboxSvc.ServerID != uuid.Nil && uuid.UUID(sb.ServerID.Bytes) != a.sandboxSvc.ServerID {
		writeAPIError(w, http.StatusConflict, "sandbox_runtime", "sandbox is assigned to a different worker")
		return nil, false
	}
	return a.sandboxSvc.Runtime, true
}

func (a *API) sandboxGuestAccess(w http.ResponseWriter, sb queries.Sandbox) (kruntime.GuestAccess, bool) {
	rt, ok := a.sandboxLocalRuntime(w, sb)
	if !ok {
		return nil, false
	}
	access, ok := rt.(kruntime.GuestAccess)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "sandbox_runtime", "guest access is not implemented for this runtime")
		return nil, false
	}
	return access, true
}

func (a *API) sandboxStreamAccess(w http.ResponseWriter, sb queries.Sandbox) (kruntime.GuestStreamAccess, bool) {
	rt, ok := a.sandboxLocalRuntime(w, sb)
	if !ok {
		return nil, false
	}
	access, ok := rt.(kruntime.GuestStreamAccess)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "sandbox_runtime", "live shell streaming is not implemented for this runtime")
		return nil, false
	}
	return access, true
}

func (a *API) defaultSandboxHostname(sb queries.Sandbox) string {
	if a == nil || a.cfg == nil || a.cfg.Snapshot() == nil {
		return ""
	}
	base := strings.Trim(strings.TrimSpace(a.cfg.Snapshot().SandboxBaseDomain), ".")
	if base == "" {
		return ""
	}
	label := strings.ToLower(strings.TrimSpace(sb.Name))
	label = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, label)
	label = strings.Trim(label, "-")
	for strings.Contains(label, "--") {
		label = strings.ReplaceAll(label, "--", "-")
	}
	if label == "" {
		label = "sandbox"
	}
	shortID := pguuid.ToString(sb.ID)
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	return label + "-" + shortID + "." + base
}
