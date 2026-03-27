package deploy

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

const (
	deploymentInstanceRoleActive   = "active"
	deploymentInstanceRoleWarmPool = "warm_pool"
	deploymentInstanceRoleTemplate = "template"

	vmStatusSuspending = "suspending"
	vmStatusSuspended  = "suspended"
	vmStatusWarming    = "warming"
	vmStatusTemplate   = "template"
)

type launchMode string

const (
	launchModeCold   launchMode = "cold"
	launchModeResume launchMode = "resume"
	launchModeClone  launchMode = "clone"
)

type instanceVMMetadata struct {
	Runtime         string
	SnapshotRef     string
	CloneSourceVMID pgtype.UUID
}

func selectLaunchMode(inst queries.DeploymentInstance, vm queries.Vm, templateRef string) (launchMode, bool) {
	if inst.Role == deploymentInstanceRoleTemplate {
		return "", false
	}
	if inst.VmID.Valid && vm.Status == vmStatusSuspended {
		return launchModeResume, true
	}
	if inst.Role == deploymentInstanceRoleWarmPool {
		return "", false
	}
	if strings.TrimSpace(templateRef) != "" {
		return launchModeClone, true
	}
	return launchModeCold, true
}

func isActiveInstance(inst queries.DeploymentInstance) bool {
	return inst.Role == "" || inst.Role == deploymentInstanceRoleActive
}

func isWarmPoolInstance(inst queries.DeploymentInstance) bool {
	return inst.Role == deploymentInstanceRoleWarmPool
}
