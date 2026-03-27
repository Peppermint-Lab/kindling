//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/preview"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/rpc"
	"github.com/kindlingvm/kindling/internal/webhook"
)

type previewHarness struct {
	db     *database.DB
	q      *queries.Queries
	client *http.Client
	server *httptest.Server
}

func newPreviewHarness(t *testing.T) *previewHarness {
	t.Helper()

	dsn := e2eDatabaseURL()
	if dsn == "" {
		t.Skip("set KINDLING_E2E_DATABASE_URL or DATABASE_URL to run integration tests")
	}
	ctx := context.Background()
	db, err := database.New(ctx, dsn)
	if err != nil {
		t.Skipf("postgres not reachable: %v", err)
	}
	t.Cleanup(db.Close)
	if err := database.Migrate(ctx, db.Pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	q := queries.New(db.Pool)
	api := rpc.NewAPI(q, nil, nil)
	webhookHandler := webhook.NewHandler(q)
	mux := http.NewServeMux()
	api.Register(mux)
	mux.Handle("POST /webhooks/github", webhookHandler)
	ts := httptest.NewServer(auth.Middleware(q, mux))
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	base, _ := url.Parse(ts.URL)
	if err := ensureSession(ctx, t, client, base, q); err != nil {
		t.Fatal(err)
	}

	return &previewHarness{
		db:     db,
		q:      q,
		client: client,
		server: ts,
	}
}

func createPreviewProject(t *testing.T, q *queries.Queries, repo string) pgtype.UUID {
	t.Helper()
	projectID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err := q.ProjectCreate(context.Background(), queries.ProjectCreateParams{
		ID:                   projectID,
		OrgID:                auth.PgUUID(auth.BootstrapOrganizationID),
		Name:                 "preview-e2e-" + repo[len(repo)-8:],
		GithubRepository:     repo,
		GithubInstallationID: 0,
		GithubWebhookSecret:  "",
		RootDirectory:        "/",
		DockerfilePath:       "Dockerfile",
		DesiredInstanceCount: 1,
	})
	if err != nil {
		t.Fatalf("project create: %v", err)
	}
	return projectID
}

func previewWebhookPayload(action, repo string, prNumber int, branch, sha string) []byte {
	body := map[string]any{
		"action": action,
		"number": prNumber,
		"pull_request": map[string]any{
			"head": map[string]any{
				"ref": branch,
				"sha": sha,
			},
		},
		"repository": map[string]any{
			"full_name": repo,
		},
	}
	raw, _ := json.Marshal(body)
	return raw
}

func postPreviewWebhook(t *testing.T, url string, payload []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status %d", resp.StatusCode)
	}
}

