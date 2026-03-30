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

func TestShouldServeHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		components serveComponents
		want       bool
	}{
		{
			name:       "api only",
			components: serveComponents{api: true},
			want:       true,
		},
		{
			name:       "api and worker",
			components: serveComponents{api: true, worker: true},
			want:       true,
		},
		{
			name:       "worker only",
			components: serveComponents{worker: true},
			want:       false,
		},
		{
			name:       "edge only",
			components: serveComponents{edge: true},
			want:       false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldServeHTTP(tt.components); got != tt.want {
				t.Fatalf("shouldServeHTTP(%+v) = %v, want %v", tt.components, got, tt.want)
			}
		})
	}
}
