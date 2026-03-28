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

	got := ComputeDesiredInstanceCount(proj, 125, 0, false)
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

	got := ComputeDesiredInstanceCount(proj, 0, 150, true)
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

	got := ComputeDesiredInstanceCount(proj, 500, 400, true)
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

	got := ComputeDesiredInstanceCount(proj, 10, 20, true)
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

	got := ComputeDesiredInstanceCount(proj, 0, 0, false)
	if got != 1 {
		t.Fatalf("got %d want 1", got)
	}
}
