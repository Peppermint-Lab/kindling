package rpc

import (
	"testing"

	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestSanitizeReturnTo(t *testing.T) {
	t.Parallel()

	if got := sanitizeReturnTo("/settings?tab=authentication", "/"); got != "/settings?tab=authentication" {
		t.Fatalf("sanitizeReturnTo(valid) = %q", got)
	}
	if got := sanitizeReturnTo("https://example.com", "/"); got != "/" {
		t.Fatalf("sanitizeReturnTo(absolute) = %q, want /", got)
	}
	if got := sanitizeReturnTo("//evil.example.com", "/"); got != "/" {
		t.Fatalf("sanitizeReturnTo(double slash) = %q, want /", got)
	}
}

func TestExternalAuthFailurePath(t *testing.T) {
	t.Parallel()

	if got := externalAuthFailurePath(externalAuthState{Mode: "link"}); got != "/settings?tab=authentication" {
		t.Fatalf("externalAuthFailurePath(link) = %q", got)
	}
	if got := externalAuthFailurePath(externalAuthState{Mode: "login"}); got != "/login" {
		t.Fatalf("externalAuthFailurePath(login) = %q", got)
	}
}

func TestAuthProviderReady(t *testing.T) {
	t.Parallel()

	if authProviderReady(queries.AuthProvider{Provider: "github", Enabled: true, ClientID: "id"}) {
		t.Fatal("expected github provider without secret to be unready")
	}
	if !authProviderReady(queries.AuthProvider{Provider: "github", Enabled: true, ClientID: "id", ClientSecretCiphertext: []byte("secret")}) {
		t.Fatal("expected github provider with client id and secret to be ready")
	}
	if authProviderReady(queries.AuthProvider{Provider: "oidc", Enabled: true, ClientID: "id", ClientSecretCiphertext: []byte("secret")}) {
		t.Fatal("expected oidc provider without issuer to be unready")
	}
	if !authProviderReady(queries.AuthProvider{Provider: "oidc", Enabled: true, ClientID: "id", ClientSecretCiphertext: []byte("secret"), IssuerUrl: "https://issuer.example.com"}) {
		t.Fatal("expected oidc provider with issuer to be ready")
	}
}
