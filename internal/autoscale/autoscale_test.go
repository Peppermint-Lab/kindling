package autoscale

import (
	"testing"

	"github.com/kindlingvm/kindling/internal/database/queries"
)

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
