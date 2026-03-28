package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func normalizeServiceSlug(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	var b strings.Builder
	b.Grow(len(raw))
	lastHyphen := false
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastHyphen = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if b.Len() == 0 || lastHyphen {
				continue
			}
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (a *API) primaryServiceForProject(ctx context.Context, projectID pgtype.UUID) (queries.Service, error) {
	return a.q.ServicePrimaryByProjectID(ctx, projectID)
}

func (a *API) listProjectServices(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	services, err := a.q.ServiceListByProjectID(r.Context(), projectID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_project_services", err)
		return
	}
	writeJSON(w, http.StatusOK, services)
}

func (a *API) createProjectService(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, project, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	var req struct {
		Name                   string `json:"name"`
		Slug                   string `json:"slug"`
		RootDirectory          string `json:"root_directory"`
		DockerfilePath         string `json:"dockerfile_path"`
		DesiredInstanceCount   *int32 `json:"desired_instance_count"`
		BuildOnlyOnRootChanges *bool  `json:"build_only_on_root_changes"`
		PublicDefault          *bool  `json:"public_default"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "name is required")
		return
	}
	slug := normalizeServiceSlug(req.Slug)
	if slug == "" {
		slug = normalizeServiceSlug(req.Name)
	}
	if slug == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "slug is required")
		return
	}
	rootDirectory := strings.TrimSpace(req.RootDirectory)
	if rootDirectory == "" {
		rootDirectory = project.RootDirectory
	}
	dockerfilePath := strings.TrimSpace(req.DockerfilePath)
	if dockerfilePath == "" {
		dockerfilePath = project.DockerfilePath
	}
	desired := project.DesiredInstanceCount
	if req.DesiredInstanceCount != nil {
		desired = *req.DesiredInstanceCount
	}
	buildOnly := project.BuildOnlyOnRootChanges
	if req.BuildOnlyOnRootChanges != nil {
		buildOnly = *req.BuildOnlyOnRootChanges
	}
	publicDefault := false
	if req.PublicDefault != nil {
		publicDefault = *req.PublicDefault
	}
	service, err := a.q.ServiceCreate(r.Context(), queries.ServiceCreateParams{
		ID:                     pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:              projectID,
		Name:                   req.Name,
		Slug:                   slug,
		RootDirectory:          rootDirectory,
		DockerfilePath:         dockerfilePath,
		DesiredInstanceCount:   desired,
		BuildOnlyOnRootChanges: buildOnly,
		PublicDefault:          publicDefault,
		IsPrimary:              false,
	})
	if err != nil {
		if isPgUniqueViolation(err) {
			writeAPIError(w, http.StatusConflict, "service_slug_taken", "that service slug is already in use for this project")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_project_service", err)
		return
	}
	writeJSON(w, http.StatusCreated, service)
}

func (a *API) getService(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	serviceID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid service id")
		return
	}
	service, err := a.q.ServiceFirstByIDAndOrg(r.Context(), queries.ServiceFirstByIDAndOrgParams{
		ID:    serviceID,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	writeJSON(w, http.StatusOK, service)
}
