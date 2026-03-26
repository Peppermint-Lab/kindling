//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/rpc"
	"github.com/kindlingvm/kindling/internal/serverreconcile"
)

func e2eDatabaseURL() string {
	if u := strings.TrimSpace(os.Getenv("KINDLING_E2E_DATABASE_URL")); u != "" {
		return u
	}
	return strings.TrimSpace(os.Getenv("DATABASE_URL"))
}

func TestDrainServer_HTTPAndReconciler(t *testing.T) {
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

	serverA := uuid.New()
	serverB := uuid.New()
	depID := uuid.New()
	projID := uuid.New()
	instID := uuid.New()

	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Pool.Exec(cctx, `DELETE FROM deployment_instances WHERE id = $1`, pgtype.UUID{Bytes: instID, Valid: true})
		_, _ = db.Pool.Exec(cctx, `DELETE FROM deployments WHERE id = $1`, pgtype.UUID{Bytes: depID, Valid: true})
		_, _ = db.Pool.Exec(cctx, `DELETE FROM projects WHERE id = $1`, pgtype.UUID{Bytes: projID, Valid: true})
		_, _ = db.Pool.Exec(cctx, `DELETE FROM servers WHERE id = ANY($1)`, []pgtype.UUID{
			{Bytes: serverA, Valid: true},
			{Bytes: serverB, Valid: true},
		})
	})

	_, err = db.Pool.Exec(ctx, `
INSERT INTO servers (id, hostname, internal_ip, ip_range, status, last_heartbeat_at)
VALUES ($1, 'e2e-drain-a', '127.0.0.1', '10.100.0.0/20'::cidr, 'active', NOW()),
       ($2, 'e2e-drain-b', '127.0.0.2', '10.100.16.0/20'::cidr, 'active', NOW())`,
		pgtype.UUID{Bytes: serverA, Valid: true},
		pgtype.UUID{Bytes: serverB, Valid: true},
	)
	if err != nil {
		t.Fatalf("seed servers: %v", err)
	}

	_, err = q.ProjectCreate(ctx, queries.ProjectCreateParams{
		ID:                   pgtype.UUID{Bytes: projID, Valid: true},
		OrgID:                auth.PgUUID(auth.BootstrapOrganizationID),
		Name:                 "e2e-drain-proj",
		GithubRepository:     "",
		GithubInstallationID: 0,
		GithubWebhookSecret:  "",
		RootDirectory:        "/",
		DockerfilePath:       "Dockerfile",
		DesiredInstanceCount: 1,
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	_, err = q.DeploymentCreate(ctx, queries.DeploymentCreateParams{
		ID:           pgtype.UUID{Bytes: depID, Valid: true},
		ProjectID:    pgtype.UUID{Bytes: projID, Valid: true},
		GithubCommit: "deadbeef",
	})
	if err != nil {
		t.Fatalf("deployment: %v", err)
	}
	if err := q.DeploymentMarkRunning(ctx, pgtype.UUID{Bytes: depID, Valid: true}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	_, err = q.DeploymentInstanceCreate(ctx, queries.DeploymentInstanceCreateParams{
		ID:           pgtype.UUID{Bytes: instID, Valid: true},
		DeploymentID: pgtype.UUID{Bytes: depID, Valid: true},
	})
	if err != nil {
		t.Fatalf("instance: %v", err)
	}
	_, err = q.DeploymentInstanceUpdateServer(ctx, queries.DeploymentInstanceUpdateServerParams{
		ID:       pgtype.UUID{Bytes: instID, Valid: true},
		ServerID: pgtype.UUID{Bytes: serverA, Valid: true},
	})
	if err != nil {
		t.Fatalf("instance server: %v", err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

	api := rpc.NewAPI(q, nil)
	mux := http.NewServeMux()
	api.Register(mux)
	handler := auth.Middleware(q, mux)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	base, _ := url.Parse(ts.URL)

	if err := ensureSession(ctx, t, client, base, q); err != nil {
		t.Fatal(err)
	}

	// --- List servers (auth)
	listBody := getOK(t, client, ts.URL+"/api/servers")
	var listed []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatalf("list servers json: %v", err)
	}
	foundA := false
	for _, row := range listed {
		if row.ID == serverA.String() && row.Status == "active" {
			foundA = true
		}
	}
	if !foundA {
		t.Fatalf("expected server A in list, got %#v", listed)
	}

	// --- Drain server A
	drainResp, err := client.Post(ts.URL+"/api/servers/"+serverA.String()+"/drain", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	drainResp.Body.Close()
	if drainResp.StatusCode != http.StatusOK {
		t.Fatalf("drain status %d", drainResp.StatusCode)
	}
	srvA, err := q.ServerFindByID(ctx, pgtype.UUID{Bytes: serverA, Valid: true})
	if err != nil || srvA.Status != "draining" {
		t.Fatalf("server A after drain: %+v err %v", srvA, err)
	}

	// --- Server reconciler schedules deployments that still have instances on A
	var scheduled uuid.UUID
	var seen bool
	depSched := reconciler.New(reconciler.Config{
		Name: "deployment-e2e",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			if !seen {
				scheduled = id
				seen = true
			}
			return nil
		},
		DefaultAfter: 24 * time.Hour,
	})
	deployCtx, cancelDeploy := context.WithCancel(ctx)
	defer cancelDeploy()
	go depSched.Start(deployCtx)
	time.Sleep(50 * time.Millisecond)

	h := serverreconcile.NewHandler(q, depSched, func() {})
	if err := h.Reconcile(ctx, serverA); err != nil {
		t.Fatalf("server reconcile: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if seen && scheduled == depID {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !seen || scheduled != depID {
		t.Fatalf("expected deployment %s to be scheduled, got seen=%v id=%v", depID, seen, scheduled)
	}

	// --- Activate server A back to active (cleanup operator path)
	actResp, err := client.Post(ts.URL+"/api/servers/"+serverA.String()+"/activate", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	actResp.Body.Close()
	if actResp.StatusCode != http.StatusOK {
		t.Fatalf("activate status %d", actResp.StatusCode)
	}
	srvA2, err := q.ServerFindByID(ctx, pgtype.UUID{Bytes: serverA, Valid: true})
	if err != nil || srvA2.Status != "active" {
		t.Fatalf("server A after activate: %+v err %v", srvA2, err)
	}
}

func ensureSession(ctx context.Context, t *testing.T, client *http.Client, base *url.URL, q *queries.Queries) error {
	t.Helper()
	statusURL := base.ResolveReference(&url.URL{Path: "/api/auth/bootstrap-status"}).String()
	resp, err := client.Get(statusURL)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var st struct {
		NeedsBootstrap bool `json:"needs_bootstrap"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return err
	}
	if st.NeedsBootstrap {
		payload := []byte(`{"email":"e2e-drain@kindling.local","password":"e2e-drain-password-9chars","display_name":"E2E"}`)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.ResolveReference(&url.URL{Path: "/api/auth/bootstrap"}).String(), bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("bootstrap: %d %s", resp.StatusCode, string(b))
		}
		return nil
	}
	email := strings.TrimSpace(os.Getenv("KINDLING_E2E_EMAIL"))
	pass := os.Getenv("KINDLING_E2E_PASSWORD")
	if email == "" || pass == "" {
		t.Skip("cluster already bootstrapped; set KINDLING_E2E_EMAIL and KINDLING_E2E_PASSWORD or use an empty DB (e.g. kindling_e2e)")
	}
	loginPayload, err := json.Marshal(map[string]string{"email": email, "password": pass})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.ResolveReference(&url.URL{Path: "/api/auth/login"}).String(), bytes.NewReader(loginPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("login: %d %s", resp2.StatusCode, string(b))
	}
	return nil
}

func getOK(t *testing.T, client *http.Client, url string) []byte {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, string(b))
	}
	return b
}
