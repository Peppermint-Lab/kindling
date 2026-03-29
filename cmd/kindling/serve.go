package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/bootstrap"
	"github.com/kindlingvm/kindling/internal/ci"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/deploy"
	"github.com/kindlingvm/kindling/internal/rpc"
	crunrt "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/sandbox"
	"github.com/spf13/cobra"
)

// Serve command duration constants.
const serverHeartbeatInterval = 10 * time.Second    // how often to write server heartbeat rows
const componentHeartbeatInterval = 10 * time.Second // heartbeat interval for API/edge/worker components
const usagePollerInterval = 15 * time.Second        // resource usage polling interval
const walBackoffMax = 30 * time.Second              // max backoff for WAL listener reconnects
const shutdownGracePeriod = 15 * time.Second        // graceful shutdown timeout for edge proxy
const volumeRecoveryInterval = 1 * time.Minute      // volume operation recovery sweep interval
const periodicReconcileInterval = 30 * time.Second  // interval for idle scale-down, preview cleanup, etc.

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
			return runServe(cmd.Context(), databaseURL, serveOptions{
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

func runServe(ctx context.Context, databaseURL string, opts serveOptions) error {
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

	serverID := loadServerID()
	q := queries.New(db.Pool)

	// Register server and seed cluster/server settings.
	if components.worker || components.edge {
		if err := registerServerAndHeartbeat(ctx, q, serverID); err != nil {
			return err
		}
	}
	if err := seedAllSettings(ctx, q, serverID, opts, components); err != nil {
		return err
	}

	// Master key, secrets, config manager.
	masterKey, err := bootstrap.LoadOrCreateMasterKey()
	if err != nil {
		return fmt.Errorf("master key: %w", err)
	}
	backfilledSecrets, err := config.BackfillProjectSecrets(ctx, q, masterKey)
	if err != nil {
		return fmt.Errorf("backfill project secrets: %w", err)
	}
	if backfilledSecrets > 0 {
		slog.Info("backfilled legacy project secrets", "count", backfilledSecrets)
	}

	cfgMgr := config.NewManager(db.Pool, serverID, masterKey)
	if err := cfgMgr.Reload(ctx); err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	go func() {
		if err := cfgMgr.RunListen(ctx); err != nil && ctx.Err() == nil {
			slog.Error("config listen ended", "error", err)
		}
	}()

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
	var sandboxSvc *sandbox.Service
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
		rt, deployer, ciSvc, sandboxSvc, recs = w.rt, w.deployer, w.ciSvc, w.sandboxSvc, w.recs
		if err := startWorkerInternalDNS(ctx, q, rt); err != nil {
			return err
		}
		if shouldStopWorkloadsOnShutdown() {
			defer rt.StopAll()
		} else {
			slog.Info("preserving workloads on shutdown", "env", "KINDLING_PRESERVE_WORKLOADS_ON_SHUTDOWN")
		}
		startWorkerHeartbeats(ctx, q, serverID, rt)
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
	wal := newWALListener(databaseURL, walDeps{
		q:                    q,
		deploymentReconciler: recs.deployment,
		buildReconciler:      recs.build,
		ciJobReconciler:      recs.ciJob,
		vmReconciler:         recs.vm,
		domainReconciler:     recs.domain,
		serverReconciler:     recs.server,
		migrationReconciler:  recs.migration,
		volumeOpReconciler:   recs.volumeOp,
		sandboxReconciler:    recs.sandbox,
		sandboxTplReconciler: recs.sandboxTpl,
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
	if components.worker {
		go runProjectAutoscaleLoop(ctx, databaseURL, q, recs.deployment)
		go runIdleScaleDownLoop(ctx, databaseURL, q, recs.deployment, cfgMgr)
		go runPreviewCleanupLoop(ctx, databaseURL, q, recs.deployment)
		go runPreviewIdleScaleDownLoop(ctx, databaseURL, q, recs.deployment, cfgMgr)
		go runSandboxExpiryLoop(ctx, databaseURL, q, recs.sandbox)
		go runSandboxIdleLoop(ctx, databaseURL, q, recs.sandbox)
	}

	// API server.
	if components.api {
		return startAPIServer(ctx, q, cfgMgr, dashboardEvents, recs.deployment, recs.ciJob, recs.sandbox, recs.sandboxTpl, sandboxSvc, ciSvc, listenAddr)
	}

	<-ctx.Done()
	return nil
}
