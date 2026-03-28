package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/kindlingvm/kindling/internal/database/queries"
	crunrt "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/workerdns"
)

func startWorkerInternalDNS(ctx context.Context, q *queries.Queries, rt crunrt.Runtime) error {
	if rt == nil || !internalDNSEnabledForRuntime(rt.Name()) {
		return nil
	}

	addr := strings.TrimSpace(os.Getenv("KINDLING_INTERNAL_DNS_ADDR"))
	allowedPrefix, err := parseServerIPRange()
	if err != nil {
		return fmt.Errorf("parse internal dns client range: %w", err)
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
