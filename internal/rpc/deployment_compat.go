package rpc

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/rpc/deployments"
	"github.com/kindlingvm/kindling/internal/rpc/rpcutil"
)

// Thin wrappers that delegate to the deployments sub-package, for callers still in root rpc.

func formatTS(t pgtype.Timestamptz) *string { return rpcutil.FormatTS(t) }

func optionalUUIDString(u pgtype.UUID) *string { return rpcutil.OptionalUUIDString(u) }

func deploymentPhase(dep queries.Deployment, build *queries.Build) string {
	return deployments.DeploymentPhase(dep, build)
}

type deploymentOut = deployments.DeploymentOut

type deploymentListItemOut = deployments.DeploymentListItemOut

func (a *API) deploymentToOutCtx(ctx context.Context, dep queries.Deployment) deployments.DeploymentOut {
	return (&deployments.Handler{Q: a.q}).ToOutCtx(ctx, dep)
}

func (a *API) listRowForOrgToOutCtx(ctx context.Context, row queries.DeploymentFindRecentWithProjectForOrgRow) deployments.DeploymentListItemOut {
	return (&deployments.Handler{Q: a.q}).ListRowForOrgToOutCtx(ctx, row)
}

func isProjectVolumeTransitionalStatus(status string) bool {
	switch status {
	case "backing_up", "restoring", "repairing", "deleting":
		return true
	default:
		return false
	}
}
