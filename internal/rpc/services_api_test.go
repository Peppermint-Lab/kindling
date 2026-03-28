package rpc

import "testing"

func TestNormalizeServiceEndpointInput(t *testing.T) {
	t.Parallel()

	got, err := normalizeServiceEndpointInput(serviceEndpointInput{
		Name:       " Web API ",
		Protocol:   "HTTP",
		TargetPort: int32ptr(8080),
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("expected valid input: %v", err)
	}
	if got.Name != "web-api" {
		t.Fatalf("got name %q want %q", got.Name, "web-api")
	}
	if got.Protocol != "http" {
		t.Fatalf("got protocol %q want http", got.Protocol)
	}
	if got.TargetPort != 8080 {
		t.Fatalf("got port %d want 8080", got.TargetPort)
	}
	if got.Visibility != "public" {
		t.Fatalf("got visibility %q want public", got.Visibility)
	}
}

func TestNormalizeServiceEndpointInputRejectsPublicTCP(t *testing.T) {
	t.Parallel()

	_, err := normalizeServiceEndpointInput(serviceEndpointInput{
		Name:       "postgres",
		Protocol:   "tcp",
		TargetPort: int32ptr(5432),
		Visibility: "public",
	})
	if err == nil {
		t.Fatal("expected public tcp endpoint to fail validation")
	}
}

func TestNormalizeServiceEndpointInputRejectsBadPort(t *testing.T) {
	t.Parallel()

	_, err := normalizeServiceEndpointInput(serviceEndpointInput{
		Name:       "api",
		Protocol:   "http",
		TargetPort: int32ptr(70000),
		Visibility: "private",
	})
	if err == nil {
		t.Fatal("expected invalid port to fail validation")
	}
}

func int32ptr(v int32) *int32 { return &v }
