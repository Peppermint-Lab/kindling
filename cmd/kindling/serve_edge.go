package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/edgeproxy"
	"github.com/kindlingvm/kindling/internal/rpc"
)

// controlPlaneEdgeHostsFromDB returns unique hostnames (API + dashboard) and the
// loopback origin when at least one HTTPS control-plane hostname is configured.
func controlPlaneEdgeHostsFromDB(ctx context.Context, q *queries.Queries, listenAddr string) ([]string, *url.URL, error) {
	apiHost, err := publicAPIHostnameFromDB(ctx, q)
	if err != nil {
		return nil, nil, fmt.Errorf("public API hostname: %w", err)
	}
	dashHost, err := dashboardHostnameFromDB(ctx, q)
	if err != nil {
		return nil, nil, fmt.Errorf("dashboard hostname: %w", err)
	}

	apiURL, err := url.Parse(loopbackAPIOrigin(listenAddr))
	if err != nil {
		return nil, nil, fmt.Errorf("parse loopback API URL: %w", err)
	}
	if apiURL.Scheme != "http" {
		return nil, nil, fmt.Errorf("api backend must be http, got %q", apiURL.Scheme)
	}

	var hosts []string
	add := func(h string) {
		if h == "" {
			return
		}
		for _, x := range hosts {
			if strings.EqualFold(x, h) {
				return
			}
		}
		hosts = append(hosts, strings.ToLower(h))
	}
	add(apiHost)
	add(dashHost)

	if len(hosts) == 0 {
		return nil, nil, nil
	}
	return hosts, apiURL, nil
}

func publicAPIHostnameFromDB(ctx context.Context, q *queries.Queries) (string, error) {
	v, err := q.ClusterSettingGet(ctx, rpc.ClusterSettingKeyPublicBaseURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	raw := rpc.NormalizePublicBaseURL(v)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", nil
	}
	if u.Scheme != "https" {
		return "", nil
	}
	h := strings.ToLower(u.Hostname())
	if net.ParseIP(h) != nil {
		return "", nil
	}
	return h, nil
}

func dashboardHostnameFromDB(ctx context.Context, q *queries.Queries) (string, error) {
	v, err := q.ClusterSettingGet(ctx, rpc.ClusterSettingKeyDashboardPublicHost)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return rpc.NormalizeDashboardPublicHost(v), nil
}

// startEdgeProxy creates and starts the edge proxy if conditions are met.
func startEdgeProxy(
	ctx context.Context,
	components serveComponents,
	snap *config.Snapshot,
	db *database.DB,
	routeChangeCh chan struct{},
	q *queries.Queries,
	listenAddr string,
	serverID uuid.UUID,
) error {
	if !components.edge || snap.EdgeHTTPSAddr == "" {
		return nil
	}
	coldStart := snap.ColdStartTimeout
	edgeHTTP := snap.EdgeHTTPAddr
	if edgeHTTP == "" {
		edgeHTTP = ":80"
	}
	cpHosts, apiBackend, err := controlPlaneEdgeHostsFromDB(ctx, q, listenAddr)
	if err != nil {
		return fmt.Errorf("control plane edge: %w", err)
	}
	if len(cpHosts) > 0 && apiBackend != nil {
		slog.Info("edge control plane proxy", "hosts", cpHosts, "api", apiBackend.String())
	}
	edgeSvc, err := edgeproxy.New(edgeproxy.Config{
		HTTPAddr:          edgeHTTP,
		HTTPSAddr:         snap.EdgeHTTPSAddr,
		ACMEEmail:         snap.ACMEEmail,
		ACMEStaging:       snap.ACMEStaging,
		Pool:              db.Pool,
		RouteChangeNotify: routeChangeCh,
		ColdStartTimeout:  coldStart,
		ControlPlaneHosts: cpHosts,
		APIBackend:        apiBackend,
		ServerID:          serverID,
	})
	if err != nil {
		return fmt.Errorf("edge proxy: %w", err)
	}
	if err := edgeSvc.Start(ctx); err != nil {
		return fmt.Errorf("edge proxy start: %w", err)
	}
	slog.Info("edge proxy started", "https", snap.EdgeHTTPSAddr, "http", edgeHTTP)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancel()
		_ = edgeSvc.Stop(shutdownCtx)
	}()
	return nil
}

func loopbackAPIOrigin(listenAddr string) string {
	if host, port, err := net.SplitHostPort(listenAddr); err == nil {
		if host == "" || host == "0.0.0.0" || host == "[::]" || host == "::" {
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port)
	}
	if strings.HasPrefix(listenAddr, ":") {
		return "http://127.0.0.1" + listenAddr
	}
	return "http://" + listenAddr
}
