package rpc

import "testing"

func TestValidateProjectScalingBounds(t *testing.T) {
	t.Parallel()

	if err := validateProjectScalingBounds(0, 3); err != nil {
		t.Fatalf("expected bounds to pass: %v", err)
	}
	if err := validateProjectScalingBounds(2, 2); err != nil {
		t.Fatalf("expected equal bounds to pass: %v", err)
	}
	if err := validateProjectScalingBounds(-1, 1); err == nil {
		t.Fatal("expected negative min to fail")
	}
	if err := validateProjectScalingBounds(1, -1); err == nil {
		t.Fatal("expected negative max to fail")
	}
	if err := validateProjectScalingBounds(4, 3); err == nil {
		t.Fatal("expected min > max to fail")
	}
}

func TestClampDesiredReplicaTarget(t *testing.T) {
	t.Parallel()

	if got := clampDesiredReplicaTarget(0, 0, 3); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
	if got := clampDesiredReplicaTarget(5, 0, 3); got != 3 {
		t.Fatalf("got %d want 3", got)
	}
	if got := clampDesiredReplicaTarget(0, 2, 5); got != 2 {
		t.Fatalf("got %d want 2", got)
	}
	if got := clampDesiredReplicaTarget(2, 0, 0); got != 0 {
		t.Fatalf("got %d want 0", got)
	}
}

func TestShouldResetInheritedServiceTargets(t *testing.T) {
	t.Parallel()

	if shouldResetInheritedServiceTargets(nil, 2, 2) {
		t.Fatal("expected nil request not to reset services")
	}
	if shouldResetInheritedServiceTargets(int32ptr(2), 2, 2) {
		t.Fatal("expected no-op desired update not to reset services")
	}
	if !shouldResetInheritedServiceTargets(int32ptr(4), 2, 4) {
		t.Fatal("expected desired change to reset matching inherited services")
	}
}
