package queries

import (
	"strings"
	"testing"
)

func TestRouteQueriesOnlyUseActiveRunningBackends(t *testing.T) {
	t.Parallel()

	if !strings.Contains(routeFindActive, "di.role = 'active'") {
		t.Fatalf("RouteFindActive should filter active deployment instances, got:\n%s", routeFindActive)
	}
	if !strings.Contains(routeFindActive, "v.status = 'running'") {
		t.Fatalf("RouteFindActive should filter running VMs, got:\n%s", routeFindActive)
	}
	if !strings.Contains(domainEdgeLookup, "di.role = 'active'") {
		t.Fatalf("DomainEdgeLookup should count only active deployment instances, got:\n%s", domainEdgeLookup)
	}
	if !strings.Contains(domainEdgeLookup, "vm.status = 'running'") {
		t.Fatalf("DomainEdgeLookup should count only running VMs, got:\n%s", domainEdgeLookup)
	}
}

func TestUsageQueryOnlyUsesActiveRunningInstances(t *testing.T) {
	t.Parallel()

	if !strings.Contains(deploymentInstancesRunningForUsageOnServer, "di.role = 'active'") {
		t.Fatalf("usage query should filter active deployment instances, got:\n%s", deploymentInstancesRunningForUsageOnServer)
	}
	if !strings.Contains(deploymentInstancesRunningForUsageOnServer, "di.status = 'running'") {
		t.Fatalf("usage query should filter running deployment instances, got:\n%s", deploymentInstancesRunningForUsageOnServer)
	}
}

func TestRetryQueriesResetInstanceBackToActive(t *testing.T) {
	t.Parallel()

	if !strings.Contains(deploymentInstancePrepareRetry, "role = 'active'") {
		t.Fatalf("prepare retry should reset role to active, got:\n%s", deploymentInstancePrepareRetry)
	}
	if !strings.Contains(deploymentInstanceResetForDeadServer, "role = 'active'") {
		t.Fatalf("dead-server reset should reset role to active, got:\n%s", deploymentInstanceResetForDeadServer)
	}
}
