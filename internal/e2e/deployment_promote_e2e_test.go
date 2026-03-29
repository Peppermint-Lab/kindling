//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestPromoteDeployment_ReusesHistoricalProductionRevision(t *testing.T) {
	h := newPreviewHarness(t)
	ctx := context.Background()
	repo := "acme/" + uuid.NewString()
	projectID := createPreviewProject(t, h.q, repo)
	service, err := h.q.ServicePrimaryByProjectID(ctx, projectID)
	if err != nil {
		t.Fatalf("primary service: %v", err)
	}

	imageA, err := h.q.ImageFindOrCreate(ctx, queries.ImageFindOrCreateParams{
		ID:         pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Registry:   "registry.example.com",
		Repository: "kindling/e2e-" + uuid.NewString(),
		Tag:        "rollback-a",
	})
	if err != nil {
		t.Fatalf("image A: %v", err)
	}
	imageB, err := h.q.ImageFindOrCreate(ctx, queries.ImageFindOrCreateParams{
		ID:         pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Registry:   "registry.example.com",
		Repository: "kindling/e2e-" + uuid.NewString(),
		Tag:        "rollback-b",
	})
	if err != nil {
		t.Fatalf("image B: %v", err)
	}
	t.Cleanup(func() {
		_, _ = h.db.Pool.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = h.db.Pool.Exec(context.Background(), `DELETE FROM images WHERE id = ANY($1)`, []pgtype.UUID{imageA.ID, imageB.ID})
	})

	buildA, err := h.q.BuildCreate(ctx, queries.BuildCreateParams{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:    projectID,
		ServiceID:    service.ID,
		Status:       "pending",
		GithubCommit: "aaaaaaaa11111111",
		GithubBranch: "main",
	})
	if err != nil {
		t.Fatalf("build A: %v", err)
	}
	if err := h.q.BuildMarkSuccessful(ctx, queries.BuildMarkSuccessfulParams{
		ID:      buildA.ID,
		ImageID: imageA.ID,
	}); err != nil {
		t.Fatalf("build A success: %v", err)
	}

	buildB, err := h.q.BuildCreate(ctx, queries.BuildCreateParams{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:    projectID,
		ServiceID:    service.ID,
		Status:       "pending",
		GithubCommit: "bbbbbbbb22222222",
		GithubBranch: "main",
	})
	if err != nil {
		t.Fatalf("build B: %v", err)
	}
	if err := h.q.BuildMarkSuccessful(ctx, queries.BuildMarkSuccessfulParams{
		ID:      buildB.ID,
		ImageID: imageB.ID,
	}); err != nil {
		t.Fatalf("build B success: %v", err)
	}

	targetDep, err := h.q.DeploymentCreate(ctx, queries.DeploymentCreateParams{
		ID:                       pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:                projectID,
		ServiceID:                service.ID,
		BuildID:                  buildA.ID,
		ImageID:                  imageA.ID,
		PromotedFromDeploymentID: pgtype.UUID{Valid: false},
		GithubCommit:             "aaaaaaaa11111111",
		GithubBranch:             "main",
		DeploymentKind:           "production",
		PreviewEnvironmentID:     pgtype.UUID{Valid: false},
	})
	if err != nil {
		t.Fatalf("target deployment: %v", err)
	}
	if err := h.q.DeploymentMarkRunning(ctx, targetDep.ID); err != nil {
		t.Fatalf("target running: %v", err)
	}
	if err := h.q.DeploymentMarkStopped(ctx, targetDep.ID); err != nil {
		t.Fatalf("target stopped: %v", err)
	}

	activeDep, err := h.q.DeploymentCreate(ctx, queries.DeploymentCreateParams{
		ID:                       pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:                projectID,
		ServiceID:                service.ID,
		BuildID:                  buildB.ID,
		ImageID:                  imageB.ID,
		PromotedFromDeploymentID: pgtype.UUID{Valid: false},
		GithubCommit:             "bbbbbbbb22222222",
		GithubBranch:             "main",
		DeploymentKind:           "production",
		PreviewEnvironmentID:     pgtype.UUID{Valid: false},
	})
	if err != nil {
		t.Fatalf("active deployment: %v", err)
	}
	if err := h.q.DeploymentMarkRunning(ctx, activeDep.ID); err != nil {
		t.Fatalf("active running: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/deployments/"+uuid.UUID(targetDep.ID.Bytes).String()+"/promote", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.server.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("promote status %d", resp.StatusCode)
	}

	var created struct {
		ID                       string `json:"id"`
		ProjectID                string `json:"project_id"`
		ServiceID                string `json:"service_id"`
		BuildID                  string `json:"build_id"`
		ImageID                  string `json:"image_id"`
		PromotedFromDeploymentID string `json:"promoted_from_deployment_id"`
		GithubCommit             string `json:"github_commit"`
		DeploymentKind           string `json:"deployment_kind"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode promote response: %v", err)
	}
	if created.PromotedFromDeploymentID != uuid.UUID(targetDep.ID.Bytes).String() {
		t.Fatalf("unexpected promoted_from_deployment_id: %+v", created)
	}
	if created.BuildID != uuid.UUID(buildA.ID.Bytes).String() || created.ImageID != uuid.UUID(imageA.ID.Bytes).String() {
		t.Fatalf("expected promote to reuse build/image, got %+v", created)
	}
	if created.ServiceID != uuid.UUID(service.ID.Bytes).String() || created.ProjectID != uuid.UUID(projectID.Bytes).String() {
		t.Fatalf("expected promote to preserve project/service, got %+v", created)
	}
	if created.GithubCommit != "aaaaaaaa11111111" || created.DeploymentKind != "production" {
		t.Fatalf("unexpected promote response: %+v", created)
	}

	invalidReq, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/deployments/"+uuid.UUID(activeDep.ID.Bytes).String()+"/promote", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	invalidReq.Header.Set("Content-Type", "application/json")
	invalidReq.Header.Set("Origin", h.server.URL)
	invalidResp, err := h.client.Do(invalidReq)
	if err != nil {
		t.Fatal(err)
	}
	defer invalidResp.Body.Close()
	if invalidResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected active deployment promote conflict, got %d", invalidResp.StatusCode)
	}
}
