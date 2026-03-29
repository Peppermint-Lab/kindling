package githubactions

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestHTTPClientGenerateJITConfig(t *testing.T) {
	t.Parallel()

	var sawAccessToken bool
	var sawRegistrationToken bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/321/access_tokens":
			sawAccessToken = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"installation-token"}`))
		case "/orgs/kindlingvm/actions/runners/registration-token":
			sawRegistrationToken = true
			if got := r.Header.Get("Authorization"); got != "Bearer installation-token" {
				t.Fatalf("authorization header = %q", got)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"registration-token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.Client(), srv.URL)
	jit, err := client.GenerateJITConfig(context.Background(), JITConfigRequest{
		Integration: Integration{
			Connection: queries.OrgProviderConnection{
				ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
				OrganizationID: pgtype.UUID{Bytes: uuid.New(), Valid: true},
			},
			Metadata: ProviderMetadata{
				Mode:           ProviderModeActionsRunner,
				OrgLogin:       "kindlingvm",
				AppID:          123,
				InstallationID: 321,
				RunnerGroupID:  456,
			},
			Credentials: ProviderCredentials{
				AppPrivateKeyPEM: testRSAPrivateKeyPEM(t),
				WebhookSecret:    "secret",
			},
		},
		RunnerName: "kindling-1-2",
		Labels:     []string{"self-hosted", "kindling", "linux", "x64"},
	})
	if err != nil {
		t.Fatalf("GenerateJITConfig returned error: %v", err)
	}
	if !sawAccessToken || !sawRegistrationToken {
		t.Fatalf("expected both API calls, got token=%v registration=%v", sawAccessToken, sawRegistrationToken)
	}
	if jit.EncodedJITConfig != "registration-token" {
		t.Fatalf("EncodedJITConfig = %q", jit.EncodedJITConfig)
	}
	if jit.RunnerID != 0 {
		t.Fatalf("RunnerID = %d", jit.RunnerID)
	}
	if jit.RunnerURL != "https://github.com/kindlingvm" {
		t.Fatalf("RunnerURL = %q", jit.RunnerURL)
	}
}

func testRSAPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	return string(pem.EncodeToMemory(block))
}
