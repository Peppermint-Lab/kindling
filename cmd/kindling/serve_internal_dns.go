package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/database/queries"
	crunrt "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/workerdns"
)

type internalDNSServerStore interface {
	FindServerByID(ctx context.Context, serverID uuid.UUID) (queries.Server, bool, error)
}

func startWorkerInternalDNS(ctx context.Context, q *queries.Queries, serverID uuid.UUID, rt crunrt.Runtime) error {
	if rt == nil || !internalDNSEnabledForRuntime(rt.Name()) {
		return nil
	}

	addr := strings.TrimSpace(os.Getenv("KINDLING_INTERNAL_DNS_ADDR"))
	allowedPrefix, err := resolveInternalDNSAllowedPrefix(ctx, pgServerRegistrationStore{q: q}, serverID)
	if err != nil {
		return fmt.Errorf("resolve internal dns client range: %w", err)
	}

	upstreams := splitCSV(os.Getenv("KINDLING_INTERNAL_DNS_UPSTREAMS"))
	server := workerdns.NewServer(workerdns.Config{
		Addr:                addr,
		AllowedClientPrefix: allowedPrefix,
		Upstreams:           upstreams,
	}, workerdns.NewResolver(q))
	if err := server.Start(ctx); err != nil {
		return fmt.Errorf("start internal dns: %w", err)
	}

	slog.Info("internal dns started", "addr", effectiveInternalDNSAddr(addr), "allowed_prefix", allowedPrefix.String())
	return nil
}

func resolveInternalDNSAllowedPrefix(ctx context.Context, store internalDNSServerStore, serverID uuid.UUID) (netip.Prefix, error) {
	server, found, err := store.FindServerByID(ctx, serverID)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("find server: %w", err)
	}
	if !found {
		return netip.Prefix{}, fmt.Errorf("server %s not registered", serverID)
	}
	return server.IpRange, nil
}

func internalDNSEnabledForRuntime(runtimeName string) bool {
	if runtimeName != "cloud-hypervisor" {
		return false
	}
	addr := strings.TrimSpace(os.Getenv("KINDLING_INTERNAL_DNS_ADDR"))
	switch strings.ToLower(addr) {
	case "off", "disabled", "false":
		return false
	default:
		return true
	}
}

func internalDNSRuntimeMetadata(runtimeName string) map[string]any {
	if runtimeName != "cloud-hypervisor" {
		return nil
	}
	meta := map[string]any{
		"internal_dns_enabled": internalDNSEnabledForRuntime(runtimeName),
	}
	if meta["internal_dns_enabled"] == true {
		meta["internal_dns_addr"] = effectiveInternalDNSAddr(strings.TrimSpace(os.Getenv("KINDLING_INTERNAL_DNS_ADDR")))
	}
	return meta
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func effectiveInternalDNSAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ":53"
	}
	return addr
}
