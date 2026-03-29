package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// ── stub infrastructure ──────────────────────────────────────────────

// stubRow implements pgx.Row for tests that need QueryRow.
type stubRow struct {
	project *queries.Project
	err     error
}

func (r *stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.project == nil {
		return pgx.ErrNoRows
	}
	p := r.project
	// Match the field order from the projectFindByGitHubRepo query:
	// id, org_id, name, github_repository, github_installation_id,
	// github_webhook_secret, root_directory, dockerfile_path,
	// desired_instance_count, min_instance_count, max_instance_count,
	// last_request_at, scaled_to_zero, scale_to_zero_enabled,
	// build_only_on_root_changes, created_at, updated_at
	if len(dest) < 17 {
		return fmt.Errorf("stubRow: expected 17 scan targets, got %d", len(dest))
	}
	*dest[0].(*pgtype.UUID) = p.ID
	*dest[1].(*pgtype.UUID) = p.OrgID
	*dest[2].(*string) = p.Name
	*dest[3].(*string) = p.GithubRepository
	*dest[4].(*int64) = p.GithubInstallationID
	*dest[5].(*string) = p.GithubWebhookSecret
	*dest[6].(*string) = p.RootDirectory
	*dest[7].(*string) = p.DockerfilePath
	*dest[8].(*int32) = p.DesiredInstanceCount
	*dest[9].(*int32) = p.MinInstanceCount
	*dest[10].(*int32) = p.MaxInstanceCount
	*dest[11].(*pgtype.Timestamptz) = p.LastRequestAt
	*dest[12].(*bool) = p.ScaledToZero
	*dest[13].(*bool) = p.ScaleToZeroEnabled
	*dest[14].(*bool) = p.BuildOnlyOnRootChanges
	*dest[15].(*pgtype.Timestamptz) = p.CreatedAt
	*dest[16].(*pgtype.Timestamptz) = p.UpdatedAt
	return nil
}

// stubDBTX is a minimal DBTX implementation used for handler-level tests.
// It routes QueryRow calls for the project-find query to the configured stub.
type stubDBTX struct {
	project *queries.Project // nil → ErrNoRows
}

