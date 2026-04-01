package autoscale

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

type fakeAutoscaleStore struct {
	projects  []queries.Project
	services  map[uuid.UUID][]queries.Service
	deploys   map[uuid.UUID]queries.Deployment
	httpRows  map[uuid.UUID][]queries.ProjectHTTPUsageRollupsAggregatedByDeploymentRow
	cpuRows   map[uuid.UUID][]queries.InstanceUsageLatestPerInstanceByDeploymentRow
	updatedID uuid.UUID
	updatedTo int32
}

func (f *fakeAutoscaleStore) ProjectFindAll(context.Context) ([]queries.Project, error) {
	return f.projects, nil
}

func (f *fakeAutoscaleStore) ServiceListByProjectID(_ context.Context, projectID pgtype.UUID) ([]queries.Service, error) {
	return f.services[uuid.UUID(projectID.Bytes)], nil
}

func (f *fakeAutoscaleStore) DeploymentLatestRunningByServiceID(_ context.Context, serviceID pgtype.UUID) (queries.Deployment, error) {
	return f.deploys[uuid.UUID(serviceID.Bytes)], nil
}

func (f *fakeAutoscaleStore) ProjectHTTPUsageRollupsAggregatedByDeployment(_ context.Context, arg queries.ProjectHTTPUsageRollupsAggregatedByDeploymentParams) ([]queries.ProjectHTTPUsageRollupsAggregatedByDeploymentRow, error) {
	return f.httpRows[uuid.UUID(arg.DeploymentID.Bytes)], nil
}

func (f *fakeAutoscaleStore) InstanceUsageLatestPerInstanceByDeployment(_ context.Context, arg queries.InstanceUsageLatestPerInstanceByDeploymentParams) ([]queries.InstanceUsageLatestPerInstanceByDeploymentRow, error) {
	return f.cpuRows[uuid.UUID(arg.ID.Bytes)], nil
}

func (f *fakeAutoscaleStore) ServiceSetDesiredInstanceCount(_ context.Context, arg queries.ServiceSetDesiredInstanceCountParams) error {
	f.updatedID = uuid.UUID(arg.ID.Bytes)
	f.updatedTo = arg.DesiredInstanceCount
	return nil
}

func TestRunAutoscaleSweepScalesBusyService(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	serviceID := uuid.New()
	deploymentID := uuid.New()
	store := &fakeAutoscaleStore{
		projects: []queries.Project{
			{
				ID:                   pgtype.UUID{Bytes: projectID, Valid: true},
				DesiredInstanceCount: 1,
				MinInstanceCount:     1,
				MaxInstanceCount:     5,
			},
		},
		services: map[uuid.UUID][]queries.Service{
			projectID: {
				{
					ID:                   pgtype.UUID{Bytes: serviceID, Valid: true},
					ProjectID:            pgtype.UUID{Bytes: projectID, Valid: true},
					DesiredInstanceCount: 1,
				},
			},
		},
		deploys: map[uuid.UUID]queries.Deployment{
			serviceID: {
				ID:        pgtype.UUID{Bytes: deploymentID, Valid: true},
				ProjectID: pgtype.UUID{Bytes: projectID, Valid: true},
				ServiceID: pgtype.UUID{Bytes: serviceID, Valid: true},
			},
		},
		httpRows: map[uuid.UUID][]queries.ProjectHTTPUsageRollupsAggregatedByDeploymentRow{
			deploymentID: {
				{RequestCount: 900},
			},
		},
		cpuRows: map[uuid.UUID][]queries.InstanceUsageLatestPerInstanceByDeploymentRow{},
	}

	runAutoscaleSweep(context.Background(), store, nil, time.Date(2026, time.March, 31, 23, 30, 0, 0, time.UTC))

	if store.updatedID != serviceID {
		t.Fatalf("updated service %s want %s", store.updatedID, serviceID)
	}
	if store.updatedTo != 5 {
		t.Fatalf("updated target %d want 5", store.updatedTo)
	}
}

func TestRunAutoscaleSweepHoldsScaleDownAfterRecentTraffic(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 31, 23, 30, 0, 0, time.UTC)
	projectID := uuid.New()
	serviceID := uuid.New()
	deploymentID := uuid.New()
	store := &fakeAutoscaleStore{
		projects: []queries.Project{
			{
				ID:                   pgtype.UUID{Bytes: projectID, Valid: true},
				DesiredInstanceCount: 3,
				MinInstanceCount:     1,
				MaxInstanceCount:     5,
				LastRequestAt:        pgtype.Timestamptz{Time: now.Add(-2 * time.Minute), Valid: true},
			},
		},
		services: map[uuid.UUID][]queries.Service{
			projectID: {
				{
					ID:                   pgtype.UUID{Bytes: serviceID, Valid: true},
					ProjectID:            pgtype.UUID{Bytes: projectID, Valid: true},
					DesiredInstanceCount: 3,
				},
			},
		},
		deploys: map[uuid.UUID]queries.Deployment{
			serviceID: {
				ID:        pgtype.UUID{Bytes: deploymentID, Valid: true},
				ProjectID: pgtype.UUID{Bytes: projectID, Valid: true},
				ServiceID: pgtype.UUID{Bytes: serviceID, Valid: true},
			},
		},
		httpRows: map[uuid.UUID][]queries.ProjectHTTPUsageRollupsAggregatedByDeploymentRow{},
		cpuRows:  map[uuid.UUID][]queries.InstanceUsageLatestPerInstanceByDeploymentRow{},
	}

	runAutoscaleSweep(context.Background(), store, nil, now)

	if store.updatedID != uuid.Nil {
		t.Fatalf("expected no scale-down update, got service %s target %d", store.updatedID, store.updatedTo)
	}
}

