package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/preview"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

func (d *Deployer) ensurePreviewRoutes(ctx context.Context, dep queries.Deployment, proj queries.Project, logger *slog.Logger) error {
	if dep.DeploymentKind != "preview" || !dep.PreviewEnvironmentID.Valid {
		return nil
	}
	base, err := d.q.ClusterSettingGet(ctx, config.SettingPreviewBaseDomain)
	if err != nil || strings.TrimSpace(base) == "" {
		return nil
	}
	base = strings.TrimSpace(base)

	pe, err := d.q.PreviewEnvironmentByID(ctx, dep.PreviewEnvironmentID)
	if err != nil {
		return fmt.Errorf("preview env: %w", err)
	}

	stableDom, err := d.q.DomainFindByPreviewEnvironmentAndKind(ctx, queries.DomainFindByPreviewEnvironmentAndKindParams{
		PreviewEnvironmentID: pe.ID,
		DomainKind:           "preview_stable",
	})
	if err == nil {
		if err := d.q.DomainUpdateDeploymentForDomainID(ctx, queries.DomainUpdateDeploymentForDomainIDParams{
			ID:           stableDom.ID,
			DeploymentID: dep.ID,
		}); err != nil {
			return fmt.Errorf("update stable preview domain: %w", err)
		}
	}

	sha := strings.TrimSpace(dep.GithubCommit)
	if len(sha) > 7 {
		sha = sha[:7]
	}
	immutableHost := preview.ImmutableHostname(sha, int(pe.PrNumber), proj.Name, base)

	_, err = d.q.DomainFindByDeploymentIDAndKind(ctx, queries.DomainFindByDeploymentIDAndKindParams{
		DeploymentID: dep.ID,
		DomainKind:   "preview_immutable",
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lookup immutable preview domain: %w", err)
		}
		if _, err := d.q.DomainCreatePreview(ctx, queries.DomainCreatePreviewParams{
			ID:                   pguuid.ToPgtype(uuid.New()),
			ProjectID:            dep.ProjectID,
			DeploymentID:         dep.ID,
			DomainName:           immutableHost,
			DomainKind:           "preview_immutable",
			PreviewEnvironmentID: pe.ID,
		}); err != nil {
			return fmt.Errorf("create immutable preview domain: %w", err)
		}
		logger.Info("preview immutable domain", "host", immutableHost)
	}
	return nil
}
