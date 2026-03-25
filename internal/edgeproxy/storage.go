package edgeproxy

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// Ensure PostgreSQLStorage implements certmagic.Storage.
var _ certmagic.Storage = (*PostgreSQLStorage)(nil)

// PostgreSQLStorage stores TLS certificates in PostgreSQL.
type PostgreSQLStorage struct {
	q *queries.Queries
}

// NewPostgreSQLStorage creates a new PG-backed cert storage.
func NewPostgreSQLStorage(pool *pgxpool.Pool) *PostgreSQLStorage {
	return &PostgreSQLStorage{q: queries.New(pool)}
}

func (s *PostgreSQLStorage) Lock(ctx context.Context, key string) error {
	// CertMagic calls Lock before writing. We use PG's UPSERT semantics
	// so we don't need explicit locking for correctness.
	return nil
}

func (s *PostgreSQLStorage) Unlock(ctx context.Context, key string) error {
	return nil
}

func (s *PostgreSQLStorage) Store(ctx context.Context, key string, value []byte) error {
	return s.q.CertMagicStore(ctx, queries.CertMagicStoreParams{
		Key:   key,
		Value: value,
	})
}

func (s *PostgreSQLStorage) Load(ctx context.Context, key string) ([]byte, error) {
	row, err := s.q.CertMagicLoad(ctx, key)
	if err != nil {
		return nil, fs.ErrNotExist
	}
	return row.Value, nil
}

func (s *PostgreSQLStorage) Delete(ctx context.Context, key string) error {
	return s.q.CertMagicDelete(ctx, key)
}

func (s *PostgreSQLStorage) Exists(ctx context.Context, key string) bool {
	exists, err := s.q.CertMagicExists(ctx, key)
	if err != nil {
		return false
	}
	return exists
}

func (s *PostgreSQLStorage) List(ctx context.Context, prefix string, recursive bool) ([]string, error) {
	pattern := prefix + "%"
	keys, err := s.q.CertMagicList(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("list certmagic keys: %w", err)
	}
	return keys, nil
}

func (s *PostgreSQLStorage) Stat(ctx context.Context, key string) (certmagic.KeyInfo, error) {
	row, err := s.q.CertMagicLoad(ctx, key)
	if err != nil {
		return certmagic.KeyInfo{}, fs.ErrNotExist
	}
	return certmagic.KeyInfo{
		Key:        key,
		Modified:   row.Modified.Time,
		Size:       int64(len(row.Value)),
		IsTerminal: true,
	}, nil
}

func (s *PostgreSQLStorage) String() string {
	return "PostgreSQLStorage"
}

// certmagicLoadRow matches the sqlc return type.
type certmagicLoadRow struct {
	Value    []byte
	Modified time.Time
}