func TestPreviewLifecycle_WebhookAndAPIControls(t *testing.T) {
	h := newPreviewHarness(t)
	ctx := context.Background()
	repo := "acme/" + uuid.NewString()
	projectID := createPreviewProject(t, h.q, repo)
	t.Cleanup(func() {
		_, _ = h.db.Pool.Exec(context.Background(), `DELETE FROM cluster_settings WHERE key IN ('preview_base_domain', 'preview_retention_after_close_seconds')`)
		_, _ = h.db.Pool.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
	})

	if err := h.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
		Key:   "preview_base_domain",
		Value: "preview.example.com",
	}); err != nil {
		t.Fatalf("preview_base_domain: %v", err)
	}
	if err := h.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
		Key:   "preview_retention_after_close_seconds",
		Value: "600",
	}); err != nil {
		t.Fatalf("preview_retention_after_close_seconds: %v", err)
	}

	const prNumber = 42
	projectIDStr := uuid.UUID(projectID.Bytes).String()
	postPreviewWebhook(t, h.server.URL+"/webhooks/github", previewWebhookPayload("opened", repo, prNumber, "feature/preview", "aaaaaaaa11111111"))

	pe, err := h.q.PreviewEnvironmentByProjectAndPR(ctx, queries.PreviewEnvironmentByProjectAndPRParams{
		ProjectID: projectID,
		Provider:  "github",
		PrNumber:  prNumber,
	})
	if err != nil {
		t.Fatalf("preview env after open: %v", err)
	}
	if pe.ClosedAt.Valid || pe.ExpiresAt.Valid {
		t.Fatalf("preview should be active after open: %+v", pe)
	}

	body := getOK(t, h.client, h.server.URL+"/api/projects/"+projectIDStr+"/previews")
	var previews []struct {
		ID              string `json:"id"`
		LifecycleState  string `json:"lifecycle_state"`
		HeadSHA         string `json:"head_sha"`
		LatestDeployment struct {
			ID string `json:"id"`
		} `json:"latest_deployment"`
	}
	if err := json.Unmarshal(body, &previews); err != nil {
		t.Fatalf("list previews json: %v", err)
	}
	if len(previews) != 1 || previews[0].LifecycleState != "active" || previews[0].LatestDeployment.ID == "" {
		t.Fatalf("unexpected previews after open: %+v", previews)
	}
	firstDeploymentID := previews[0].LatestDeployment.ID

	resp, err := h.client.Post(h.server.URL+"/api/projects/"+projectIDStr+"/previews/"+previews[0].ID+"/redeploy", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("redeploy status %d", resp.StatusCode)
	}

	body = getOK(t, h.client, h.server.URL+"/api/projects/"+projectIDStr+"/previews")
	previews = nil
	if err := json.Unmarshal(body, &previews); err != nil {
		t.Fatalf("list previews after redeploy: %v", err)
	}
	if previews[0].LatestDeployment.ID == "" || previews[0].LatestDeployment.ID == firstDeploymentID {
		t.Fatalf("expected redeploy to advance latest deployment: %+v", previews[0])
	}

	postPreviewWebhook(t, h.server.URL+"/webhooks/github", previewWebhookPayload("closed", repo, prNumber, "feature/preview", "aaaaaaaa11111111"))

	body = getOK(t, h.client, h.server.URL+"/api/projects/"+projectIDStr+"/previews")
	previews = nil
	if err := json.Unmarshal(body, &previews); err != nil {
		t.Fatalf("list previews after close: %v", err)
	}
	if len(previews) != 1 || previews[0].LifecycleState != "closed" {
		t.Fatalf("expected closed preview after close webhook: %+v", previews)
	}

	postPreviewWebhook(t, h.server.URL+"/webhooks/github", previewWebhookPayload("reopened", repo, prNumber, "feature/preview", "bbbbbbbb22222222"))

	body = getOK(t, h.client, h.server.URL+"/api/projects/"+projectIDStr+"/previews")
	previews = nil
	if err := json.Unmarshal(body, &previews); err != nil {
		t.Fatalf("list previews after reopen: %v", err)
	}
	if len(previews) != 1 || previews[0].LifecycleState != "active" || previews[0].HeadSHA != "bbbbbbbb22222222" {
		t.Fatalf("expected active reopened preview: %+v", previews)
	}

	req, err := http.NewRequest(http.MethodDelete, h.server.URL+"/api/projects/"+projectIDStr+"/previews/"+previews[0].ID, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete preview status %d", resp.StatusCode)
	}

	body = getOK(t, h.client, h.server.URL+"/api/projects/"+projectIDStr+"/previews")
	previews = nil
	if err := json.Unmarshal(body, &previews); err != nil {
		t.Fatalf("list previews after delete: %v", err)
	}
	if len(previews) != 0 {
		t.Fatalf("expected preview list to be empty after delete: %+v", previews)
	}
	_, err = h.q.PreviewEnvironmentByProjectAndPR(ctx, queries.PreviewEnvironmentByProjectAndPRParams{
		ProjectID: projectID,
		Provider:  "github",
		PrNumber:  prNumber,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected preview env to be deleted, got err=%v", err)
	}
}

