package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

type serverRegistrationStore interface {
	FindServerByID(ctx context.Context, serverID uuid.UUID) (queries.Server, bool, error)
	CountOtherServers(ctx context.Context, serverID uuid.UUID) (int, error)
	RegisterExistingServer(ctx context.Context, serverID uuid.UUID, hostname, internalIP string, ipRange netip.Prefix) (queries.Server, error)
	AllocateAndRegisterServer(ctx context.Context, serverID uuid.UUID, hostname, internalIP string) (queries.Server, error)
	EnsureServerSettings(ctx context.Context, serverID uuid.UUID) error
}

type pgServerRegistrationStore struct {
	pool *pgxpool.Pool
	q    *queries.Queries
}

func (s pgServerRegistrationStore) FindServerByID(ctx context.Context, serverID uuid.UUID) (queries.Server, bool, error) {
	server, err := s.q.ServerFindByID(ctx, pgtype.UUID{Bytes: serverID, Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return queries.Server{}, false, nil
		}
		return queries.Server{}, false, err
	}
	return server, true, nil
}

func (s pgServerRegistrationStore) RegisterExistingServer(ctx context.Context, serverID uuid.UUID, hostname, internalIP string, ipRange netip.Prefix) (queries.Server, error) {
	return s.q.ServerRegister(ctx, queries.ServerRegisterParams{
		ID:         pgtype.UUID{Bytes: serverID, Valid: true},
		Hostname:   hostname,
		InternalIp: internalIP,
		IpRange:    ipRange,
	})
}

func (s pgServerRegistrationStore) CountOtherServers(ctx context.Context, serverID uuid.UUID) (int, error) {
	servers, err := s.q.ServerFindAll(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, server := range servers {
		if server.ID.Valid && server.ID.Bytes == serverID {
			continue
		}
		count++
	}
	return count, nil
}

func (s pgServerRegistrationStore) AllocateAndRegisterServer(ctx context.Context, serverID uuid.UUID, hostname, internalIP string) (queries.Server, error) {
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
		ID:         pgtype.UUID{Bytes: serverID, Valid: true},
		Hostname:   hostname,
		InternalIp: internalIP,
		IpRange:    ipRange,
	})
	if err != nil {
		return queries.Server{}, fmt.Errorf("register server: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return queries.Server{}, fmt.Errorf("commit: %w", err)
	}
	return server, nil
}

func (s pgServerRegistrationStore) EnsureServerSettings(ctx context.Context, serverID uuid.UUID) error {
	return s.q.ServerSettingEnsure(ctx, pgtype.UUID{Bytes: serverID, Valid: true})
}

func registerServer(ctx context.Context, store serverRegistrationStore, serverID uuid.UUID, host, internalIP string) (queries.Server, error) {
	existing, found, err := store.FindServerByID(ctx, serverID)
	if err != nil {
		return queries.Server{}, fmt.Errorf("find existing server: %w", err)
	}
	otherCount, err := store.CountOtherServers(ctx, serverID)
	if err != nil {
		return queries.Server{}, fmt.Errorf("count existing servers: %w", err)
	}
	if otherCount > 0 && isLoopbackOrUnspecifiedIP(internalIP) {
		return queries.Server{}, fmt.Errorf("multi-server registration requires a non-loopback internal IP; got %q", internalIP)
	}

	var server queries.Server
	if found {
		server, err = store.RegisterExistingServer(ctx, serverID, host, internalIP, existing.IpRange)
		if err != nil {
			return queries.Server{}, fmt.Errorf("re-register server: %w", err)
		}
	} else {
		server, err = store.AllocateAndRegisterServer(ctx, serverID, host, internalIP)
		if err != nil {
			return queries.Server{}, fmt.Errorf("allocate and register server: %w", err)
		}
	}

	if err := store.EnsureServerSettings(ctx, serverID); err != nil {
		return queries.Server{}, fmt.Errorf("ensure server settings: %w", err)
	}
	return server, nil
}

func validateSharedDatabaseEntryPoint(ctx context.Context, store serverRegistrationStore, serverID uuid.UUID, databaseURL string) error {
	otherCount, err := store.CountOtherServers(ctx, serverID)
	if err != nil {
		return fmt.Errorf("count existing servers: %w", err)
	}
	if otherCount == 0 {
		return nil
	}

	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database URL: %w", err)
	}
	hosts := strings.Split(cfg.Host, ",")
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if !isLoopbackLikeHost(host) {
			return nil
		}
	}
	return fmt.Errorf("multi-server cluster requires a shared non-local database entrypoint; got %q", cfg.Host)
}

func isLoopbackOrUnspecifiedIP(raw string) bool {
	host := normalizeHostForLoopbackCheck(raw)
	switch strings.ToLower(host) {
	case "", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}

func isLoopbackLikeHost(host string) bool {
	host = normalizeHostForLoopbackCheck(host)
	if strings.HasPrefix(host, "/") {
		return true
	}
	return isLoopbackOrUnspecifiedIP(host)
}

func normalizeHostForLoopbackCheck(host string) string {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]")
	host = strings.TrimSuffix(host, ".")
	return host
}
