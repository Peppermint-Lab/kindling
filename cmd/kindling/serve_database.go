package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/oci"
	"github.com/kindlingvm/kindling/internal/rpc"
)

// seedAllSettings seeds all cluster and server settings from serve flags and environment.
func seedAllSettings(ctx context.Context, q *queries.Queries, serverID uuid.UUID, opts serveOptions, components serveComponents) error {
	if err := seedPublicBaseURLIfUnset(ctx, q, opts.publicBaseURL); err != nil {
		return fmt.Errorf("seed public base url: %w", err)
	}
	dashSeed := strings.TrimSpace(opts.dashboardHost)
	if dashSeed == "" {
		dashSeed = strings.TrimSpace(os.Getenv("KINDLING_DASHBOARD_HOST"))
	}
	if err := seedDashboardPublicHostIfUnset(ctx, q, dashSeed); err != nil {
		return fmt.Errorf("seed dashboard public host: %w", err)
	}
	if err := seedClusterSettingsFromServeFlags(ctx, q, opts); err != nil {
		return fmt.Errorf("seed cluster settings: %w", err)
	}
	if components.worker || components.edge || components.api {
		if err := seedAdvertiseHostIfUnset(ctx, q, serverID, opts); err != nil {
			return fmt.Errorf("seed advertise host: %w", err)
		}
		if err := seedInternalAPIPort(ctx, q, serverID, opts.listenAddr); err != nil {
			return fmt.Errorf("seed internal api port: %w", err)
		}
	}
	if components.worker || components.edge {
		if err := seedCloudHypervisorStateDirIfUnset(ctx, q, serverID); err != nil {
			return fmt.Errorf("seed cloud hypervisor state dir: %w", err)
		}
	}
	return nil
}

func seedPublicBaseURLIfUnset(ctx context.Context, q *queries.Queries, fromFlag string) error {
	fromFlag = rpc.NormalizePublicBaseURL(fromFlag)
	if fromFlag == "" {
		return nil
	}
	_, err := q.ClusterSettingGet(ctx, rpc.ClusterSettingKeyPublicBaseURL)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check public base URL setting: %w", err)
	}
	return q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
		Key:   rpc.ClusterSettingKeyPublicBaseURL,
		Value: fromFlag,
	})
}

func seedDashboardPublicHostIfUnset(ctx context.Context, q *queries.Queries, host string) error {
	return seedClusterSettingIfUnset(ctx, q, rpc.ClusterSettingKeyDashboardPublicHost, host)
}

func seedClusterSettingsFromServeFlags(ctx context.Context, q *queries.Queries, opts serveOptions) error {
	if err := seedClusterSettingIfUnset(ctx, q, config.SettingEdgeHTTPSAddr, opts.edgeHTTPSAddr); err != nil {
		return fmt.Errorf("seed edge HTTPS addr: %w", err)
	}
	if err := seedClusterSettingIfUnset(ctx, q, config.SettingEdgeHTTPAddr, opts.edgeHTTPAddr); err != nil {
		return fmt.Errorf("seed edge HTTP addr: %w", err)
	}
	if err := seedClusterSettingIfUnset(ctx, q, config.SettingACMEEmail, opts.acmeEmail); err != nil {
		return fmt.Errorf("seed ACME email: %w", err)
	}
	if opts.acmeStaging {
		if err := seedClusterSettingIfUnset(ctx, q, config.SettingACMEStaging, "true"); err != nil {
			return fmt.Errorf("seed ACME staging: %w", err)
		}
	}
	return nil
}

func seedAdvertiseHostIfUnset(ctx context.Context, q *queries.Queries, serverID uuid.UUID, opts serveOptions) error {
	host := strings.TrimSpace(opts.advertiseHost)
	if host == "" {
		host = strings.TrimSpace(os.Getenv("KINDLING_RUNTIME_ADVERTISE_HOST"))
	}
	if host == "" {
		return nil
	}
	return q.ServerSettingSeedAdvertiseHostIfUnset(ctx, queries.ServerSettingSeedAdvertiseHostIfUnsetParams{
		ServerID:      pgtype.UUID{Bytes: serverID, Valid: true},
		AdvertiseHost: host,
	})
}

func seedCloudHypervisorStateDirIfUnset(ctx context.Context, q *queries.Queries, serverID uuid.UUID) error {
	stateDir := strings.TrimSpace(os.Getenv("KINDLING_CH_STATE_DIR"))
	if stateDir == "" {
		return nil
	}
	return q.ServerSettingSeedCloudHypervisorStateDirIfUnset(ctx, queries.ServerSettingSeedCloudHypervisorStateDirIfUnsetParams{
		ServerID:                pgtype.UUID{Bytes: serverID, Valid: true},
		CloudHypervisorStateDir: stateDir,
	})
}

func seedInternalAPIPort(ctx context.Context, q *queries.Queries, serverID uuid.UUID, listenAddr string) error {
	port, err := parseListenPort(listenAddr)
	if err != nil {
		return err
	}
	return q.ServerSettingUpsertInternalAPIPort(ctx, queries.ServerSettingUpsertInternalAPIPortParams{
		ServerID:        pgtype.UUID{Bytes: serverID, Valid: true},
		InternalApiPort: int32(port),
	})
}

func parseListenPort(listenAddr string) (int, error) {
	listenAddr = strings.TrimSpace(listenAddr)
	if listenAddr == "" {
		return 8080, nil
	}
	_, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		if !strings.Contains(listenAddr, ":") {
			portStr = listenAddr
		} else if strings.HasPrefix(listenAddr, ":") {
			portStr = strings.TrimPrefix(listenAddr, ":")
		} else {
			return 0, fmt.Errorf("parse listen port %q: %w", listenAddr, err)
		}
	}
	portStr = strings.TrimSpace(portStr)
	if portStr == "" {
		return 8080, nil
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid listen port %q", listenAddr)
	}
	return port, nil
}

func ensureInterServerProxySecret(ctx context.Context, q *queries.Queries, cfgMgr *config.Manager) error {
	if cfgMgr == nil {
		return nil
	}
	if existing, err := q.ClusterSecretGet(ctx, config.SecretInterServerProxySharedKey); err == nil && len(existing) > 0 {
		return nil
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check inter-server proxy secret: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate inter-server proxy secret: %w", err)
	}
	enc, err := cfgMgr.EncryptBytes([]byte(base64.RawURLEncoding.EncodeToString(raw)))
	if err != nil {
		return fmt.Errorf("encrypt inter-server proxy secret: %w", err)
	}
	return q.ClusterSecretUpsert(ctx, queries.ClusterSecretUpsertParams{
		Key:        config.SecretInterServerProxySharedKey,
		Ciphertext: enc,
	})
}

func seedClusterSettingIfUnset(ctx context.Context, q *queries.Queries, key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	_, err := q.ClusterSettingGet(ctx, key)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check cluster setting %s: %w", key, err)
	}
	return q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{Key: key, Value: value})
}

func registryAuthFromSnapshot(s *config.Snapshot) *oci.Auth {
	if s == nil {
		return nil
	}
	u := strings.TrimSpace(s.RegistryUsername)
	p := strings.TrimSpace(s.RegistryPassword)
	if u == "" || p == "" {
		return nil
	}
	return &oci.Auth{Username: u, Password: p}
}
