package main

import (
	"net/http/httptest"
	"testing"
)

func TestCorsOriginAllowedLocalhostOnlyForLocalAPI(t *testing.T) {
	t.Parallel()

	localReq := httptest.NewRequest("GET", "http://localhost:8080/api/meta", nil)
	localReq.Host = "localhost:8080"
	if !corsOriginAllowed(localReq, "http://localhost:5173", nil) {
		t.Fatal("expected localhost origin to be allowed for local API host")
	}

	publicReq := httptest.NewRequest("GET", "https://api.example.com/api/meta", nil)
	publicReq.Host = "api.example.com"
	if corsOriginAllowed(publicReq, "http://localhost:5173", nil) {
		t.Fatal("expected localhost origin to be rejected for non-local API host")
	}
}
