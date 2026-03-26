package config

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/bootstrap"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// DecryptSecretPlaintext reads and decrypts a cluster_secrets row. Missing row returns ("", nil).
func DecryptSecretPlaintext(ctx context.Context, q *queries.Queries, masterKey []byte, key string) (string, error) {
	var out string
	if err := decryptSecretInto(ctx, q, masterKey, key, &out); err != nil {
		return "", err
	}
	return out, nil
}

// GitHubTokenFromPool loads the GitHub token for one-shot CLI commands (e.g. deploy without running serve).
func GitHubTokenFromPool(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	mk, err := bootstrap.LoadOrCreateMasterKey()
	if err != nil {
		return "", err
	}
	q := queries.New(pool)
	return DecryptSecretPlaintext(ctx, q, mk, SecretGitHubToken)
}
