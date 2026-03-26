package deploy

import "testing"

func TestRequiresExternalHealthCheck(t *testing.T) {
	if requiresExternalHealthCheck("apple-vz") {
		t.Fatal("expected apple-vz to rely on runtime readiness instead of external health checks")
	}

	if !requiresExternalHealthCheck("crun") {
		t.Fatal("expected crun runtime to keep external health checks")
	}
}
