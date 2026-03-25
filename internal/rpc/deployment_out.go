package rpc

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// deploymentOut is the JSON shape for deployment resources (API v0.2).
type deploymentOut struct {
	ID           string  `json:"id"`
	ProjectID    string  `json:"project_id"`
	BuildID      *string `json:"build_id,omitempty"`
	ImageID      *string `json:"image_id,omitempty"`
	VmID         *string `json:"vm_id,omitempty"`
	GithubCommit string  `json:"github_commit"`
	RunningAt    *string `json:"running_at"`
	StoppedAt    *string `json:"stopped_at"`
	FailedAt     *string `json:"failed_at"`
	CreatedAt    *string `json:"created_at"`
	UpdatedAt    *string `json:"updated_at"`
	BuildStatus  string  `json:"build_status,omitempty"`
	Phase        string  `json:"phase"`
}

type deploymentListItemOut struct {
	deploymentOut
	ProjectName string `json:"project_name"`
}

func formatTS(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(time.RFC3339Nano)
	return &s
}

func optionalUUIDString(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := pgUUIDToString(u)
	return &s
}

func deploymentToOut(dep queries.Deployment, build *queries.Build) deploymentOut {
	var bs string
	if build != nil {
		bs = build.Status
	}
	return deploymentOut{
		ID:           pgUUIDToString(dep.ID),
		ProjectID:    pgUUIDToString(dep.ProjectID),
		BuildID:      optionalUUIDString(dep.BuildID),
		ImageID:      optionalUUIDString(dep.ImageID),
		VmID:         optionalUUIDString(dep.VmID),
		GithubCommit: dep.GithubCommit,
		RunningAt:    formatTS(dep.RunningAt),
		StoppedAt:    formatTS(dep.StoppedAt),
		FailedAt:     formatTS(dep.FailedAt),
		CreatedAt:    formatTS(dep.CreatedAt),
		UpdatedAt:    formatTS(dep.UpdatedAt),
		BuildStatus:  bs,
		Phase:        deploymentPhase(dep, build),
	}
}

func (a *API) deploymentToOutCtx(ctx context.Context, dep queries.Deployment) deploymentOut {
	var build *queries.Build
	if dep.BuildID.Valid {
		b, err := a.q.BuildFirstByID(ctx, dep.BuildID)
		if err == nil {
			build = &b
		}
	}
	return deploymentToOut(dep, build)
}

func listRowToOut(row queries.DeploymentFindRecentWithProjectRow) deploymentListItemOut {
	st := pgTextString(row.BuildStatus)
	if row.BuildID.Valid && st == "" {
		st = "pending"
	}
	var buildPtr *queries.Build
	if row.BuildID.Valid {
		buildPtr = &queries.Build{Status: st}
	}
	dep := queries.Deployment{
		ID:           row.ID,
		ProjectID:    row.ProjectID,
		BuildID:      row.BuildID,
		ImageID:      row.ImageID,
		VmID:         row.VmID,
		GithubCommit: row.GithubCommit,
		RunningAt:    row.RunningAt,
		StoppedAt:    row.StoppedAt,
		FailedAt:     row.FailedAt,
		DeletedAt:    row.DeletedAt,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
	out := deploymentToOut(dep, buildPtr)
	return deploymentListItemOut{deploymentOut: out, ProjectName: row.ProjectName}
}
