package rpc

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

var projectSecretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type projectSecretOut struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt *string `json:"created_at"`
	UpdatedAt *string `json:"updated_at"`
}

type serviceSecretOut struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Scope       string  `json:"scope"`
	ServiceID   *string `json:"service_id,omitempty"`
	ServiceName *string `json:"service_name,omitempty"`
	CreatedAt   *string `json:"created_at"`
	UpdatedAt   *string `json:"updated_at"`
}

func projectSecretFromEnv(row queries.EnvironmentVariable) projectSecretOut {
	return projectSecretOut{
		ID:        pguuid.ToString(row.ID),
		Name:      row.Name,
		CreatedAt: formatTS(row.CreatedAt),
		UpdatedAt: formatTS(row.UpdatedAt),
	}
}

func projectSecretFromMetadataRow(row queries.EnvironmentVariableMetadataFindProjectDefaultsByProjectIDRow) projectSecretOut {
	return projectSecretOut{
		ID:        pguuid.ToString(row.ID),
		Name:      row.Name,
		CreatedAt: formatTS(row.CreatedAt),
		UpdatedAt: formatTS(row.UpdatedAt),
	}
}

func projectSecretFromProjectUpsertRow(row queries.EnvironmentVariableUpsertProjectDefaultRow) projectSecretOut {
	return projectSecretOut{
		ID:        pguuid.ToString(row.ID),
		Name:      row.Name,
		CreatedAt: formatTS(row.CreatedAt),
		UpdatedAt: formatTS(row.UpdatedAt),
	}
}

func serviceSecretFromMetadataRow(row queries.EnvironmentVariableMetadataFindEffectiveByServiceIDRow, service queries.Service) serviceSecretOut {
	out := serviceSecretOut{
		ID:        pguuid.ToString(row.ID),
		Name:      row.Name,
		CreatedAt: formatTS(row.CreatedAt),
		UpdatedAt: formatTS(row.UpdatedAt),
		Scope:     "project_default",
	}
	if row.ServiceID.Valid {
		serviceID := pguuid.ToString(row.ServiceID)
		out.Scope = "service"
		out.ServiceID = &serviceID
		if row.ServiceID == service.ID {
			serviceName := service.Name
			out.ServiceName = &serviceName
		}
	}
	return out
}

func serviceSecretFromUpsertRow(row queries.EnvironmentVariableUpsertForServiceRow, service queries.Service) serviceSecretOut {
	serviceID := pguuid.ToString(row.ServiceID)
	serviceName := service.Name
	return serviceSecretOut{
		ID:          pguuid.ToString(row.ID),
		Name:        row.Name,
		Scope:       "service",
		ServiceID:   &serviceID,
		ServiceName: &serviceName,
		CreatedAt:   formatTS(row.CreatedAt),
		UpdatedAt:   formatTS(row.UpdatedAt),
	}
}

func validProjectSecretName(name string) bool {
	return projectSecretNamePattern.MatchString(name)
}

func (a *API) listProjectSecrets(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}

	rows, err := a.q.EnvironmentVariableMetadataFindProjectDefaultsByProjectID(r.Context(), projectID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_project_secrets", err)
		return
	}
	out := make([]projectSecretOut, len(rows))
	for i := range rows {
		out[i] = projectSecretFromMetadataRow(rows[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) upsertProjectSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	var req struct {
		Name  string  `json:"name"`
		Value *string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validProjectSecretName(req.Name) {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "name must be a valid environment variable identifier")
		return
	}
	if req.Value == nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "value is required")
		return
	}
	if a.cfg == nil {
		writeAPIError(w, http.StatusInternalServerError, "encrypt_project_secret", "config manager unavailable")
		return
	}
	enc, err := a.cfg.EncryptProjectSecretValue(*req.Value)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "encrypt_project_secret", err)
		return
	}

	row, err := a.q.EnvironmentVariableUpsertProjectDefault(r.Context(), queries.EnvironmentVariableUpsertProjectDefaultParams{
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID: projectID,
		Name:      req.Name,
		Value:     enc,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "upsert_project_secret", err)
		return
	}
	if a.dashboardEvents != nil {
		a.dashboardEvents.Publish(TopicProject(uuid.UUID(projectID.Bytes)))
	}
	writeJSON(w, http.StatusOK, projectSecretFromProjectUpsertRow(row))
}

func (a *API) deleteProjectSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	secretID, err := parseUUID(r.PathValue("secret_id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid secret id")
		return
	}
	if _, err := a.q.EnvironmentVariableDeleteProjectDefaultByIDAndProjectID(r.Context(), queries.EnvironmentVariableDeleteProjectDefaultByIDAndProjectIDParams{
		ID:        secretID,
		ProjectID: projectID,
	}); err != nil {
		if err == pgx.ErrNoRows {
			writeAPIError(w, http.StatusNotFound, "not_found", "project secret not found")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project_secret", err)
		return
	}
	if a.dashboardEvents != nil {
		a.dashboardEvents.Publish(TopicProject(uuid.UUID(projectID.Bytes)))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listServiceSecrets(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	ctx, ok := a.serviceForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	rows, err := a.q.EnvironmentVariableMetadataFindEffectiveByServiceID(r.Context(), ctx.ID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_service_secrets", err)
		return
	}
	out := make([]serviceSecretOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, serviceSecretFromMetadataRow(row, ctx.Service))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) upsertServiceSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	ctx, ok := a.serviceForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	var req struct {
		Name  string  `json:"name"`
		Value *string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validProjectSecretName(req.Name) {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "name must be a valid environment variable identifier")
		return
	}
	if req.Value == nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "value is required")
		return
	}
	if a.cfg == nil {
		writeAPIError(w, http.StatusInternalServerError, "encrypt_service_secret", "config manager unavailable")
		return
	}
	enc, err := a.cfg.EncryptProjectSecretValue(*req.Value)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "encrypt_service_secret", err)
		return
	}
	row, err := a.q.EnvironmentVariableUpsertForService(r.Context(), queries.EnvironmentVariableUpsertForServiceParams{
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID: ctx.Project.ID,
		ServiceID: ctx.ID,
		Name:      req.Name,
		Value:     enc,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "upsert_service_secret", err)
		return
	}
	if a.dashboardEvents != nil {
		a.dashboardEvents.Publish(TopicProject(uuid.UUID(ctx.Project.ID.Bytes)))
	}
	writeJSON(w, http.StatusOK, serviceSecretFromUpsertRow(row, ctx.Service))
}

func (a *API) deleteServiceSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	ctx, ok := a.serviceForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	secretID, err := parseUUID(r.PathValue("secret_id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid secret id")
		return
	}
	if _, err := a.q.EnvironmentVariableDeleteByIDAndServiceID(r.Context(), queries.EnvironmentVariableDeleteByIDAndServiceIDParams{
		ID:        secretID,
		ServiceID: ctx.ID,
	}); err != nil {
		if err == pgx.ErrNoRows {
			writeAPIError(w, http.StatusNotFound, "not_found", "service secret not found")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_service_secret", err)
		return
	}
	if a.dashboardEvents != nil {
		a.dashboardEvents.Publish(TopicProject(uuid.UUID(ctx.Project.ID.Bytes)))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) projectForRequest(w http.ResponseWriter, r *http.Request, orgID pgtype.UUID) (pgtype.UUID, queries.Project, bool) {
	projectID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return pgtype.UUID{}, queries.Project{}, false
	}
	project, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: orgID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return pgtype.UUID{}, queries.Project{}, false
	}
	return projectID, project, true
}
