// Package webhook handles GitHub webhook events to trigger deployments.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/ci"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/githubactions"
	"github.com/kindlingvm/kindling/internal/githubapi"
	"github.com/kindlingvm/kindling/internal/preview"
	"github.com/kindlingvm/kindling/internal/reconciler"
)

// Handler handles GitHub webhook requests.
type Handler struct {
	q                    *queries.Queries
	cfg                  decryptor
	deploymentReconciler *reconciler.Scheduler
	ciJobReconciler      *reconciler.Scheduler
	ciJobService         ciWorkflowJobHandler
}

type decryptor interface {
	DecryptBytes([]byte) ([]byte, error)
}

type ciWorkflowJobHandler interface {
	HandleGitHubWorkflowJobEvent(context.Context, ci.GitHubWorkflowJobEvent) (ci.GitHubWorkflowJobHandleResult, error)
}

// NewHandler creates a new webhook handler.
func NewHandler(q *queries.Queries, cfg decryptor) *Handler {
	return &Handler{q: q, cfg: cfg}
}

// SetDeploymentReconciler configures the deployment reconciler used for
// immediate preview cleanup work after close/delete lifecycle changes.
func (h *Handler) SetDeploymentReconciler(r *reconciler.Scheduler) {
	h.deploymentReconciler = r
}

func (h *Handler) SetCIJobRuntime(r *reconciler.Scheduler, svc ciWorkflowJobHandler) {
	h.ciJobReconciler = r
	h.ciJobService = svc
}

// pushEvent is the relevant subset of GitHub's push event payload.
type pushEvent struct {
	Ref        string       `json:"ref"`
	After      string       `json:"after"`
	Commits    []pushCommit `json:"commits"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type pushCommit struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

// GitHub push webhooks include at most 2048 commits. A full slice is ambiguous,
// so we avoid skipping builds based on an incomplete changed-file set.
const maxPushCommitsInWebhook = 2048

type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest prBody `json:"pull_request"`
	Repository  repo   `json:"repository"`
}

type prBody struct {
	Head struct {
		Ref string `json:"ref"`
		Sha string `json:"sha"`
	} `json:"head"`
}

type repo struct {
	FullName string `json:"full_name"`
}

type workflowJobEvent struct {
	Action       string `json:"action"`
	Repository   repo   `json:"repository"`
	Organization struct {
		Login string `json:"login"`
	} `json:"organization"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	WorkflowJob struct {
		ID           int64    `json:"id"`
		RunID        int64    `json:"run_id"`
		RunAttempt   int32    `json:"run_attempt"`
		HTMLURL      string   `json:"html_url"`
		Status       string   `json:"status"`
		Conclusion   string   `json:"conclusion"`
		Name         string   `json:"name"`
		WorkflowName string   `json:"workflow_name"`
		Event        string   `json:"event"`
		Labels       []string `json:"labels"`
	} `json:"workflow_job"`
}

func previewBaseDomain(ctx context.Context, q *queries.Queries) string {
	v, err := q.ClusterSettingGet(ctx, config.SettingPreviewBaseDomain)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func previewRetentionSeconds(ctx context.Context, q *queries.Queries) int64 {
	v, err := q.ClusterSettingGet(ctx, config.SettingPreviewRetentionAfterCloseSecs)
	if err != nil {
		return 3600
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return 3600
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 3600
	}
	return n
}

func pushChangedFiles(push pushEvent) ([]string, bool) {
	if len(push.Commits) == 0 {
		return nil, false
	}
	if len(push.Commits) >= maxPushCommitsInWebhook {
		return nil, false
	}
	files := make([]string, 0, len(push.Commits)*3)
	for _, commit := range push.Commits {
		files = append(files, commit.Added...)
		files = append(files, commit.Modified...)
		files = append(files, commit.Removed...)
	}
	if len(files) == 0 {
		return nil, false
	}
	return files, true
}

func rootDirectoryMatchesChangedFiles(rootDir string, files []string) bool {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" || rootDir == "/" {
		return len(files) > 0
	}

	prefix := strings.TrimPrefix(rootDir, "/")
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return len(files) > 0
	}
	prefix += "/"

	for _, file := range files {
		file = strings.TrimSpace(file)
		file = strings.TrimPrefix(file, "/")
		if file == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(file, prefix) {
			return true
		}
	}
	return false
}

func shouldCreateDeploymentForRoot(rootDirectory string, buildOnlyOnRootChanges bool, push pushEvent) bool {
	if !buildOnlyOnRootChanges {
		return true
	}
	changedFiles, ok := pushChangedFiles(push)
	if !ok {
		return true
	}
	return rootDirectoryMatchesChangedFiles(rootDirectory, changedFiles)
}