func TestPreviewCleanupAndIdleScaleDown_Integration(t *testing.T) {
	h := newPreviewHarness(t)
	ctx := context.Background()
	projectID := createPreviewProject(t, h.q, "acme/"+uuid.NewString())
	t.Cleanup(func() {
		_, _ = h.db.Pool.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
	})

	activeEnvID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	closedEnvID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	activeDepID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	closedDepID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	_, err := h.q.PreviewEnvironmentCreate(ctx, queries.PreviewEnvironmentCreateParams{
		ID:               activeEnvID,
		ProjectID:        projectID,
		Provider:         "github",
		PrNumber:         101,
		HeadBranch:       "feature/active",
		HeadSha:          "active111",
		StableDomainName: "pr-101-example.preview.example.com",
	})
	if err != nil {
		t.Fatalf("create active preview env: %v", err)
	}
	closedEnv, err := h.q.PreviewEnvironmentCreate(ctx, queries.PreviewEnvironmentCreateParams{
		ID:               closedEnvID,
		ProjectID:        projectID,
		Provider:         "github",
		PrNumber:         102,
		HeadBranch:       "feature/closed",
		HeadSha:          "closed222",
		StableDomainName: "pr-102-example.preview.example.com",
	})
	if err != nil {
		t.Fatalf("create closed preview env: %v", err)
	}
	if _, err := h.q.PreviewEnvironmentMarkClosed(ctx, queries.PreviewEnvironmentMarkClosedParams{
		ID:        closedEnv.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true},
	}); err != nil {
		t.Fatalf("mark closed preview env: %v", err)
	}

	_, err = h.q.DeploymentCreate(ctx, queries.DeploymentCreateParams{
		ID:                   activeDepID,
		ProjectID:            projectID,
		GithubCommit:         "active111",
		GithubBranch:         "feature/active",
		DeploymentKind:       "preview",
		PreviewEnvironmentID: activeEnvID,
	})
	if err != nil {
		t.Fatalf("create active preview deployment: %v", err)
	}
	if err := h.q.DeploymentMarkRunning(ctx, activeDepID); err != nil {
		t.Fatalf("mark active deployment running: %v", err)
	}

	_, err = h.q.DeploymentCreate(ctx, queries.DeploymentCreateParams{
		ID:                   closedDepID,
		ProjectID:            projectID,
		GithubCommit:         "closed222",
		GithubBranch:         "feature/closed",
		DeploymentKind:       "preview",
		PreviewEnvironmentID: closedEnvID,
	})
	if err != nil {
		t.Fatalf("create closed preview deployment: %v", err)
	}
	if err := h.q.DeploymentMarkRunning(ctx, closedDepID); err != nil {
		t.Fatalf("mark closed deployment running: %v", err)
	}

	_, err = h.db.Pool.Exec(ctx, `
UPDATE deployments
SET preview_last_request_at = NOW() - INTERVAL '10 minutes'
WHERE id = ANY($1)
`, []pgtype.UUID{activeDepID, closedDepID})
	if err != nil {
		t.Fatalf("seed preview_last_request_at: %v", err)
	}

	sched := reconciler.New(reconciler.Config{
		Name:         "preview-test",
		Reconcile:    func(context.Context, uuid.UUID) error { return nil },
		DefaultAfter: 24 * time.Hour,
	})

	preview.RunIdleScaleDownOnce(ctx, e2eDatabaseURL(), h.q, sched, 300)

	activeDep, err := h.q.DeploymentFirstByID(ctx, activeDepID)
	if err != nil {
		t.Fatalf("reload active deployment: %v", err)
	}
	closedDep, err := h.q.DeploymentFirstByID(ctx, closedDepID)
	if err != nil {
		t.Fatalf("reload closed deployment: %v", err)
	}
	if !activeDep.PreviewScaledToZero {
		t.Fatal("expected active preview deployment to scale to zero")
	}
	if closedDep.PreviewScaledToZero {
		t.Fatal("expected closed preview deployment to be ignored by idle scaler")
	}

	preview.RunCleanupOnce(ctx, e2eDatabaseURL(), h.q, sched)

	_, err = h.q.PreviewEnvironmentByID(ctx, closedEnvID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected expired preview env to be deleted, err=%v", err)
	}
	closedDep, err = h.q.DeploymentFirstByID(ctx, closedDepID)
	if err != nil {
		t.Fatalf("reload cleaned deployment: %v", err)
	}
	if !closedDep.StoppedAt.Valid {
		t.Fatal("expected cleaned preview deployment to be stopped")
	}
}
