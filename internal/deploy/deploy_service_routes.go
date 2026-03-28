package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/preview"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

func (d *Deployer) ensureProductionRoutes(ctx context.Context, dep queries.Deployment, proj queries.Project, service *queries.Service, logger *slog.Logger) error {
	if service == nil {
		return nil
	}

	if err := d.q.DomainUpdateDeploymentForService(ctx, queries.DomainUpdateDeploymentForServiceParams{
		DeploymentID: dep.ID,
		ServiceID:    service.ID,
	}); err != nil {
		return fmt.Errorf("update service domains: %w", err)
	}

	publicEndpoint, ok, err := d.publicHTTPEndpoint(ctx, service.ID)
	if err != nil {
		return err
	}
	if !ok {
		if err := d.q.DomainDeleteManagedByServiceID(ctx, service.ID); err != nil {
			return fmt.Errorf("delete managed service domain: %w", err)
		}
		return nil
	}

	base, err := d.q.ClusterSettingGet(ctx, config.SettingServiceBaseDomain)
	if err != nil || strings.TrimSpace(base) == "" {
		return nil
	}
	host := preview.ProductionServiceHostname(service.Slug, proj.Name, base)
	if host == "" {
		return nil
	}

	_, err = d.q.DomainManagedByServiceID(ctx, service.ID)
	switch {
	case err == nil:
		if _, err := d.q.DomainUpdateManagedByServiceID(ctx, queries.DomainUpdateManagedByServiceIDParams{
			ServiceID:    service.ID,
			DomainName:   host,
			DeploymentID: dep.ID,
		}); err != nil {
			return fmt.Errorf("update managed service domain: %w", err)
		}
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := d.q.DomainCreateManaged(ctx, queries.DomainCreateManagedParams{
			ID:           pguuid.ToPgtype(uuid.New()),
			ProjectID:    dep.ProjectID,
			ServiceID:    service.ID,
			DeploymentID: dep.ID,
			DomainName:   host,
		}); err != nil {
			return fmt.Errorf("create managed service domain: %w", err)
		}
	default:
		return fmt.Errorf("lookup managed service domain: %w", err)
	}

	logger.Info("service managed domain ensured",
		"service_id", pguuid.FromPgtype(service.ID),
		"endpoint_id", pguuid.FromPgtype(publicEndpoint.ID),
		"host", host,
	)
	return nil
}

func (d *Deployer) publicHTTPEndpoint(ctx context.Context, serviceID pgtype.UUID) (queries.ServiceEndpoint, bool, error) {
	endpoints, err := d.q.ServiceEndpointListByServiceID(ctx, serviceID)
	if err != nil {
		return queries.ServiceEndpoint{}, false, fmt.Errorf("list service endpoints: %w", err)
	}
	for _, endpoint := range endpoints {
		if endpoint.Protocol == "http" && endpoint.Visibility == "public" {
			return endpoint, true, nil
		}
	}
	return queries.ServiceEndpoint{}, false, nil
}
