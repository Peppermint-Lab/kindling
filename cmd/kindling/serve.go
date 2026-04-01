package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/bootstrap"
	"github.com/kindlingvm/kindling/internal/ci"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/deploy"
	"github.com/kindlingvm/kindling/internal/rpc"
	crunrt "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/usage"
	"github.com/kindlingvm/kindling/internal/wgmesh"
	"github.com/spf13/cobra"
)

// Serve command duration constants.
const serverHeartbeatInterval = 10 * time.Second    // how often to write server heartbeat rows
const componentHeartbeatInterval = 10 * time.Second // heartbeat interval for API/edge/worker components
const usagePollerInterval = 15 * time.Second        // resource usage polling interval
const walBackoffMax = 30 * time.Second              // max backoff for WAL listener reconnects
const shutdownGracePeriod = 15 * time.Second        // graceful shutdown timeout for edge proxy
const volumeRecoveryInterval = 1 * time.Minute      // volume operation recovery sweep interval
const periodicReconcileInterval = 30 * time.Second  // interval for idle scale-down, preview cleanup, slow autoscale, etc.

// projectAutoscaleFastInterval is the burst scale-up autoscale tick (HTTP short window only; no scale-down).
const projectAutoscaleFastInterval = 5 * time.Second

func serveCmd() *cobra.Command {
	var (
		listenAddr    string
		publicBaseURL string
		dashboardHost string
		advertiseHost string
		edgeHTTPSAddr string
		edgeHTTPAddr  string
		acmeEmail     string
		acmeStaging   bool
		componentsRaw string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the kindling server",
		RunE: func(cmd *cobra.Command, args []string) error {
			databaseURL, err := bootstrap.ResolvePostgresDSN("")
			if err != nil {
				return fmt.Errorf("resolve postgres DSN: %w", err)
			}
			replicationDSN, err := bootstrap.ResolvePostgresReplicationDSN(databaseURL)
			if err != nil {
				return fmt.Errorf("resolve postgres replication DSN: %w", err)
			}
			return runServe(cmd.Context(), databaseURL, replicationDSN, serveOptions{
				listenAddr:    listenAddr,
				publicBaseURL: publicBaseURL,
				dashboardHost: dashboardHost,
				advertiseHost: advertiseHost,
				edgeHTTPSAddr: edgeHTTPSAddr,
				edgeHTTPAddr:  edgeHTTPAddr,
				acmeEmail:     acmeEmail,
				acmeStaging:   acmeStaging,
				componentsRaw: componentsRaw,
			})
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", ":8080", "API listen address")
	cmd.Flags().StringVar(&publicBaseURL, "public-url", "", "Optional seed for cluster_settings.public_base_url when that row is missing (e.g. first boot)")
	cmd.Flags().StringVar(&dashboardHost, "dashboard-host", "", "Optional seed for cluster_settings.dashboard_public_host when missing (hostname for dashboard SPA, e.g. app.example.com); KINDLING_DASHBOARD_HOST if unset")
	cmd.Flags().StringVar(&advertiseHost, "advertise-host", "", "Optional seed for server_settings.advertise_host when empty (public IP or DNS for browser-openable runtime URLs); KINDLING_RUNTIME_ADVERTISE_HOST if unset")
	cmd.Flags().StringVar(&edgeHTTPSAddr, "edge-https", "", "HTTPS listen for TLS edge proxy (e.g. :443); stored in cluster_settings when missing")
	cmd.Flags().StringVar(&edgeHTTPAddr, "edge-http", ":80", "HTTP listen for edge proxy; stored in cluster_settings.edge_http_addr when missing")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", "", "Let's Encrypt email; stored in cluster_settings when missing")
	cmd.Flags().BoolVar(&acmeStaging, "acme-staging", false, "Use Let's Encrypt staging CA; stored in cluster_settings when missing")
	cmd.Flags().StringVar(&componentsRaw, "components", "", "Comma-separated serve components: api, edge, worker (default: api,edge,worker; env KINDLING_COMPONENTS)")

	return cmd
}

type serveOptions struct {
	listenAddr    string
	publicBaseURL string
	dashboardHost string
	advertiseHost string
	edgeHTTPSAddr string
	edgeHTTPAddr  string
	acmeEmail     string
	acmeStaging   bool
	componentsRaw string
}

func runServe(ctx context.Context, databaseURL, replicationDSN string, opts serveOptions) error {
	listenAddr := opts.listenAddr
	components, err := resolveServeComponents(opts.componentsRaw)
	if err != nil {
		return fmt.Errorf("resolve serve components: %w", err)
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("starting kindling", "listen", listenAddr, "api", components.api, "edge", components.edge, "worker", components.worker)

	// Connect to PostgreSQL and run migrations.
	db, err := database.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()
	slog.Info("connected to PostgreSQL")

	if err := database.Migrate(ctx, db.Pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	slog.Info("schema migrated")

	serverID, err := bootstrap.LoadOrCreateServerID()
	if err != nil {
		return fmt.Errorf("server id: %w", err)
	}
	q := queries.New(db.Pool)
	regStore := pgServerRegistrationStore{
		pool: db.Pool,
		q:    q,
	}

	// Master key is required for cluster secrets and config manager paths.
	masterKey, err := bootstrap.LoadOrCreateMasterKey()
	if err != nil {
		return fmt.Errorf("master key: %w", err)
	}

	// Register server and seed cluster/server settings.
	if components.worker || components.edge {
		if err := validateSharedDatabaseEntryPoint(ctx, regStore, serverID, databaseURL); err != nil {
			return err
		}
		regIP, err := resolveServerRegistrationIP(ctx, regStore, serverID)
		if err != nil {
			return err
		}
		if err := registerServerAndHeartbeat(ctx, db.Pool, q, serverID, regIP); err != nil {
			return err
		}
		if wgmesh.Enabled() {
			priv, err := wgmesh.EnsurePrivateKey()
			if err != nil {
				return fmt.Errorf("wireguard private key: %w", err)
			}
			ep := wgmesh.Endpoint()
			if ep == "" {
				return fmt.Errorf("KINDLING_WG_ENDPOINT is required when KINDLING_WG_MESH is enabled (UDP endpoint peers use to reach this host)")
			}
			wgIP := wgmesh.OverlayIP(serverID)
			pub := priv.PublicKey()
			if err := q.ServerWireGuardSet(ctx, queries.ServerWireGuardSetParams{
				ID:                 pgtype.UUID{Bytes: serverID, Valid: true},
				WireguardIp:        wgIP,
				WireguardPublicKey: pub.String(),
				WireguardEndpoint:  ep,
			}); err != nil {
				return fmt.Errorf("publish wireguard server metadata: %w", err)
			}
			go wgmesh.RunReconcileLoop(ctx, q, serverID, priv)
			listenPort, lpErr := wgmesh.ListenPort()
			if lpErr == nil {
				slog.Info("wireguard mesh enabled", "iface", wgmesh.IfaceName, "overlay_ip", wgIP.String(), "listen_port", listenPort)
			} else {
				slog.Warn("wireguard mesh enabled with invalid KINDLING_WG_LISTEN_PORT", "iface", wgmesh.IfaceName, "overlay_ip", wgIP.String(), "error", lpErr)
			}
		}
	}
	if err := seedAllSettings(ctx, q, serverID, opts, components); err != nil {
		return err
	}

	// Secrets backfill and config manager.
	backfilledSecrets, err := config.BackfillProjectSecrets(ctx, q, masterKey)
	if err != nil {
		return fmt.Errorf("backfill project secrets: %w", err)
	}
	if backfilledSecrets > 0 {
		slog.Info("backfilled legacy project secrets", "count", backfilledSecrets)
	}

	cfgMgr := config.NewManager(db.Pool, serverID, masterKey)
	if err := ensureInterServerProxySecret(ctx, q, cfgMgr); err != nil {
		return fmt.Errorf("ensure inter-server proxy secret: %w", err)
	}
	if err := cfgMgr.Reload(ctx); err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	go func() {
		if err := cfgMgr.RunListen(ctx); err != nil && ctx.Err() == nil {
			slog.Error("config listen ended", "error", err)
		}
	}()
	if components.api || components.edge {
		go usage.RunHostMetricsPoller(ctx, q, serverID, componentHeartbeatInterval, func() string {
			snap := cfgMgr.Snapshot()
			if snap != nil && strings.TrimSpace(snap.ServerCloudHypervisorStateDir) != "" {
				return snap.ServerCloudHypervisorStateDir
			}
			if v := strings.TrimSpace(os.Getenv("KINDLING_CH_STATE_DIR")); v != "" {
				return v
			}
			if _, err := os.Stat("/data"); err == nil {
				return "/data/kindling-runtime/cloud-hypervisor"
			}
			return ""
		})
	}

	routeChangeCh := make(chan struct{}, 1)
	notifyRouteChange := func() {
		select {
		case routeChangeCh <- struct{}{}:
		default:
		}
	}

	snap := cfgMgr.Snapshot()

	// Worker: runtime, deployer, reconcilers.
	var recs reconcilers
	var deployer *deploy.Deployer
	var rt crunrt.Runtime
	var ciSvc interface {
		Cancel(context.Context, uuid.UUID) error
		CreateLocalWorkflowJob(context.Context, ci.CreateJobRequest) (queries.CiJob, error)
		HandleGitHubWorkflowJobEvent(context.Context, ci.GitHubWorkflowJobEvent) (ci.GitHubWorkflowJobHandleResult, error)
	}
	if components.worker {
		w, werr := setupWorker(ctx, q, db, serverID, cfgMgr, snap, notifyRouteChange)
		if werr != nil {
			return werr
		}
		rt, deployer, ciSvc, recs = w.rt, w.deployer, w.ciSvc, w.recs
		if err := startWorkerInternalDNS(ctx, q, serverID, rt); err != nil {
			return err
		}
		if shouldStopWorkloadsOnShutdown() {
			defer rt.StopAll()
		} else {
			slog.Info("preserving workloads on shutdown", "env", "KINDLING_PRESERVE_WORKLOADS_ON_SHUTDOWN")
		}
		startWorkerHeartbeats(ctx, q, serverID, rt, w.hostRuntime)
		startReconcilers(ctx, q, serverID, rt, recs, notifyRouteChange)
	}

	// In split-mode deployments the API process still needs a CI job service so
	// webhook-driven GitHub Actions runner events can be accepted and persisted.
	// The worker process picks up the resulting ci_jobs rows via WAL and performs
	// the actual provisioning/reconciliation work.
	if components.api && ciSvc == nil {
		ciSvc = ci.NewJobService(q, cfgMgr, serverID)
	}
	// Component heartbeats for tracked servers.
	serverTracked := components.worker || components.edge
	if serverTracked && components.api {
		go runServerComponentHeartbeat(ctx, q, serverID, "api", componentHeartbeatInterval, nil)
	}
	if serverTracked && components.edge {
		go runServerComponentHeartbeat(ctx, q, serverID, "edge", componentHeartbeatInterval, nil)
	}

	// Dashboard event broker.
	var dashboardEvents *rpc.DashboardEventBroker
	if components.api {
		dashboardEvents = rpc.NewDashboardEventBroker()
	}
	publishDeploymentScopes := func(projectID uuid.UUID) {
		if dashboardEvents == nil {
			return
		}
		dashboardEvents.PublishMany(
			rpc.TopicDeployments,
			rpc.TopicProject(projectID),
			rpc.TopicProjectDeployments(projectID),
		)
	}
	publishCIScopes := func(projectID uuid.UUID) {
		if dashboardEvents == nil {
			return
		}
		dashboardEvents.PublishMany(
			rpc.TopicCIJobs,
			rpc.TopicProject(projectID),
			rpc.TopicProjectCIJobs(projectID),
		)
	}
	publishCIJob := func(jobID uuid.UUID) {
		if dashboardEvents == nil {
			return
		}
		dashboardEvents.Publish(rpc.TopicCIJob(jobID))
	}
	if deployer != nil {
		deployer.SetDashboardPublishers(publishDeploymentScopes, func() {
			if dashboardEvents == nil {
				return
			}
			dashboardEvents.Publish(rpc.TopicServers)
		})
	}
	if ciDashboardPublisher, ok := ciSvc.(interface{ SetDashboardPublisher(func(uuid.UUID)) }); ok {
		ciDashboardPublisher.SetDashboardPublisher(publishCIScopes)
	}
	if ciJobDashboardPublisher, ok := ciSvc.(interface{ SetJobDashboardPublisher(func(uuid.UUID)) }); ok {
		ciJobDashboardPublisher.SetJobDashboardPublisher(publishCIJob)
	}

	// WAL listener.
	wal := newWALListener(replicationDSN, walDeps{
		q:                    q,
		deploymentReconciler: recs.deployment,
		buildReconciler:      recs.build,
		ciJobReconciler:      recs.ciJob,
		vmReconciler:         recs.vm,
		domainReconciler:     recs.domain,
		serverReconciler:     recs.server,
		migrationReconciler:  recs.migration,
		volumeOpReconciler:   recs.volumeOp,
		dashboardEvents:      dashboardEvents,
		publishDeployScopes:  publishDeploymentScopes,
		notifyRouteChange:    notifyRouteChange,
		edgeEnabled:          components.edge,
	})
	if components.api || components.edge || components.worker {
		startWALListener(ctx, wal)
	}

	// Edge proxy.
	if err := startEdgeProxy(ctx, components, snap, db, routeChangeCh, q, listenAddr, serverID, func(id uuid.UUID) {
		if recs.deployment != nil {
			recs.deployment.ScheduleNow(id)
		}
	}); err != nil {
		return err
	}

	// Worker background loops.
	if components.api || components.edge || components.worker {
		go runDeadServerDetectionLoop(ctx, databaseURL, q)
	}

	if components.worker {
		go runProjectAutoscaleSlowLoop(ctx, replicationDSN, q, recs.deployment)
		go runProjectAutoscaleFastLoop(ctx, replicationDSN, recs.deployment)
		go runIdleScaleDownLoop(ctx, databaseURL, q, recs.deployment, cfgMgr)
		go runPreviewCleanupLoop(ctx, databaseURL, q, recs.deployment)
		go runPreviewIdleScaleDownLoop(ctx, databaseURL, q, recs.deployment, cfgMgr)
		go runBuildRecoveryLoop(ctx, databaseURL, q, recs.build)
		go runWebhookPollingLoop(ctx, databaseURL, q, recs.deployment, cfgMgr)
	}

	internalAPIAddr, err := internalAPIListenAddr(listenAddr, components)
	if err != nil {
		return err
	}

	// API server.
	// In split-mode deployments the worker serves the same authenticated HTTP
	// handlers on an adjacent internal port so the public API can proxy guest
	// traffic without sharing in-memory runtime state.
	if shouldServeHTTP(components) {
		return startAPIServer(ctx, q, serverID, cfgMgr, dashboardEvents, recs.deployment, recs.ciJob, ciSvc, listenAddr)
	}
	if components.worker {
		return startAPIServer(ctx, q, serverID, cfgMgr, dashboardEvents, recs.deployment, recs.ciJob, ciSvc, internalAPIAddr)
	}

	<-ctx.Done()
	return nil
}

func shouldServeHTTP(components serveComponents) bool {
	return components.api
}
