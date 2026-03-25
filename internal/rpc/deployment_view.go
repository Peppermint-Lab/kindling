package rpc

import (
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// deploymentPhase derives a coarse UI phase from deployment + optional build row.
func deploymentPhase(dep queries.Deployment, build *queries.Build) string {
	if build != nil && build.Status == "failed" {
		return "failed"
	}
	if dep.FailedAt.Valid {
		return "failed"
	}
	if dep.StoppedAt.Valid {
		return "stopped"
	}
	if dep.RunningAt.Valid {
		return "running"
	}
	if build != nil {
		switch build.Status {
		case "building":
			return "building"
		case "successful":
			// Image ready but VM not running yet — instance starting.
			return "starting"
		case "pending":
			return "queued"
		}
	}
	return "pending"
}
