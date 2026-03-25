// Package deploy implements the deployment reconciler, which drives the full
// lifecycle: pending → create build → wait for build → create VM → health check
// → mark running → stop old deployments.
package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/runtime"
)

// Deployer orchestrates deployments via reconciliation.
type Deployer struct {
	q                    *queries.Queries
	serverID             uuid.UUID
	deploymentReconciler *reconciler.Scheduler
	rt                   runtime.Runtime
}

// New creates a new deployer.
func New(q *queries.Queries, serverID uuid.UUID) *Deployer {
	return &Deployer{q: q, serverID: serverID}
}

// SetRuntime sets the runtime for starting instances.
func (d *Deployer) SetRuntime(rt runtime.Runtime) {
	d.rt = rt
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

	// Step 3: Update deployment's image ref.
	if dep.ImageID != build.ImageID {
		dep, err = d.q.DeploymentUpdateImage(ctx, queries.DeploymentUpdateImageParams{
			ID:      dep.ID,
			ImageID: build.ImageID,
		})
		if err != nil {
			return fmt.Errorf("update deployment image: %w", err)
		}
	}

	// Step 4: Start the instance via runtime.
	if d.rt == nil {
		return fmt.Errorf("no runtime configured")
	}

	// Get image ref for the runtime.
	image, err := d.q.ImageFindByID(ctx, dep.ImageID)
	if err != nil {
		return fmt.Errorf("fetch image: %w", err)
	}
	imageRef := fmt.Sprintf("%s/%s:%s", image.Registry, image.Repository, image.Tag)

	// Fetch env vars for this project.
	envVars, err := d.q.EnvironmentVariableFindByProjectID(ctx, dep.ProjectID)
	if err != nil {
		return fmt.Errorf("fetch env vars: %w", err)
	}
	var env []string
	for _, ev := range envVars {
		env = append(env, fmt.Sprintf("%s=%s", ev.Name, ev.Value))
	}

	logger.Info("starting instance", "image", imageRef, "runtime", d.rt.Name())

	ip, err := d.rt.Start(ctx, runtime.Instance{
		ID:       deploymentID,
		ImageRef: imageRef,
		VCPUs:    1,
		MemoryMB: 512,
		Port:     3000,
		Env:      env,
	})
	if err != nil {
		logger.Error("failed to start instance", "error", err)
		return fmt.Errorf("start instance: %w", err)
	}

	logger.Info("instance started", "ip", ip)

	// Step 5: Health check.
	if requiresExternalHealthCheck(d.rt.Name()) && !d.healthCheck(ip, 3000) {
		logger.Info("health check failed, retrying")
		return fmt.Errorf("health check failed")
	}

	// Step 6: Persist runtime metadata after startup succeeds and any
	// runtime-specific readiness gate has passed.
	dep, err = d.persistRuntimeVMMetadata(ctx, d.q, dep, ip, 1, 512, env)
	if err != nil {
		return fmt.Errorf("persist runtime vm metadata: %w", err)
	}

	// Step 7: Mark running.
	if err := d.q.DeploymentMarkRunning(ctx, dep.ID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}
	logger.Info("deployment is running", "ip", ip)

	// Step 8: Update domain routing.
	d.q.DomainUpdateDeploymentForProject(ctx, queries.DomainUpdateDeploymentForProjectParams{
		DeploymentID: dep.ID,
		ProjectID:    dep.ProjectID,
	})

	// Step 9: Stop old deployments.
	d.stopOldDeployments(ctx, dep)

	return nil
}

func (d *Deployer) healthCheck(addr string, port int) bool {
	client := &http.Client{Timeout: 10 * time.Second}
	// addr might already include port (e.g. "0.0.0.0:32768").
	url := addr
	if !strings.Contains(addr, ":") {
		url = fmt.Sprintf("%s:%d", addr, port)
	}
	if !strings.HasPrefix(url, "http") {
		url = "http://" + url
	}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func requiresExternalHealthCheck(runtimeName string) bool {
	return runtimeName != "apple-vz"
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
