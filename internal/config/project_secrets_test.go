package config

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

func TestBackfillProjectSecretsEncryptsLegacyRows(t *testing.T) {
	key := bytes.Repeat([]byte{'k'}, 32)
	alreadyEncrypted, err := EncryptProjectSecretValue(key, "cipher")
	if err != nil {
		t.Fatal(err)
	}

	store := &fakeProjectSecretBackfillStore{
		rows: []queries.EnvironmentVariable{
			{
				ID:        pguuid.ToPgtype(uuid.New()),
				ProjectID: pguuid.ToPgtype(uuid.New()),
				Name:      "LEGACY",
				Value:     "plain-value",
				CreatedAt: ts(time.Now().UTC()),
				UpdatedAt: ts(time.Now().UTC()),
			},
			{
				ID:        pguuid.ToPgtype(uuid.New()),
				ProjectID: pguuid.ToPgtype(uuid.New()),
				Name:      "EMPTY_VALUE",
				Value:     "",
				CreatedAt: ts(time.Now().UTC()),
				UpdatedAt: ts(time.Now().UTC()),
			},
			{
				ID:        pguuid.ToPgtype(uuid.New()),
				ProjectID: pguuid.ToPgtype(uuid.New()),
				Name:      "ALREADY",
				Value:     alreadyEncrypted,
				CreatedAt: ts(time.Now().UTC()),
				UpdatedAt: ts(time.Now().UTC()),
			},
		},
	}

	updated, err := BackfillProjectSecrets(context.Background(), store, key)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2", updated)
	}
	if len(store.updated) != 2 {
		t.Fatalf("update calls = %d, want 2", len(store.updated))
	}

	gotLegacy, err := DecryptProjectSecretValue(key, store.updated[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	if gotLegacy != "plain-value" {
		t.Fatalf("legacy backfill decrypted to %q", gotLegacy)
	}

	gotEmpty, err := DecryptProjectSecretValue(key, store.updated[1].Value)
	if err != nil {
		t.Fatal(err)
	}
	if gotEmpty != "" {
		t.Fatalf("empty backfill decrypted to %q", gotEmpty)
	}
}

func TestBackfillProjectSecretsRejectsMalformedEncryptedRows(t *testing.T) {
	key := bytes.Repeat([]byte{'k'}, 32)
	store := &fakeProjectSecretBackfillStore{
		rows: []queries.EnvironmentVariable{
			{
				ID:        pguuid.ToPgtype(uuid.New()),
				ProjectID: pguuid.ToPgtype(uuid.New()),
				Name:      "BROKEN",
				Value:     projectSecretEnvelopePrefix + "not-base64***",
			},
		},
	}

	if _, err := BackfillProjectSecrets(context.Background(), store, key); err == nil {
		t.Fatal("expected malformed encrypted row to fail backfill")
	}
}

type fakeProjectSecretBackfillStore struct {
	rows    []queries.EnvironmentVariable
	updated []queries.EnvironmentVariableUpdateValueParams
}

func (f *fakeProjectSecretBackfillStore) EnvironmentVariableFindAll(_ context.Context) ([]queries.EnvironmentVariable, error) {
	return f.rows, nil
}

func (f *fakeProjectSecretBackfillStore) EnvironmentVariableUpdateValue(_ context.Context, arg queries.EnvironmentVariableUpdateValueParams) (queries.EnvironmentVariable, error) {
	f.updated = append(f.updated, arg)
	for i := range f.rows {
		if f.rows[i].ID == arg.ID {
			f.rows[i].Value = arg.Value
			return f.rows[i], nil
		}
	}
	return queries.EnvironmentVariable{}, nil
}

func ts(v time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: v, Valid: true}
}


