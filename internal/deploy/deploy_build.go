package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

// ensureBuild creates or checks the deployment's build. Returns (dep, done, err)
// where done=true means the caller should return immediately.
func (d *Deployer) ensureBuild(ctx context.Context, dep queries.Deployment, deploymentID uuid.UUID, logger *slog.Logger) (queries.Deployment, bool, error) {
	if !dep.BuildID.Valid {
		logger.Info("creating build for deployment")
		branch := strings.TrimSpace(dep.GithubBranch)
		if branch == "" {
			branch = "main"
		}
		build, err := d.q.BuildCreate(ctx, queries.BuildCreateParams{
			ID:           pguuid.ToPgtype(uuid.New()),
			ProjectID:    dep.ProjectID,
			ServiceID:    dep.ServiceID,
			Status:       "pending",
			GithubCommit: dep.GithubCommit,
			GithubBranch: branch,
		})
		if err != nil {
			return dep, false, fmt.Errorf("create build: %w", err)
		}
		dep, err = d.q.DeploymentUpdateBuild(ctx, queries.DeploymentUpdateBuildParams{
			ID:      dep.ID,
			BuildID: build.ID,
		})
		if err != nil {
			return dep, false, fmt.Errorf("update deployment build: %w", err)
		}
		logger.Info("build created", "build_id", build.ID)
		return dep, true, nil
	}

	build, err := d.q.BuildFirstByID(ctx, dep.BuildID)
	if err != nil {
		return dep, false, fmt.Errorf("fetch build: %w", err)
	}
	if build.FailedAt.Valid {
		logger.Info("build failed, failing deployment")
		return dep, true, d.q.DeploymentUpdateFailedAt(ctx, dep.ID)
	}
	if !build.ImageID.Valid {
		logger.Info("build in progress, will retry")
		d.scheduleRetry(deploymentID, buildPollRetryInterval)
		return dep, true, nil
	}
	return dep, false, nil
}

// syncDeploymentImage updates the deployment's image ID to match the build if needed.
func (d *Deployer) syncDeploymentImage(ctx context.Context, dep queries.Deployment) (queries.Deployment, error) {
	build, err := d.q.BuildFirstByID(ctx, dep.BuildID)
	if err != nil {
		return dep, fmt.Errorf("fetch build: %w", err)
	}
	if dep.ImageID != build.ImageID {
		dep, err = d.q.DeploymentUpdateImage(ctx, queries.DeploymentUpdateImageParams{
			ID:      dep.ID,
			ImageID: build.ImageID,
		})
		if err != nil {
			return dep, fmt.Errorf("update deployment image: %w", err)
		}
	}
	return dep, nil
}

// prepareImageAndEnv resolves the OCI image reference and decrypted env vars
// for the deployment and stores them in rc.
func (d *Deployer) prepareImageAndEnv(ctx context.Context, rc *reconcileContext) error {
	image, err := d.q.ImageFindByID(ctx, rc.dep.ImageID)
	if err != nil {
		return fmt.Errorf("fetch image: %w", err)
	}
	rc.imageRef = fmt.Sprintf("%s/%s:%s", image.Registry, image.Repository, image.Tag)
	var envVars []queries.EnvironmentVariable
	if rc.service != nil {
		envVars, err = d.q.EnvironmentVariableFindEffectiveByServiceID(ctx, rc.service.ID)
		if err != nil {
			return fmt.Errorf("fetch service env vars: %w", err)
		}
	} else {
		envVars, err = d.q.EnvironmentVariableFindByProjectID(ctx, rc.dep.ProjectID)
		if err != nil {
			return fmt.Errorf("fetch env vars: %w", err)
		}
	}
	rc.env, err = buildRuntimeEnv(envVars, d.secretDecoder)
	if err != nil {
		return fmt.Errorf("build runtime env: %w", err)
	}
	return nil
}

// resolveProjectVolume ensures the volume server is ready. Returns (volume, done, err).
func (d *Deployer) resolveProjectVolume(
	ctx context.Context,
	dep queries.Deployment,
	projectVolume *queries.ProjectVolume,
	persistentVolume *runtime.PersistentVolumeMount,
	deploymentID uuid.UUID,
	logger *slog.Logger,
) (*runtime.PersistentVolumeMount, bool, error) {
	if projectVolume == nil {
		return persistentVolume, false, nil
	}
	resolvedVolume, err := d.ensureProjectVolumeServer(ctx, *projectVolume)
	if err != nil {
		logger.Warn("project volume unavailable", "project_id", pguuid.FromPgtype(dep.ProjectID), "error", err)
		d.scheduleRetry(deploymentID, projectVolumeRetryDelay(err))
		return nil, true, nil
	}
	persistentVolume = persistentVolumeMountFromRow(resolvedVolume)
	if dep.DeploymentKind == "production" && !dep.RunningAt.Valid {
		drained, err := d.stopOldDeploymentsForVolume(ctx, dep, logger)
		if err != nil {
			return nil, false, fmt.Errorf("stop old deployments for volume: %w", err)
		}
		if drained {
			d.scheduleRetry(deploymentID, reconcileRetryInterval)
			return nil, true, nil
		}
	}
	return persistentVolume, false, nil
}