func (s *stubDBTX) Exec(_ context.Context, _ string, _ ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (s *stubDBTX) Query(_ context.Context, _ string, _ ...interface{}) (pgx.Rows, error) {
	return nil, fmt.Errorf("stubDBTX: Query not implemented")
}

func (s *stubDBTX) QueryRow(_ context.Context, sql string, _ ...interface{}) pgx.Row {
	// Return project stub for any QueryRow call (used by ProjectFindByGitHubRepo).
	return &stubRow{project: s.project}
}

// newTestHandler creates a Handler with a stubbed database that returns
// the given project for ProjectFindByGitHubRepo calls.
func newTestHandler(project *queries.Project) *Handler {
	q := queries.New(&stubDBTX{project: project})
	return &Handler{q: q}
}

// signBody computes HMAC-SHA256 of body with key and returns the
// "sha256=<hex>" header value.
func signBody(t *testing.T, key string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// makePayload builds a minimal JSON webhook payload with the given repo and
// optional extra fields merged in.
func makePayload(repo string, extra map[string]any) []byte {
	m := map[string]any{
		"repository": map[string]any{"full_name": repo},
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// testProject returns a queries.Project with the given repo and secret.
func testProject(repoName, secret string) *queries.Project {
	return &queries.Project{
		ID:                   pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		OrgID:                pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		Name:                 "test-project",
		GithubRepository:     repoName,
		GithubWebhookSecret:  secret,
		RootDirectory:        "/",
		DockerfilePath:       "Dockerfile",
		DesiredInstanceCount: 1,
		MinInstanceCount:     0,
		MaxInstanceCount:     3,
	}
}

// ── Tests: centralized signature ordering ────────────────────────────

// TestServeHTTP_UnknownEvent_UnsignedReturns401 verifies that unknown events
// no longer silently return 200 without signature verification.
func TestServeHTTP_UnknownEvent_UnsignedReturns401(t *testing.T) {
	t.Parallel()

	secret := "my-secret"
	h := newTestHandler(testProject("org/repo", secret))
	body := makePayload("org/repo", nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "deployment_status") // unknown event
	// No signature header set.

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned unknown event: expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestServeHTTP_UnknownEvent_TamperedReturns403 verifies that unknown events
// with a tampered signature return 403.
func TestServeHTTP_UnknownEvent_TamperedReturns403(t *testing.T) {
	t.Parallel()

	secret := "my-secret"
	h := newTestHandler(testProject("org/repo", secret))
	body := makePayload("org/repo", nil)

	wrongSig := signBody(t, "wrong-secret", body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "deployment_status")
	req.Header.Set("X-Hub-Signature-256", wrongSig)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("tampered unknown event: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestServeHTTP_UnknownEvent_ValidSignatureReturns200 verifies that unknown
// events with a valid signature return 200 "ignored event".
func TestServeHTTP_UnknownEvent_ValidSignatureReturns200(t *testing.T) {
	t.Parallel()

	secret := "my-secret"
	h := newTestHandler(testProject("org/repo", secret))
	body := makePayload("org/repo", nil)

	sig := signBody(t, secret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "deployment_status")
	req.Header.Set("X-Hub-Signature-256", sig)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid unknown event: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ignored event") {
		t.Fatalf("expected 'ignored event' in body, got %q", rec.Body.String())
	}
}

// TestServeHTTP_Push_NonMainBranch_UnsignedReturns401 verifies that push
// events for non-main branches are NOT returned as 200 before signature check.
func TestServeHTTP_Push_NonMainBranch_UnsignedReturns401(t *testing.T) {
	t.Parallel()

	secret := "my-secret"
	h := newTestHandler(testProject("org/repo", secret))
	body := makePayload("org/repo", map[string]any{
		"ref":   "refs/heads/feature-branch",
		"after": "abc123",
	})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")
	// No signature.

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned non-main push: expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestServeHTTP_Push_NonMainBranch_TamperedReturns403 verifies tampered
// pushes for non-main branches are rejected with 403.
func TestServeHTTP_Push_NonMainBranch_TamperedReturns403(t *testing.T) {
	t.Parallel()

	secret := "my-secret"
	h := newTestHandler(testProject("org/repo", secret))
	body := makePayload("org/repo", map[string]any{
		"ref":   "refs/heads/feature-branch",
		"after": "abc123",
	})

	wrongSig := signBody(t, "wrong-secret", body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", wrongSig)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("tampered non-main push: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestServeHTTP_Push_NonMainBranch_ValidSigReturns200 verifies that push
// events for non-main branches return 200 "ignored branch" AFTER signature
// verification passes.
func TestServeHTTP_Push_NonMainBranch_ValidSigReturns200(t *testing.T) {
	t.Parallel()

	secret := "my-secret"
	h := newTestHandler(testProject("org/repo", secret))
	body := makePayload("org/repo", map[string]any{
		"ref":   "refs/heads/feature-branch",
		"after": "abc123",
	})

	sig := signBody(t, secret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sig)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid non-main push: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ignored branch") {
		t.Fatalf("expected 'ignored branch' in body, got %q", rec.Body.String())
	}
}

// TestServeHTTP_BlankSecret_AllEventsRejected verifies that projects with
// blank webhook secrets reject ALL event types with 401.
func TestServeHTTP_BlankSecret_AllEventsRejected(t *testing.T) {
	t.Parallel()

	events := []string{"push", "pull_request", "deployment_status", "ping"}
	for _, event := range events {
		t.Run(event, func(t *testing.T) {
			t.Parallel()

			h := newTestHandler(testProject("org/repo", "")) // blank secret
			body := makePayload("org/repo", map[string]any{
				"ref":   "refs/heads/main",
				"after": "abc123",
			})

			req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
			req.Header.Set("X-GitHub-Event", event)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("blank secret for %s: expected 401, got %d body=%q", event, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestServeHTTP_UnknownRepo_Returns404 verifies that webhooks for repos not
// connected to any project return 404.
func TestServeHTTP_UnknownRepo_Returns404(t *testing.T) {
	t.Parallel()

	h := newTestHandler(nil) // nil → project not found (ErrNoRows)
	body := makePayload("org/unknown-repo", nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown repo: expected 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestServeHTTP_MissingRepository_Returns400 verifies that payloads without
// repository.full_name are rejected.
func TestServeHTTP_MissingRepository_Returns400(t *testing.T) {
	t.Parallel()

	h := newTestHandler(testProject("org/repo", "secret"))
	body := []byte(`{"ref":"refs/heads/main"}`) // no repository field

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing repo: expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestServeHTTP_InvalidJSON_Returns400 verifies that non-JSON bodies are
// rejected before any other processing.
func TestServeHTTP_InvalidJSON_Returns400(t *testing.T) {
	t.Parallel()

	h := newTestHandler(testProject("org/repo", "secret"))
	body := []byte(`not json at all`)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON: expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestServeHTTP_SignatureVerifiedBeforeEventDispatch verifies the ordering
// invariant: for EVERY event type, signature verification runs before any
// event-specific handler. We confirm this by ensuring unsigned requests NEVER
// return event-handler responses (like "ignored branch" or "ignored event").
func TestServeHTTP_SignatureVerifiedBeforeEventDispatch(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	tests := []struct {
		name  string
		event string
		body  []byte
	}{
		{
			name:  "push/main",
			event: "push",
			body: makePayload("org/repo", map[string]any{
				"ref":   "refs/heads/main",
				"after": "abc123",
			}),
		},
		{
			name:  "push/feature",
			event: "push",
			body: makePayload("org/repo", map[string]any{
				"ref":   "refs/heads/feature",
				"after": "abc123",
			}),
		},
		{
			name:  "pull_request",
			event: "pull_request",
			body: makePayload("org/repo", map[string]any{
				"action": "opened",
				"number": 1,
				"pull_request": map[string]any{
					"head": map[string]any{"ref": "feature", "sha": "abc"},
				},
			}),
		},
		{
			name:  "unknown_event",
			event: "check_run",
			body:  makePayload("org/repo", nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestHandler(testProject("org/repo", secret))

			// Test with NO signature → 401
			req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(tt.body)))
			req.Header.Set("X-GitHub-Event", tt.event)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("unsigned %s: expected 401, got %d body=%q", tt.name, rec.Code, rec.Body.String())
			}

			// Test with WRONG signature → 403
			wrongSig := signBody(t, "wrong-key", tt.body)
			req2 := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(tt.body)))
			req2.Header.Set("X-GitHub-Event", tt.event)
			req2.Header.Set("X-Hub-Signature-256", wrongSig)
			rec2 := httptest.NewRecorder()
			h.ServeHTTP(rec2, req2)
			if rec2.Code != http.StatusForbidden {
				t.Fatalf("tampered %s: expected 403, got %d body=%q", tt.name, rec2.Code, rec2.Body.String())
			}
		})
	}
}

// TestServeHTTP_No200ForUnsignedRequests is a comprehensive check that no
// combination of event type and payload can yield a 200 OK response when the
// request is unsigned.
func TestServeHTTP_No200ForUnsignedRequests(t *testing.T) {
	t.Parallel()

	secret := "the-secret"
	h := newTestHandler(testProject("org/repo", secret))

	payloads := []struct {
		event string
		body  []byte
	}{
		{"push", makePayload("org/repo", map[string]any{"ref": "refs/heads/main", "after": "abc"})},
		{"push", makePayload("org/repo", map[string]any{"ref": "refs/heads/develop", "after": "abc"})},
		{"pull_request", makePayload("org/repo", map[string]any{"action": "opened", "number": 1, "pull_request": map[string]any{"head": map[string]any{"ref": "f", "sha": "a"}}})},
		{"deployment_status", makePayload("org/repo", nil)},
		{"ping", makePayload("org/repo", nil)},
		{"issues", makePayload("org/repo", nil)},
	}

	for _, p := range payloads {
		t.Run(p.event, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(p.body)))
			req.Header.Set("X-GitHub-Event", p.event)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
				t.Fatalf("unsigned %s request got %d (expected non-success), body=%q",
					p.event, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestServeHTTP_MethodNotAllowed verifies only POST is accepted.
func TestServeHTTP_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	h := newTestHandler(testProject("org/repo", "secret"))

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(method, "/webhooks/github", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s: expected 405, got %d", method, rec.Code)
			}
		})
	}
}

// TestServeHTTP_MissingEventHeader verifies missing X-GitHub-Event returns 400.
func TestServeHTTP_MissingEventHeader(t *testing.T) {
	t.Parallel()

	h := newTestHandler(testProject("org/repo", "secret"))
	body := makePayload("org/repo", nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	// No X-GitHub-Event header.

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing event header: expected 400, got %d", rec.Code)
	}
}

// TestServeHTTP_ProjectLookupBeforeSignature verifies that project lookup
// happens before signature verification (necessary to get per-project secret)
// but no business logic runs before verification. We confirm by checking that
// an unknown repo gets 404 even without a signature (the project lookup failure
// happens before we'd check the signature).
func TestServeHTTP_ProjectLookupBeforeSignature(t *testing.T) {
	t.Parallel()

	h := newTestHandler(nil) // project not found
	body := makePayload("org/unknown-repo", nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")
	// No signature — but we should get 404 (not 401) because project lookup
	// necessarily precedes per-project signature verification.

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("project lookup ordering: expected 404 for unknown repo, got %d body=%q",
			rec.Code, rec.Body.String())
	}
}

// TestServeHTTP_PullRequest_UnsignedReturns401 verifies pull_request events
// are rejected when unsigned.
func TestServeHTTP_PullRequest_UnsignedReturns401(t *testing.T) {
	t.Parallel()

	secret := "pr-secret"
	h := newTestHandler(testProject("org/repo", secret))
	body := makePayload("org/repo", map[string]any{
		"action":       "opened",
		"number":       42,
		"pull_request": map[string]any{"head": map[string]any{"ref": "feat", "sha": "abc"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned pull_request: expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestServeHTTP_BlankSecretRejectsEmptyKeyHMAC_FullHandler verifies that at
// the handler level, a project with blank secret rejects HMAC computed with
// empty key. This is the handler-level version of
// TestEnforceSignature_BlankSecretRejectsEmptyKeyHMAC.
func TestServeHTTP_BlankSecretRejectsEmptyKeyHMAC_FullHandler(t *testing.T) {
	t.Parallel()

	h := newTestHandler(testProject("org/repo", "")) // blank secret
	body := makePayload("org/repo", map[string]any{
		"ref":   "refs/heads/main",
		"after": "abc123",
	})

	// Compute HMAC with empty key.
	emptyKeySig := signBody(t, "", body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", emptyKeySig)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("blank secret with empty-key HMAC: expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}
