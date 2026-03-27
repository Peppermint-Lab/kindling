package rpc

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/preview"
)

type previewLatestDeploymentOut struct {
	ID                  string  `json:"id"`
	GithubCommit        string  `json:"github_commit"`
	Phase               string  `json:"phase"`
	BuildStatus         string  `json:"build_status,omitempty"`
	CreatedAt           *string `json:"created_at,omitempty"`
	PreviewScaledToZero bool    `json:"preview_scaled_to_zero"`
	WakeRequestedAt     *string `json:"wake_requested_at,omitempty"`
}

type previewImmutableURLOut struct {
	URL          string `json:"url"`
	DeploymentID string `json:"deployment_id,omitempty"`
	GithubCommit string `json:"github_commit,omitempty"`
}

type previewEnvironmentOut struct {
	ID               string                      `json:"id"`
	PRNumber         int32                       `json:"pr_number"`
	HeadBranch       string                      `json:"head_branch"`
	HeadSHA          string                      `json:"head_sha"`
	LifecycleState   string                      `json:"lifecycle_state"`
	StableURL        string                      `json:"stable_url,omitempty"`
	ClosedAt         *string                     `json:"closed_at,omitempty"`
	ExpiresAt        *string                     `json:"expires_at,omitempty"`
	LatestDeployment *previewLatestDeploymentOut `json:"latest_deployment,omitempty"`
	ImmutableURLs    []previewImmutableURLOut    `json:"immutable_urls,omitempty"`
}

func previewLifecycleState(pe queries.PreviewEnvironment, now time.Time) string {
	if !pe.ClosedAt.Valid {
		return "active"
	}
	if pe.ExpiresAt.Valid && !pe.ExpiresAt.Time.After(now) {
		return "cleanup_due"
	}
	return "closed"
}

func pgUUIDEqual(a, b pgtype.UUID) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Bytes == b.Bytes)
}

func (a *API) previewLatestDeploymentOut(ctx context.Context, latestID pgtype.UUID) (*previewLatestDeploymentOut, error) {
	if !latestID.Valid {
		return nil, nil
	}
	dep, err := a.q.DeploymentFirstByID(ctx, latestID)
	if err != nil {
		return nil, err
	}
	var build *queries.Build
	var buildStatus string
	if dep.BuildID.Valid {
		b, err := a.q.BuildFirstByID(ctx, dep.BuildID)
		if err == nil {
			build = &b
			buildStatus = b.Status
		}
	}
	return &previewLatestDeploymentOut{
		ID:                  pgUUIDToString(dep.ID),
		GithubCommit:        dep.GithubCommit,
		Phase:               deploymentPhase(dep, build),
		BuildStatus:         buildStatus,
		CreatedAt:           formatTS(dep.CreatedAt),
		PreviewScaledToZero: dep.PreviewScaledToZero,
		WakeRequestedAt:     formatTS(dep.WakeRequestedAt),
	}, nil
}

func (a *API) previewProjectAndEnv(ctx context.Context, orgID pgtype.UUID, projectID, previewID pgtype.UUID) (queries.Project, queries.PreviewEnvironment, bool) {
	project, err := a.q.ProjectFirstByIDAndOrg(ctx, queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: orgID,
	})
	if err != nil {
		return queries.Project{}, queries.PreviewEnvironment{}, false
	}
	pe, err := a.q.PreviewEnvironmentByID(ctx, previewID)
	if err != nil || !pgUUIDEqual(pe.ProjectID, projectID) {
		return project, queries.PreviewEnvironment{}, false
	}
	return project, pe, true
}

// GET /api/projects/{id}/previews
func (a *API) listProjectPreviews(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	pid, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    pid,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	ctx := r.Context()
	envs, err := a.q.PreviewEnvironmentsByProjectID(ctx, pid)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_previews", err)
		return
	}

	immutRows, err := a.q.PreviewImmutableDomainsByProjectID(ctx, pid)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_previews_domains", err)
		return
	}
	immutByEnv := make(map[string][]previewImmutableURLOut)
	for _, row := range immutRows {
		eid := pgUUIDToString(row.PreviewEnvironmentID)
		entry := previewImmutableURLOut{
			URL: "https://" + row.DomainName,
		}
		if row.DeploymentID.Valid {
			entry.DeploymentID = pgUUIDToString(row.DeploymentID)
		}
		if row.GithubCommit.Valid && row.GithubCommit.String != "" {
			entry.GithubCommit = row.GithubCommit.String
		}
		immutByEnv[eid] = append(immutByEnv[eid], entry)
	}

	now := time.Now()
	out := make([]previewEnvironmentOut, 0, len(envs))
	for _, e := range envs {
		item := previewEnvironmentOut{
			ID:             pgUUIDToString(e.ID),
			PRNumber:       e.PrNumber,
			HeadBranch:     e.HeadBranch,
			HeadSHA:        e.HeadSha,
			LifecycleState: previewLifecycleState(e, now),
		}
		if e.StableDomainName != "" {
			item.StableURL = "https://" + e.StableDomainName
		}
		if latest, err := a.previewLatestDeploymentOut(ctx, e.LatestDeploymentID); err == nil {
			item.LatestDeployment = latest
		}
		item.ClosedAt = formatTS(e.ClosedAt)
		item.ExpiresAt = formatTS(e.ExpiresAt)
		if immut := immutByEnv[item.ID]; len(immut) > 0 {
			item.ImmutableURLs = immut
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/projects/{id}/previews/{preview_id}/redeploy
func (a *API) redeployProjectPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	previewID, err := parseUUID(r.PathValue("preview_id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_preview_id", "invalid preview id")
		return
	}
	_, pe, ok := a.previewProjectAndEnv(r.Context(), p.OrganizationID, projectID, previewID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "preview environment not found")
		return
	}
	if pe.ClosedAt.Valid {
		writeAPIError(w, http.StatusConflict, "invalid_state", "closed previews cannot be redeployed")
		return
	}
	headSHA := strings.TrimSpace(pe.HeadSha)
	headBranch := strings.TrimSpace(pe.HeadBranch)
	if headSHA == "" || headBranch == "" {
		writeAPIError(w, http.StatusConflict, "invalid_state", "preview environment is missing head branch or commit")
		return
	}

	dep, err := a.q.DeploymentCreate(r.Context(), queries.DeploymentCreateParams{
		ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:            projectID,
		GithubCommit:         headSHA,
		GithubBranch:         headBranch,
		DeploymentKind:       "preview",
		PreviewEnvironmentID: pe.ID,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_preview_deployment", err)
		return
	}
	if err := a.q.PreviewEnvironmentSetLatestDeployment(r.Context(), queries.PreviewEnvironmentSetLatestDeploymentParams{
		ID:                 pe.ID,
		LatestDeploymentID: dep.ID,
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "update_preview_environment", err)
		return
	}

	writeJSON(w, http.StatusCreated, a.deploymentToOutCtx(r.Context(), dep))
}

// DELETE /api/projects/{id}/previews/{preview_id}
func (a *API) deleteProjectPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	previewID, err := parseUUID(r.PathValue("preview_id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_preview_id", "invalid preview id")
		return
	}
	_, pe, ok := a.previewProjectAndEnv(r.Context(), p.OrganizationID, projectID, previewID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "preview environment not found")
		return
	}
	if err := preview.CleanupEnvironmentNow(r.Context(), a.q, a.deploymentReconciler, pe.ID); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_preview", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
