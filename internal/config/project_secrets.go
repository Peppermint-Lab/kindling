package config

import (
	"context"
	"fmt"

	"github.com/kindlingvm/kindling/internal/database/queries"
)

type projectSecretBackfillStore interface {
	EnvironmentVariableFindAll(context.Context) ([]queries.EnvironmentVariable, error)
	EnvironmentVariableUpdateValue(context.Context, queries.EnvironmentVariableUpdateValueParams) (queries.EnvironmentVariable, error)
}

// BackfillProjectSecrets rewrites legacy plaintext project env rows into encrypted envelopes.
// Already-encrypted rows are left as-is after a decryptability check.
func BackfillProjectSecrets(ctx context.Context, store projectSecretBackfillStore, masterKey []byte) (int, error) {
	rows, err := store.EnvironmentVariableFindAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("list project secrets: %w", err)
	}

	updated := 0
	for _, row := range rows {
		if IsEncryptedProjectSecretValue(row.Value) {
			if _, err := DecryptProjectSecretValue(masterKey, row.Value); err != nil {
				return updated, fmt.Errorf("validate project secret %s: %w", row.Name, err)
			}
			continue
		}

		enc, err := EncryptProjectSecretValue(masterKey, row.Value)
		if err != nil {
			return updated, fmt.Errorf("encrypt project secret %s: %w", row.Name, err)
		}
		if _, err := store.EnvironmentVariableUpdateValue(ctx, queries.EnvironmentVariableUpdateValueParams{
			ID:    row.ID,
			Value: enc,
		}); err != nil {
			return updated, fmt.Errorf("update project secret %s: %w", row.Name, err)
		}
		updated++
	}

	return updated, nil
}