func TestComputeDesiredInstanceCountScalesUpFromHTTP(t *testing.T) {
	t.Parallel()

	proj := queries.Project{
		DesiredInstanceCount: 1,
		MinInstanceCount:     1,
		MaxInstanceCount:     5,
	}

	got := ComputeDesiredInstanceCount(proj, nil, 125, 0, false)
	if got != 3 {
		t.Fatalf("got %d want 3", got)
	}
}

func TestComputeDesiredInstanceCountScalesUpFromCPU(t *testing.T) {
	t.Parallel()

	proj := queries.Project{
		DesiredInstanceCount: 1,
		MinInstanceCount:     1,
		MaxInstanceCount:     5,
	}

	got := ComputeDesiredInstanceCount(proj, nil, 0, 150, true)
	if got != 3 {
		t.Fatalf("got %d want 3", got)
	}
}

func TestComputeDesiredInstanceCountHonorsMaxBound(t *testing.T) {
	t.Parallel()

	proj := queries.Project{
		DesiredInstanceCount: 2,
		MinInstanceCount:     1,
		MaxInstanceCount:     3,
	}

	got := ComputeDesiredInstanceCount(proj, nil, 500, 400, true)
	if got != 3 {
		t.Fatalf("got %d want 3", got)
	}
}

func TestComputeDesiredInstanceCountStepsDownOneReplica(t *testing.T) {
	t.Parallel()

	proj := queries.Project{
		DesiredInstanceCount: 4,
		MinInstanceCount:     1,
		MaxInstanceCount:     5,
	}

	got := ComputeDesiredInstanceCount(proj, nil, 10, 20, true)
	if got != 3 {
		t.Fatalf("got %d want 3", got)
	}
}

func TestComputeDesiredInstanceCountRespectsNonZeroFloor(t *testing.T) {
	t.Parallel()

	proj := queries.Project{
		DesiredInstanceCount: 1,
		MinInstanceCount:     0,
		MaxInstanceCount:     3,
	}

	got := ComputeDesiredInstanceCount(proj, nil, 0, 0, false)
	if got != 1 {
		t.Fatalf("got %d want 1", got)
	}
}

func TestComputeDesiredInstanceCountUsesExplicitServiceReplicaTarget(t *testing.T) {
	t.Parallel()

	proj := queries.Project{
		DesiredInstanceCount: 1,
		MinInstanceCount:     1,
		MaxInstanceCount:     5,
	}
	svc := &queries.Service{DesiredInstanceCount: 2}
	// RPM-driven target above the service's current explicit count scales the service row upward.
	got := ComputeDesiredInstanceCount(proj, svc, 125, 0, false)
	if got != 3 {
		t.Fatalf("got %d want 3", got)
	}
}

func TestShouldSkipAutoscaleForInheritedServiceWithoutSignals(t *testing.T) {
	t.Parallel()

	if !shouldSkipAutoscale(&queries.Service{}, 0, false) {
		t.Fatal("expected inherited service without signals to be skipped")
	}
	if shouldSkipAutoscale(&queries.Service{}, 1, false) {
		t.Fatal("expected HTTP traffic to prevent skip")
	}
	if shouldSkipAutoscale(&queries.Service{}, 0, true) {
		t.Fatal("expected CPU samples to prevent skip")
	}
	if shouldSkipAutoscale(&queries.Service{DesiredInstanceCount: 2}, 0, false) {
		t.Fatal("expected explicit service target to remain autoscale eligible")
	}
}

func TestShouldHoldScaleDownAfterRecentTraffic(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 31, 23, 30, 0, 0, time.UTC)
	if !shouldHoldScaleDown(queries.Project{
		LastRequestAt: pgtype.Timestamptz{Time: now.Add(-2 * time.Minute), Valid: true},
	}, now) {
		t.Fatal("expected recent traffic to hold scale-down")
	}
	if shouldHoldScaleDown(queries.Project{
		LastRequestAt: pgtype.Timestamptz{Time: now.Add(-6 * time.Minute), Valid: true},
	}, now) {
		t.Fatal("expected old traffic to allow scale-down")
	}
}
