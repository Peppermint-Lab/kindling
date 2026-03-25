// Package deploy implements the deployment reconciler, which drives the full
// lifecycle: pending → create build → wait for build → create VM → health check
// → mark running → stop old deployments.
package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
)

// Deployer orchestrates deployments via reconciliation.
type Deployer struct {
	q                    *queries.Queries
	serverID             uuid.UUID
	deploymentReconciler *reconciler.Scheduler
}

// New creates a new deployer.
func New(q *queries.Queries, serverID uuid.UUID) *Deployer {
	return &Deployer{q: q, serverID: serverID}
}

// SetReconciler sets the deployment reconciler for re-scheduling.
func (d *Deployer) SetReconciler(r *reconciler.Scheduler) {
	d.deploymentReconciler = r
}

// ReconcileDeployment is the reconcile function for deployments.
func (d *Deployer) ReconcileDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	dep, err := d.q.DeploymentFirstByID(ctx, uuidToPgtype(deploymentID))
	if err != nil {
		return fmt.Errorf("fetch deployment: %w", err)
	}

	logger := slog.With("deployment_id", deploymentID)

	// Terminal states — clean up VM if needed.
	if dep.DeletedAt.Valid || dep.FailedAt.Valid || dep.StoppedAt.Valid {
		logger.Info("deployment in terminal state")
		if dep.VmID.Valid {
			vm, err := d.q.VMFirstByID(ctx, dep.VmID)
			if err == nil && !vm.DeletedAt.Valid {
				d.q.VMSoftDelete(ctx, dep.VmID)
				logger.Info("soft-deleted VM for terminal deployment", "vm_id", dep.VmID)
			}
		}
		return nil
	}

	// Already running — nothing to do.
	if dep.RunningAt.Valid {
		return nil
	}

	// Step 1: Ensure deployment has a build.
	if !dep.BuildID.Valid {
		logger.Info("creating build for deployment")
		build, err := d.q.BuildCreate(ctx, queries.BuildCreateParams{
			ID:           uuidToPgtype(uuid.New()),
			ProjectID:    dep.ProjectID,
			Status:       "pending",
			GithubCommit: dep.GithubCommit,
			GithubBranch: "main",
		})
		if err != nil {
			return fmt.Errorf("create build: %w", err)
		}

		dep, err = d.q.DeploymentUpdateBuild(ctx, queries.DeploymentUpdateBuildParams{
			ID:      dep.ID,
			BuildID: build.ID,
		})
		if err != nil {
			return fmt.Errorf("update deployment build: %w", err)
		}
		logger.Info("build created", "build_id", build.ID)
		return nil // build reconciler will pick it up
	}

	// Step 2: Wait for build to complete.
	build, err := d.q.BuildFirstByID(ctx, dep.BuildID)
	if err != nil {
		return fmt.Errorf("fetch build: %w", err)
	}

	if build.FailedAt.Valid {
		logger.Info("build failed, failing deployment")
		return d.q.DeploymentUpdateFailedAt(ctx, dep.ID)
	}

	if !build.ImageID.Valid {
		logger.Info("build in progress, will retry")
		d.scheduleRetry(deploymentID, 10*time.Second)
		return nil
	}

	// Step 3: Update deployment's image.
	if dep.ImageID != build.ImageID {
		dep, err = d.q.DeploymentUpdateImage(ctx, queries.DeploymentUpdateImageParams{
			ID:      dep.ID,
			ImageID: build.ImageID,
		})
		if err != nil {
			return fmt.Errorf("update deployment image: %w", err)
		}
	}

	// Step 4: Create VM if needed.
	if !dep.VmID.Valid {
		logger.Info("creating VM for deployment")

		// Get the server to place the VM on.
		server, err := d.q.ServerFindLeastLoaded(ctx)
		if err != nil {
			return fmt.Errorf("find server: %w", err)
		}

		// Allocate IP.
		ip, err := d.q.VMNextIPAddress(ctx, queries.VMNextIPAddressParams{
			ServerID: server.ID,
			IpRange:  server.IpRange,
		})
		if err != nil {
			return fmt.Errorf("allocate IP: %w", err)
		}

		vm, err := d.q.VMCreate(ctx, queries.VMCreateParams{
			ID:        uuidToPgtype(uuid.New()),
			ServerID:  server.ID,
			ImageID:   dep.ImageID,
			Status:    "pending",
			Vcpus:     1,
			Memory:    512,
			IpAddress: ip,
			Port:      pgtype.Int4{Int32: 3000, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("create VM: %w", err)
		}

		dep, err = d.q.DeploymentUpdateVM(ctx, queries.DeploymentUpdateVMParams{
			ID:   dep.ID,
			VmID: vm.ID,
		})
		if err != nil {
			return fmt.Errorf("update deployment VM: %w", err)
		}

		logger.Info("VM created", "vm_id", vm.ID)
		d.scheduleRetry(deploymentID, 5*time.Second)
		return nil // VM reconciler will boot it
	}

	// Step 5: Check VM status + health check.
	vm, err := d.q.VMFirstByID(ctx, dep.VmID)
	if err != nil {
		return fmt.Errorf("fetch VM: %w", err)
	}

	if vm.DeletedAt.Valid {
		// VM was deleted, reset and retry.
		dep, err = d.q.DeploymentUpdateVM(ctx, queries.DeploymentUpdateVMParams{
			ID:   dep.ID,
			VmID: pgtype.UUID{},
		})
		if err != nil {
			return fmt.Errorf("reset deployment VM: %w", err)
		}
		d.scheduleRetry(deploymentID, 0)
		return nil
	}

	if vm.Status != "running" {
		logger.Info("VM not running yet", "status", vm.Status)
		d.scheduleRetry(deploymentID, 5*time.Second)
		return nil
	}

	// Health check.
	if !d.healthCheck(vm.IpAddress.String(), vm.Port.Int32) {
		logger.Info("health check failed, retrying")
		return fmt.Errorf("health check failed")
	}

	// Step 6: Mark running.
	if err := d.q.DeploymentMarkRunning(ctx, dep.ID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}
	logger.Info("deployment is running")

	// Step 7: Update domain routing.
	if err := d.q.DomainUpdateDeploymentForProject(ctx, queries.DomainUpdateDeploymentForProjectParams{
		DeploymentID: dep.ID,
		ProjectID:    dep.ProjectID,
	}); err != nil {
		logger.Warn("failed to update domain routing", "error", err)
	}

	// Step 8: Stop old deployments.
	d.stopOldDeployments(ctx, dep)

	return nil
}

func (d *Deployer) healthCheck(ip string, port int32) bool {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/", ip, port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func (d *Deployer) stopOldDeployments(ctx context.Context, current queries.Deployment) {
	old, err := d.q.DeploymentFindRunningAndOlder(ctx, queries.DeploymentFindRunningAndOlderParams{
		ProjectID: current.ProjectID,
		ID:        current.ID,
	})
	if err != nil || len(old) == 0 {
		return
	}

	slog.Info("stopping old deployments", "count", len(old))
	for _, dep := range old {
		if dep.VmID.Valid {
			d.q.VMSoftDelete(ctx, dep.VmID)
		}
		d.q.DeploymentMarkStopped(ctx, dep.ID)
	}
}

func (d *Deployer) scheduleRetry(id uuid.UUID, delay time.Duration) {
	if d.deploymentReconciler != nil {
		d.deploymentReconciler.Schedule(id, time.Now().Add(delay))
	}
}

func uuidToPgtype(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}
