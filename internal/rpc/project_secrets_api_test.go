package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestValidProjectSecretName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "uppercase", in: "API_KEY", want: true},
		{name: "lowercase", in: "database_url", want: true},
		{name: "leading underscore", in: "_TOKEN", want: true},
		{name: "starts with digit", in: "1TOKEN", want: false},
		{name: "contains dash", in: "API-KEY", want: false},
		{name: "contains space", in: "API KEY", want: false},
		{name: "empty", in: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := validProjectSecretName(tt.in); got != tt.want {
				t.Fatalf("validProjectSecretName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestProjectSecretOutIsWriteOnly(t *testing.T) {
	t.Parallel()

	row := queries.EnvironmentVariable{
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID: pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Name:      "API_KEY",
		Value:     "enc:v1:super-secret",
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}

	body, err := json.Marshal(projectSecretFromEnv(row))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["value"]; ok {
		t.Fatalf("expected secret value to be omitted, got %#v", got["value"])
	}
	if got["name"] != "API_KEY" {
		t.Fatalf("expected name to round-trip, got %#v", got["name"])
	}
}
