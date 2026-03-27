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
)

var projectSecretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type projectSecretOut struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt *string `json:"created_at"`
	UpdatedAt *string `json:"updated_at"`
}

func projectSecretFromEnv(row queries.EnvironmentVariable) projectSecretOut {
	return projectSecretOut{
		ID:        pgUUIDToString(row.ID),
		Name:      row.Name,
		CreatedAt: formatTS(row.CreatedAt),
		UpdatedAt: formatTS(row.UpdatedAt),
	}
}

func projectSecretFromMetadataRow(row queries.EnvironmentVariableMetadataFindByProjectIDRow) projectSecretOut {
	return projectSecretOut{
		ID:        pgUUIDToString(row.ID),
		Name:      row.Name,
		CreatedAt: formatTS(row.CreatedAt),
		UpdatedAt: formatTS(row.UpdatedAt),
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

	rows, err := a.q.EnvironmentVariableMetadataFindByProjectID(r.Context(), projectID)
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
	if p.OrgRole != "owner" && p.OrgRole != "admin" {
		writeAPIError(w, http.StatusForbidden, "forbidden", "owner or admin role required")
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

	row, err := a.q.EnvironmentVariableCreate(r.Context(), queries.EnvironmentVariableCreateParams{
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
	writeJSON(w, http.StatusOK, projectSecretFromEnv(row))
}

func (a *API) deleteProjectSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if p.OrgRole != "owner" && p.OrgRole != "admin" {
		writeAPIError(w, http.StatusForbidden, "forbidden", "owner or admin role required")
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
	if _, err := a.q.EnvironmentVariableDeleteByIDAndProjectID(r.Context(), queries.EnvironmentVariableDeleteByIDAndProjectIDParams{
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
