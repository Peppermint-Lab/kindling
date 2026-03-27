package rpc

import (
	"net/http"
	"time"

	"github.com/kindlingvm/kindling/internal/database/queries"
)

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
	immutByEnv := make(map[string][]map[string]any)
	for _, row := range immutRows {
		eid := pgUUIDToString(row.PreviewEnvironmentID)
		entry := map[string]any{
			"url": "https://" + row.DomainName,
		}
		if row.DeploymentID.Valid {
			entry["deployment_id"] = pgUUIDToString(row.DeploymentID)
		}
		if row.GithubCommit.Valid && row.GithubCommit.String != "" {
			entry["github_commit"] = row.GithubCommit.String
		}
		immutByEnv[eid] = append(immutByEnv[eid], entry)
	}

	out := make([]map[string]any, 0, len(envs))
	for _, e := range envs {
		item := map[string]any{
			"id":          pgUUIDToString(e.ID),
			"pr_number":   e.PrNumber,
			"head_branch": e.HeadBranch,
			"head_sha":    e.HeadSha,
		}
		if e.StableDomainName != "" {
			item["stable_url"] = "https://" + e.StableDomainName
		}
		if e.LatestDeploymentID.Valid {
			item["latest_deployment_id"] = pgUUIDToString(e.LatestDeploymentID)
		}
		if e.ClosedAt.Valid {
			item["closed_at"] = e.ClosedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if e.ExpiresAt.Valid {
			item["expires_at"] = e.ExpiresAt.Time.UTC().Format(time.RFC3339Nano)
		}
		eid := pgUUIDToString(e.ID)
		if immut, ok := immutByEnv[eid]; ok && len(immut) > 0 {
			item["immutable_urls"] = immut
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}
