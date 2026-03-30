// Package server manages the server lifecycle: registration, heartbeats,
// leader election via PG advisory locks, and dead server detection.
//
// Each server persists a stable UUID on first boot using the shared bootstrap helper.
// On startup it registers with PostgreSQL, allocating an IP range for
// its VMs. The leader runs cluster-wide duties (dead server detection,
// VM failover).
package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/bootstrap"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

const (
	heartbeatInterval     = 10 * time.Second
	deadDetectionInterval = 30 * time.Second
	leaderRetryInterval   = 5 * time.Second
)

// Config holds server configuration.
type Config struct {
	// DatabaseURL is the primary connection string (may go through PgBouncer).
	DatabaseURL string

	// DatabaseDirectURL is a direct connection string for advisory locks.
	// Session-scoped advisory locks require a direct connection, not pooled.
	DatabaseDirectURL string

	// InternalIP is this server's IP on the internal network (for cross-server routing).
	InternalIP string
}

// Server represents a running kindling server instance.
type Server struct {
	cfg      Config
	id       uuid.UUID
	pool     *pgxpool.Pool
	q        *queries.Queries
	isLeader bool

	onLeadershipAcquired func(ctx context.Context)
}

// New creates a new server instance.
func New(cfg Config, pool *pgxpool.Pool) (*Server, error) {
	id, err := loadOrCreateServerID()
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:  cfg,
		id:   id,
		pool: pool,
		q:    queries.New(pool),
	}, nil
}

// ID returns the server's stable UUID.
func (s *Server) ID() uuid.UUID { return s.id }

// IsLeader returns whether this server currently holds the leader lock.
func (s *Server) IsLeader() bool { return s.isLeader }

// OnLeadershipAcquired sets a callback invoked when this server becomes leader.
func (s *Server) OnLeadershipAcquired(fn func(ctx context.Context)) {
	s.onLeadershipAcquired = fn
}

// Register upserts this server into the servers table.
// On first registration, an IP range is allocated within a transaction.
func (s *Server) Register(ctx context.Context) (queries.Server, error) {
	hostname, _ := os.Hostname()

	// Check if this server already exists (restart case).
	existing, err := s.q.ServerFindByID(ctx, pguuid.ToPgtype(s.id))
	if err == nil {
		server, err := s.q.ServerRegister(ctx, queries.ServerRegisterParams{
			ID:         pguuid.ToPgtype(s.id),
			Hostname:   hostname,
			InternalIp: s.cfg.InternalIP,
			IpRange:    existing.IpRange,
		})
		if err != nil {
			return queries.Server{}, fmt.Errorf("re-register server: %w", err)
		}
		slog.Info("re-registered server", "server_id", s.id, "ip_range", server.IpRange)
		return server, nil
	}

	// New server — allocate IP range in a transaction.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return queries.Server{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := s.q.WithTx(tx)

	if err := qtx.AdvisoryLock(ctx, "ServerAllocateIPRange"); err != nil {
		return queries.Server{}, fmt.Errorf("advisory lock: %w", err)
	}

	ipRange, err := qtx.ServerAllocateIPRange(ctx)
	if err != nil {
		return queries.Server{}, fmt.Errorf("allocate IP range: %w", err)
	}

	server, err := qtx.ServerRegister(ctx, queries.ServerRegisterParams{
		ID:         pguuid.ToPgtype(s.id),
		Hostname:   hostname,
		InternalIp: s.cfg.InternalIP,
		IpRange:    ipRange,
	})
	if err != nil {
		return queries.Server{}, fmt.Errorf("register server: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return queries.Server{}, fmt.Errorf("commit: %w", err)
	}

	slog.Info("registered new server", "server_id", s.id, "ip_range", server.IpRange)
	return server, nil
}

// RunHeartbeat sends periodic heartbeats. Blocks until ctx is cancelled.
func (s *Server) RunHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.q.ServerHeartbeat(ctx, pguuid.ToPgtype(s.id)); err != nil {
				slog.Error("heartbeat failed", "error", err)
			}
		}
	}
}

// RunLeaderElection continuously tries to acquire the cluster leader lock.
// When acquired, it runs cluster-wide duties until the connection drops or
// ctx is cancelled. Blocks until ctx is cancelled.
func (s *Server) RunLeaderElection(ctx context.Context) {
	ticker := time.NewTicker(leaderRetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tryBecomeLeader(ctx)
		}
	}
}

func (s *Server) tryBecomeLeader(ctx context.Context) {
	// Use a direct connection (not pooled) for session-scoped advisory locks.
	conn, err := pgx.Connect(ctx, s.cfg.DatabaseDirectURL)
	if err != nil {
		slog.Error("leader election connect failed", "error", err)
		return
	}
	defer conn.Close(context.Background())

	q := queries.New(conn)
	acquired, err := q.TrySessionAdvisoryLock(ctx, "cluster_leader")
	if err != nil {
		slog.Error("leader election lock failed", "error", err)
		return
	}
	if !acquired {
		return // another server is leader
	}

	s.isLeader = true
	defer func() { s.isLeader = false }()

	slog.Info("acquired cluster leadership", "server_id", s.id)

	if s.onLeadershipAcquired != nil {
		s.onLeadershipAcquired(ctx)
	}

	// Run dead server detection loop while we hold the lock.
	ticker := time.NewTicker(deadDetectionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.detectDeadServers(ctx); err != nil {
				slog.Error("dead server detection failed", "error", err)
			}
		}
	}
}

func (s *Server) detectDeadServers(ctx context.Context) error {
	dead, err := s.q.ServerFindDead(ctx)
	if err != nil {
		return fmt.Errorf("find dead servers: %w", err)
	}

	for _, srv := range dead {
		slog.Warn("detected dead server",
			"server_id", srv.ID,
			"hostname", srv.Hostname,
			"last_heartbeat", srv.LastHeartbeatAt,
		)

		if err := s.q.ServerUpdateStatus(ctx, queries.ServerUpdateStatusParams{
			ID:     srv.ID,
			Status: "dead",
		}); err != nil {
			slog.Error("failed to mark server dead", "server_id", srv.ID, "error", err)
			continue
		}

		vmIDs, err := s.q.DeploymentInstanceVMIDsByServerID(ctx, srv.ID)
		if err != nil {
			slog.Error("list instance vms for dead server", "server_id", srv.ID, "error", err)
			continue
		}
		for _, vid := range vmIDs {
			if vid.Valid {
				if err := s.q.VMSoftDelete(ctx, vid); err != nil {
					slog.Error("soft-delete vm on dead server", "vm_id", vid, "error", err)
				}
			}
		}
		if err := s.q.DeploymentInstanceResetForDeadServer(ctx, srv.ID); err != nil {
			slog.Error("reset deployment instances for dead server", "server_id", srv.ID, "error", err)
		}
	}

	return nil
}

// loadOrCreateServerID reads the server ID from disk, or generates and persists a new one.
func loadOrCreateServerID() (uuid.UUID, error) {
	return bootstrap.LoadOrCreateServerID()
}
