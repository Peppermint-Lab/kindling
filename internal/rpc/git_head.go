package rpc

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/githubapi"
)

func commitsEquivalent(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) >= 7 && len(b) >= 7 && (strings.HasPrefix(a, b) || strings.HasPrefix(b, a)) {
		return true
	}
	return false
}

func shortGitSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// GET /api/projects/{id}/git-head?ref=optional-branch-or-tag
func (a *API) gitHead(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}

	project, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	repo := strings.TrimSpace(project.GithubRepository)
	if repo == "" {
		writeAPIError(w, http.StatusBadRequest, "no_repository", "project has no GitHub repository")
		return
	}

	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	sha, usedRef, err := githubapi.ResolveCommit(r.Context(), nil, a.gitHubToken(), repo, ref)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "github_error", err.Error())
		return
	}

	var runningCommit, runningDepID string
	running, err := a.q.DeploymentLatestRunningByProjectID(r.Context(), id)
	if err == nil {
		runningCommit = strings.TrimSpace(running.GithubCommit)
		runningDepID = pgUUIDToString(running.ID)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "deployment_lookup", err)
		return
	}

	deployments, err := a.q.DeploymentFindByProjectID(r.Context(), id)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_deployments", err)
		return
	}
	latestDeployed := ""
	if len(deployments) > 0 {
		latestDeployed = strings.TrimSpace(deployments[0].GithubCommit)
	}

	baseline := runningCommit
	if baseline == "" {
		baseline = latestDeployed
	}
	updateAvailable := true
	if baseline != "" {
		updateAvailable = !commitsEquivalent(baseline, sha)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"repository":              githubapi.NormalizeRepo(repo),
		"ref":                     usedRef,
		"sha":                     sha,
		"short_sha":               shortGitSHA(sha),
		"running_deployment_id":   runningDepID,
		"running_commit":          runningCommit,
		"latest_deployed_commit":  latestDeployed,
		"update_available":        updateAvailable,
		"github_token_configured": a.gitHubToken() != "",
	})
}
