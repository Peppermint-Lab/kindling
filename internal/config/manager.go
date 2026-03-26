package config

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// Manager holds the latest Snapshot and reloads it when Postgres notifies on channel kindling_config.
type Manager struct {
	pool      *pgxpool.Pool
	q         *queries.Queries
	serverID  uuid.UUID
	masterKey []byte

	snap atomic.Pointer[Snapshot]
}

// NewManager creates a manager. Call Reload before use; use RunListen for hot reload.
func NewManager(pool *pgxpool.Pool, serverID uuid.UUID, masterKey []byte) *Manager {
	m := &Manager{
		pool:      pool,
		q:         queries.New(pool),
		serverID:  serverID,
		masterKey: masterKey,
	}
	m.snap.Store(DefaultSnapshot())
	return m
}

// Reload loads configuration from the database and publishes it to Snapshot().
func (m *Manager) Reload(ctx context.Context) error {
	s, err := LoadSnapshot(ctx, m.q, m.serverID, m.masterKey)
	if err != nil {
		return err
	}
	m.snap.Store(s)
	return nil
}

// Snapshot returns the last successfully loaded configuration. It is never nil after the first successful Reload.
func (m *Manager) Snapshot() *Snapshot {
	return m.snap.Load()
}

// Queries exposes the embedded sqlc queries (same pool as the manager).
func (m *Manager) Queries() *queries.Queries {
	return m.q
}

// RunListen blocks, listening for pg_notify on kindling_config and calling Reload.
// Use a dedicated context cancellation to stop.
func (m *Manager) RunListen(ctx context.Context) error {
	for {
		if err := m.listenOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("config listen disconnected; reconnecting", "error", err)
		}
	}
}

func (m *Manager) listenOnce(ctx context.Context) error {
	acq, err := m.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire for listen: %w", err)
	}
	defer acq.Release()

	conn := acq.Conn()
	if _, err := conn.Exec(ctx, "LISTEN kindling_config"); err != nil {
		return fmt.Errorf("listen kindling_config: %w", err)
	}

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("wait for notification: %w", err)
		}
		_ = n
		if err := m.Reload(ctx); err != nil {
			slog.Warn("config reload failed", "error", err)
		} else {
			slog.Debug("config reloaded", "channel", n.Channel, "payload", n.Payload)
		}
	}
}
