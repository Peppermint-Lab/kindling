// Package webhook handles GitHub webhook events to trigger deployments.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
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
	HeadCommit struct {
		Message string `json:"message"`
	} `json:"head_commit"`
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

	// Only handle push events for now.
	if event != "push" {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ignored event: %s", event)
		return
	}

	var push pushEvent
	if err := json.Unmarshal(body, &push); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Only deploy pushes to default branch (refs/heads/main or refs/heads/master).
	branch := strings.TrimPrefix(push.Ref, "refs/heads/")
	if branch != "main" && branch != "master" {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ignored branch: %s", branch)
		return
	}

	repo := push.Repository.FullName
	commit := push.After

	slog.Info("GitHub push received", "repo", repo, "branch", branch, "commit", commit)

	// Find the project.
	project, err := h.q.ProjectFindByGitHubRepo(r.Context(), repo)
	if err != nil {
		slog.Warn("webhook for unknown repo", "repo", repo)
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	// Verify webhook signature if secret is configured.
	if project.GithubWebhookSecret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifySignature(body, sig, project.GithubWebhookSecret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Create deployment.
	dep, err := h.q.DeploymentCreate(r.Context(), queries.DeploymentCreateParams{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:    project.ID,
		GithubCommit: commit,
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
