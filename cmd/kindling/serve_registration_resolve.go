package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/wgmesh"
)

// resolveServerRegistrationIP returns the address this process should publish in
// servers.internal_ip for registration.
func resolveServerRegistrationIP(ctx context.Context, store serverRegistrationStore, serverID uuid.UUID) (string, error) {
	if wgmesh.Enabled() {
		if err := wgmesh.RequireLinux(); err != nil {
			return "", err
		}
		return wgmesh.OverlayIP(serverID).String(), nil
	}
	otherCount, err := store.CountOtherServers(ctx, serverID)
	if err != nil {
		return "", fmt.Errorf("count servers: %w", err)
	}
	env := strings.TrimSpace(os.Getenv("KINDLING_INTERNAL_IP"))
	if otherCount > 0 && env == "" {
		return "", fmt.Errorf("multi-server cluster requires KINDLING_INTERNAL_IP (set to this host's stable private IPv4). Automatic detection is only used on the first server in the cluster")
	}
	if env != "" {
		if isLoopbackOrUnspecifiedIP(env) {
			return "", fmt.Errorf("KINDLING_INTERNAL_IP must not be loopback or unspecified (got %q)", env)
		}
		return env, nil
	}
	return detectInternalIP(), nil
}