func shouldCreateDeploymentForPush(project queries.Project, push pushEvent) bool {
	return shouldCreateDeploymentForRoot(project.RootDirectory, project.BuildOnlyOnRootChanges, push)
}

func shouldCreateDeploymentForService(service queries.Service, push pushEvent) bool {
	return shouldCreateDeploymentForRoot(service.RootDirectory, service.BuildOnlyOnRootChanges, push)
}

// ServeHTTP handles POST /webhooks/github.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event == "" {
		http.Error(w, "missing X-GitHub-Event", http.StatusBadRequest)
		return
	}

	switch event {
	case "push":
		h.handlePush(w, r, body)
	case "pull_request":
		h.handlePullRequest(w, r, body)
	case "workflow_job":
		h.handleWorkflowJob(w, r, body)
	default:
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ignored event: %s", event)
	}
}

func (h *Handler) handleWorkflowJob(w http.ResponseWriter, r *http.Request, body []byte) {
	if h.ciJobService == nil {
		http.Error(w, "ci workflow job handler unavailable", http.StatusServiceUnavailable)
		return
	}

	var event workflowJobEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	secret, err := h.workflowJobWebhookSecret(r.Context(), event)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "ignored: repository not connected to Kindling")
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(secret) == "" {
		http.Error(w, "github actions runner webhook secret is not configured", http.StatusServiceUnavailable)
		return
	}
	sig := r.Header.Get("X-Hub-Signature-256")
	if !verifySignature(body, sig, secret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	result, err := h.ciJobService.HandleGitHubWorkflowJobEvent(r.Context(), ci.GitHubWorkflowJobEvent{
		Action:         strings.TrimSpace(event.Action),
		RepoFullName:   githubapi.NormalizeRepo(event.Repository.FullName),
		OrgLogin:       strings.TrimSpace(event.Organization.Login),
		WorkflowName:   strings.TrimSpace(event.WorkflowJob.WorkflowName),
		JobName:        strings.TrimSpace(event.WorkflowJob.Name),
		EventName:      strings.TrimSpace(event.WorkflowJob.Event),
		WorkflowJobID:  event.WorkflowJob.ID,
		WorkflowRunID:  event.WorkflowJob.RunID,
		RunAttempt:     event.WorkflowJob.RunAttempt,
		HTMLURL:        strings.TrimSpace(event.WorkflowJob.HTMLURL),
		Labels:         event.WorkflowJob.Labels,
		InstallationID: event.Installation.ID,
		Conclusion:     strings.TrimSpace(event.WorkflowJob.Conclusion),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if result.Ignored {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ignored: %s", result.Reason)
		return
	}
	if result.ShouldSchedule && result.Job != nil && h.ciJobReconciler != nil {
		h.ciJobReconciler.ScheduleNow(uuid.UUID(result.Job.ID.Bytes))
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (h *Handler) workflowJobWebhookSecret(ctx context.Context, event workflowJobEvent) (string, error) {
	project, err := h.q.ProjectFindByGitHubRepo(ctx, githubapi.NormalizeRepo(event.Repository.FullName))
	if err != nil {
		return "", err
	}
	rows, err := h.q.OrgProviderConnectionListByOrg(ctx, project.OrgID)
	if err != nil {
		return "", err
	}
	owner := strings.TrimSpace(event.Organization.Login)
	if owner == "" {
		owner = strings.TrimSpace(strings.SplitN(githubapi.NormalizeRepo(event.Repository.FullName), "/", 2)[0])
	}
	integrations := make([]githubactions.Integration, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(strings.ToLower(row.Provider)) != "github" || h.cfg == nil {
			continue
		}
		plain, err := h.cfg.DecryptBytes(row.CredentialsCiphertext)
		if err != nil {
			continue
		}
		integration, err := githubactions.IntegrationFromConnection(row, plain)
		if err != nil {
			continue
		}
		integrations = append(integrations, integration)
	}
	integration, ok := githubactions.ResolveIntegrationForOwner(integrations, owner)
	if !ok {
		return "", pgx.ErrNoRows
	}
	return strings.TrimSpace(integration.Credentials.WebhookSecret), nil
}

func (h *Handler) handlePush(w http.ResponseWriter, r *http.Request, body []byte) {
	var push pushEvent
	if err := json.Unmarshal(body, &push); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	branch := strings.TrimPrefix(push.Ref, "refs/heads/")
	if branch != "main" && branch != "master" {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ignored branch: %s", branch)
		return
	}

	repo := push.Repository.FullName
	commit := push.After

	slog.Info("GitHub push received", "repo", repo, "branch", branch, "commit", commit)

	project, err := h.q.ProjectFindByGitHubRepo(r.Context(), repo)
	if err != nil {
		slog.Warn("webhook for unknown repo", "repo", repo)
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	if project.GithubWebhookSecret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifySignature(body, sig, project.GithubWebhookSecret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	services, err := h.q.ServiceListByProjectID(r.Context(), project.ID)
	if err != nil {
		slog.Error("failed to load services for webhook push", "project", project.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type createdDeployment struct {
		ServiceID    string `json:"service_id"`
		ServiceName  string `json:"service_name"`
		DeploymentID string `json:"deployment_id"`
	}
	type skippedService struct {
		ServiceID     string `json:"service_id"`
		ServiceName   string `json:"service_name"`
		RootDirectory string `json:"root_directory"`
	}
	created := make([]createdDeployment, 0, len(services))
	skipped := make([]skippedService, 0, len(services))
	for _, service := range services {
		if !shouldCreateDeploymentForService(service, push) {
			slog.Info("service deployment skipped from webhook: no root directory changes",
				"project", project.Name,
				"service", service.Name,
				"repo", repo,
				"branch", branch,
				"commit", commit,
				"root_directory", service.RootDirectory,
			)
			skipped = append(skipped, skippedService{
				ServiceID:     uuid.UUID(service.ID.Bytes).String(),
				ServiceName:   service.Name,
				RootDirectory: service.RootDirectory,
			})
			continue
		}
		dep, err := h.q.DeploymentCreate(r.Context(), queries.DeploymentCreateParams{
			ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
			ProjectID:            project.ID,
			ServiceID:            service.ID,
			GithubCommit:         commit,
			GithubBranch:         branch,
			DeploymentKind:       "production",
			PreviewEnvironmentID: pgtype.UUID{Valid: false},
		})
		if err != nil {
			slog.Error("failed to create service deployment from webhook", "project", project.Name, "service", service.Name, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		slog.Info("service deployment created from webhook",
			"deployment_id", dep.ID,
			"project", project.Name,
			"service", service.Name,
			"commit", commit,
		)
		created = append(created, createdDeployment{
			ServiceID:    uuid.UUID(service.ID.Bytes).String(),
			ServiceName:  service.Name,
			DeploymentID: uuid.UUID(dep.ID.Bytes).String(),
		})
	}
	if len(created) == 0 {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"project":          project.Name,
			"commit":           commit,
			"skipped":          true,
			"skipped_services": skipped,
		})
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"project":          project.Name,
		"commit":           commit,
		"deployments":      created,
		"skipped_services": skipped,
	})
}

func (h *Handler) handlePullRequest(w http.ResponseWriter, r *http.Request, body []byte) {
	var payload pullRequestEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	repo := strings.TrimSpace(payload.Repository.FullName)
	if repo == "" {
		http.Error(w, "missing repository", http.StatusBadRequest)
		return
	}

	project, err := h.q.ProjectFindByGitHubRepo(r.Context(), repo)
	if err != nil {
		slog.Warn("webhook PR for unknown repo", "repo", repo)
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	if project.GithubWebhookSecret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifySignature(body, sig, project.GithubWebhookSecret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	ctx := r.Context()
	baseDomain := previewBaseDomain(ctx, h.q)
	if strings.TrimSpace(baseDomain) == "" {
		slog.Info("GitHub PR webhook ignored (preview_base_domain unset)", "repo", repo, "pr", payload.Number)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "preview disabled: set cluster_settings.preview_base_domain")
		return
	}

	action := strings.ToLower(strings.TrimSpace(payload.Action))
	switch action {
	case "closed":
		h.handlePullRequestClosed(w, ctx, project, int32(payload.Number))
	case "opened", "reopened", "synchronize":
		h.handlePullRequestSync(w, ctx, project, payload, baseDomain)
	default:
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ignored PR action: %s", action)
	}
}

func (h *Handler) handlePullRequestClosed(w http.ResponseWriter, ctx context.Context, project queries.Project, prNumber int32) {
	pe, err := h.q.PreviewEnvironmentByProjectAndPR(ctx, queries.PreviewEnvironmentByProjectAndPRParams{
		ProjectID: project.ID,
		Provider:  "github",
		PrNumber:  prNumber,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "no preview environment for PR")
			return
		}
		slog.Error("PR close: load preview env", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	retention := previewRetentionSeconds(ctx, h.q)
	expires := time.Now().Add(time.Duration(retention) * time.Second)
	if _, err := h.q.PreviewEnvironmentMarkClosed(ctx, queries.PreviewEnvironmentMarkClosedParams{
		ID:        pe.ID,
		ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	}); err != nil {
		slog.Error("PR close: mark closed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	deps, err := preview.StopEnvironmentDeployments(ctx, h.q, h.deploymentReconciler, pe.ID)
	if err != nil {
		slog.Error("PR close: mark deployments stopped", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if retention == 0 {
		if err := h.q.PreviewEnvironmentDelete(ctx, pe.ID); err != nil {
			slog.Error("PR close: immediate cleanup", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		slog.Info("preview environment cleaned up immediately after close",
			"project", project.Name,
			"pr", prNumber,
			"deployments", len(deps),
		)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"status":      "deleted",
			"expires_at":  expires.UTC().Format(time.RFC3339Nano),
			"deployments": len(deps),
			"preview_env": pe.ID,
		})
		return
	}

	slog.Info("PR preview marked for cleanup", "project", project.Name, "pr", prNumber, "expires_at", expires, "deployments", len(deps))

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"status":      "closing",
		"expires_at":  expires.UTC().Format(time.RFC3339Nano),
		"deployments": len(deps),
		"preview_env": pe.ID,
	})
}

func (h *Handler) handlePullRequestSync(w http.ResponseWriter, ctx context.Context, project queries.Project, payload pullRequestEvent, baseDomain string) {
	headBranch := strings.TrimPrefix(payload.PullRequest.Head.Ref, "refs/heads/")
	sha := strings.TrimSpace(payload.PullRequest.Head.Sha)
	if sha == "" {
		http.Error(w, "missing head sha", http.StatusBadRequest)
		return
	}

	prNum := int32(payload.Number)
	service, err := h.q.ServicePrimaryByProjectID(ctx, project.ID)
	if err != nil {
		slog.Error("preview load primary service", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	stableHost := preview.StableHostname(payload.Number, service.Slug, project.Name, baseDomain)
	if stableHost == "" {
		http.Error(w, "invalid preview base domain", http.StatusBadRequest)
		return
	}

	var pe queries.PreviewEnvironment
	existing, err := h.q.PreviewEnvironmentByProjectAndPR(ctx, queries.PreviewEnvironmentByProjectAndPRParams{
		ProjectID: project.ID,
		Provider:  "github",
		PrNumber:  prNum,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			pe, err = h.q.PreviewEnvironmentCreate(ctx, queries.PreviewEnvironmentCreateParams{
				ID:               pgtype.UUID{Bytes: uuid.New(), Valid: true},
				ProjectID:        project.ID,
				ServiceID:        service.ID,
				Provider:         "github",
				PrNumber:         prNum,
				HeadBranch:       headBranch,
				HeadSha:          sha,
				StableDomainName: stableHost,
			})
			if err != nil {
				slog.Error("preview env create", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		} else {
			slog.Error("preview env load", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	} else {
		pe, err = h.q.PreviewEnvironmentReopen(ctx, queries.PreviewEnvironmentReopenParams{
			ID:         existing.ID,
			HeadBranch: headBranch,
			HeadSha:    sha,
		})
		if err != nil {
			slog.Error("preview env update head", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	dep, err := h.q.DeploymentCreate(ctx, queries.DeploymentCreateParams{
		ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:            project.ID,
		ServiceID:            service.ID,
		GithubCommit:         sha,
		GithubBranch:         headBranch,
		DeploymentKind:       "preview",
		PreviewEnvironmentID: pe.ID,
	})
	if err != nil {
		slog.Error("preview deployment create", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.q.PreviewEnvironmentSetLatestDeployment(ctx, queries.PreviewEnvironmentSetLatestDeploymentParams{
		ID:                 pe.ID,
		LatestDeploymentID: dep.ID,
	}); err != nil {
		slog.Error("preview set latest deployment", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	stableDom, err := h.q.DomainFindByPreviewEnvironmentAndKind(ctx, queries.DomainFindByPreviewEnvironmentAndKindParams{
		PreviewEnvironmentID: pe.ID,
		DomainKind:           "preview_stable",
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_, err = h.q.DomainCreatePreview(ctx, queries.DomainCreatePreviewParams{
				ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
				ProjectID:            project.ID,
				ServiceID:            service.ID,
				DeploymentID:         dep.ID,
				DomainName:           stableHost,
				DomainKind:           "preview_stable",
				PreviewEnvironmentID: pe.ID,
			})
			if err != nil {
				slog.Error("preview stable domain create", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		} else {
			slog.Error("preview stable domain lookup", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	} else {
		if err := h.q.DomainUpdateDeploymentForDomainID(ctx, queries.DomainUpdateDeploymentForDomainIDParams{
			ID:           stableDom.ID,
			DeploymentID: dep.ID,
		}); err != nil {
			slog.Error("preview stable domain update", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	slog.Info("preview deployment created from PR webhook",
		"deployment_id", dep.ID,
		"project", project.Name,
		"pr", prNum,
		"commit", sha,
	)

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"deployment_id": dep.ID,
		"project":       project.Name,
		"pr_number":     prNum,
		"commit":        sha,
		"stable_host":   stableHost,
	})
}

func verifySignature(payload []byte, signature, secret string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}

	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)

	return hmac.Equal(sig, expected)
}
