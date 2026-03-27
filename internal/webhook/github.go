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
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/preview"
)

// Handler handles GitHub webhook requests.
type Handler struct {
	q *queries.Queries
}

// NewHandler creates a new webhook handler.
func NewHandler(q *queries.Queries) *Handler {
	return &Handler{q: q}
}

// pushEvent is the relevant subset of GitHub's push event payload.
type pushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type pullRequestEvent struct {
	Action       string `json:"action"`
	Number       int    `json:"number"`
	PullRequest  prBody `json:"pull_request"`
	Repository   repo   `json:"repository"`
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
	default:
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ignored event: %s", event)
	}
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

	dep, err := h.q.DeploymentCreate(r.Context(), queries.DeploymentCreateParams{
		ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:            project.ID,
		GithubCommit:         commit,
		GithubBranch:         branch,
		DeploymentKind:       "production",
		PreviewEnvironmentID: pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Error("failed to create deployment", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("deployment created from webhook",
		"deployment_id", dep.ID,
		"project", project.Name,
		"commit", commit,
	)

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"deployment_id": dep.ID,
		"project":       project.Name,
		"commit":        commit,
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

	deps, err := h.q.DeploymentsByPreviewEnvironmentID(ctx, pe.ID)
	if err != nil {
		slog.Error("PR close: list deployments", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.q.DeploymentsMarkStoppedByPreviewEnvironment(ctx, pe.ID); err != nil {
		slog.Error("PR close: mark deployments stopped", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("PR preview marked for cleanup", "project", project.Name, "pr", prNumber, "expires_at", expires, "deployments", len(deps))

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "closing",
		"expires_at":   expires.UTC().Format(time.RFC3339Nano),
		"deployments":  len(deps),
		"preview_env":  pe.ID,
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
	stableHost := preview.StableHostname(payload.Number, project.Name, baseDomain)
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
				ProjectID:      project.ID,
				Provider:       "github",
				PrNumber:       prNum,
				HeadBranch:     headBranch,
				HeadSha:        sha,
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
		pe, err = h.q.PreviewEnvironmentUpdateHead(ctx, queries.PreviewEnvironmentUpdateHeadParams{
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
