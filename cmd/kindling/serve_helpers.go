package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

var closedPoolExitOnce sync.Once

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func detectInternalIP() string {
	if v := strings.TrimSpace(os.Getenv("KINDLING_INTERNAL_IP")); v != "" {
		return v
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			return ip.String()
		}
	}
	return "127.0.0.1"
}

func cloudHypervisorVersion() string {
	out, err := exec.Command("cloud-hypervisor", "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

type serverComponentStatusUpdate struct {
	Component        string
	Status           string
	ObservedAt       time.Time
	LastSuccessAt    *time.Time
	LastErrorAt      *time.Time
	LastErrorMessage string
	Metadata         map[string]any
}

func persistServerComponentStatus(ctx context.Context, q *queries.Queries, serverID uuid.UUID, update serverComponentStatusUpdate) error {
	observedAt := pgtype.Timestamptz{Time: update.ObservedAt.UTC(), Valid: !update.ObservedAt.IsZero()}
	lastSuccessAt := pgtype.Timestamptz{}
	if update.LastSuccessAt != nil {
		lastSuccessAt = pgtype.Timestamptz{Time: update.LastSuccessAt.UTC(), Valid: true}
	}
	lastErrorAt := pgtype.Timestamptz{}
	if update.LastErrorAt != nil {
		lastErrorAt = pgtype.Timestamptz{Time: update.LastErrorAt.UTC(), Valid: true}
	}
	metadata := []byte("{}")
	if len(update.Metadata) > 0 {
		b, err := json.Marshal(update.Metadata)
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
		metadata = b
	}
	return q.ServerComponentStatusUpsert(ctx, queries.ServerComponentStatusUpsertParams{
		ServerID:         pgtype.UUID{Bytes: serverID, Valid: true},
		Component:        update.Component,
		Status:           update.Status,
		ObservedAt:       observedAt,
		LastSuccessAt:    lastSuccessAt,
		LastErrorAt:      lastErrorAt,
		LastErrorMessage: strings.TrimSpace(update.LastErrorMessage),
		Metadata:         metadata,
	})
}

func parseServerIPRange() (netip.Prefix, error) {
	ipRange, err := netip.ParsePrefix("10.0.0.0/20")
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse server IP range: %w", err)
	}
	return ipRange, nil
}

func runServerHeartbeat(ctx context.Context, q *queries.Queries, serverID uuid.UUID) {
	ticker := time.NewTicker(serverHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := q.ServerHeartbeat(ctx, pgtype.UUID{Bytes: serverID, Valid: true}); err != nil {
				maybeExitForClosedPool(err, "server heartbeat")
				slog.Error("server heartbeat failed", "error", err)
			}
		}
	}
}

func runServerComponentHeartbeat(ctx context.Context, q *queries.Queries, serverID uuid.UUID, component string, every time.Duration, metadataFn func() map[string]any) {
	if every <= 0 {
		every = componentHeartbeatInterval
	}
	write := func() {
		now := time.Now().UTC()
		var metadata map[string]any
		if metadataFn != nil {
			metadata = metadataFn()
		}
		if err := persistServerComponentStatus(ctx, q, serverID, serverComponentStatusUpdate{
			Component:     component,
			Status:        "healthy",
			ObservedAt:    now,
			LastSuccessAt: &now,
			Metadata:      metadata,
		}); err != nil && ctx.Err() == nil {
			maybeExitForClosedPool(err, "component heartbeat")
			slog.Warn("component status heartbeat", "component", component, "error", err)
		}
	}

	write()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			write()
		}
	}
}

func maybeExitForClosedPool(err error, subsystem string) {
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "closed pool") {
		return
	}
	closedPoolExitOnce.Do(func() {
		slog.Error("database pool closed unexpectedly; exiting for systemd restart", "subsystem", subsystem, "error", err)
		go func() {
			time.Sleep(10 * time.Millisecond)
			os.Exit(1)
		}()
	})
}
