package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/bootstrap"
	"github.com/kindlingvm/kindling/internal/builder"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/deploy"
	"github.com/kindlingvm/kindling/internal/edgeproxy"
	"github.com/kindlingvm/kindling/internal/listener"
	"github.com/kindlingvm/kindling/internal/oci"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/rpc"
	crunrt "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/usage"
	"github.com/kindlingvm/kindling/internal/webhook"
	"github.com/spf13/cobra"
)

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
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the kindling server",
		RunE: func(cmd *cobra.Command, args []string) error {
			databaseURL, err := bootstrap.ResolvePostgresDSN("")
			if err != nil {
				return err
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
}

func runServe(ctx context.Context, databaseURL string, opts serveOptions) error {
	listenAddr := opts.listenAddr
	publicBaseURL := opts.publicBaseURL
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("starting kindling", "listen", listenAddr)

	// Connect to PostgreSQL
	db, err := database.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()
	slog.Info("connected to PostgreSQL")

	// Run schema migration
	if err := database.Migrate(ctx, db.Pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	slog.Info("schema migrated")

	// Set up core services
	serverID := loadServerID()
	q := queries.New(db.Pool)

	// Register this server in the database.
	_, err = q.ServerRegister(ctx, queries.ServerRegisterParams{
		ID:         pgtype.UUID{Bytes: serverID, Valid: true},
		Hostname:   hostname(),
		InternalIp: "127.0.0.1",
		IpRange:    mustParseCIDR("10.0.0.0/20"),
	})
	if err != nil {
		return fmt.Errorf("register server: %w", err)
	}
	slog.Info("server registered", "server_id", serverID)

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := q.ServerHeartbeat(ctx, pgtype.UUID{Bytes: serverID, Valid: true}); err != nil {
					slog.Error("server heartbeat failed", "error", err)
				}
			}
		}
	}()

	if err := q.ServerSettingEnsure(ctx, pgtype.UUID{Bytes: serverID, Valid: true}); err != nil {
		return fmt.Errorf("ensure server settings: %w", err)
	}

	if err := seedPublicBaseURLIfUnset(ctx, q, publicBaseURL); err != nil {
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
	if err := seedAdvertiseHostIfUnset(ctx, q, serverID, opts); err != nil {
		return fmt.Errorf("seed advertise host: %w", err)
	}

	masterKey, err := bootstrap.LoadOrCreateMasterKey()
	if err != nil {
		return fmt.Errorf("master key: %w", err)
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

	snap := cfgMgr.Snapshot()
	pullAuth := registryAuthFromSnapshot(snap)
	rt := crunrt.NewDetectedRuntime(crunrt.HostRuntimeConfig{
		ForceRuntime:  snap.ServerRuntimeOverride,
		AdvertiseHost: snap.ServerAdvertiseHost,
		PullAuth:      pullAuth,
		CloudHypervisor: crunrt.CloudHypervisorHostConfig{
			BinaryPath:    snap.ServerCloudHypervisorBin,
			KernelPath:    snap.ServerCloudHypervisorKernelPath,
			InitramfsPath: snap.ServerCloudHypervisorInitramfsPath,
		},
		AppleKernelPath:    "",
		AppleInitramfsPath: "",
	})
	defer rt.StopAll()
	slog.Info("runtime detected", "runtime", rt.Name())

	go usage.RunResourcePoller(ctx, q, serverID, rt, 15*time.Second)

	bldr := builder.New(func(c context.Context) (builder.Config, error) {
		s := cfgMgr.Snapshot()
		if s == nil {
			return builder.Config{}, fmt.Errorf("config snapshot unavailable")
		}
		return builder.Config{
			GitHubToken:      s.GitHubToken,
			RegistryURL:      s.RegistryURL,
			RegistryUsername: s.RegistryUsername,
			RegistryPassword: s.RegistryPassword,
		}, nil
	}, q, serverID)

	deployer := deploy.New(q, db.Pool, serverID)
	deployer.SetRuntime(rt)

	// Set up reconcilers
	deploymentReconciler := reconciler.New(reconciler.Config{
		Name:      "deployment",
		Reconcile: deployer.ReconcileDeployment,
	})
	deployer.SetReconciler(deploymentReconciler)

	buildReconciler := reconciler.New(reconciler.Config{
		Name:      "build",
		Reconcile: bldr.ReconcileBuild,
	})

	// VM/instance reconciler — currently handled by deployment reconciler.
	vmReconciler := reconciler.New(reconciler.Config{
		Name: "instance",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			slog.Info("reconciling instance", "id", id)
			return nil
		},
	})

	// Route change channel — domain/server reconcilers signal the edge proxy.
	routeChangeCh := make(chan struct{}, 1)
	notifyRouteChange := func() {
		select {
		case routeChangeCh <- struct{}{}:
		default:
		}
	}

	domainReconciler := reconciler.New(reconciler.Config{
		Name: "domain",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			slog.Info("reconciling domain", "id", id)
			notifyRouteChange()
			return nil
		},
	})

	serverReconciler := reconciler.New(reconciler.Config{
		Name: "server",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			slog.Info("reconciling server", "id", id)
			notifyRouteChange()
			return nil
		},
	})

	// Start reconcilers
	go deploymentReconciler.Start(ctx)
	go buildReconciler.Start(ctx)
	go vmReconciler.Start(ctx)
	go domainReconciler.Start(ctx)
	go serverReconciler.Start(ctx)
	slog.Info("reconcilers started")

	// Start WAL listener
	wal := listener.New(listener.Config{
		DatabaseURL: databaseURL,
		OnDeployment: func(ctx context.Context, id uuid.UUID) {
			deploymentReconciler.ScheduleNow(id)
		},
		OnDeploymentInstance: func(ctx context.Context, instanceID uuid.UUID) {
			inst, err := q.DeploymentInstanceFirstByID(ctx, pgtype.UUID{Bytes: instanceID, Valid: true})
			if err == nil && inst.DeploymentID.Valid {
				deploymentReconciler.ScheduleNow(uuid.UUID(inst.DeploymentID.Bytes))
			}
			notifyRouteChange()
		},
		OnProject: func(ctx context.Context, projectID uuid.UUID) {
			dep, err := q.DeploymentLatestRunningByProjectID(ctx, pgtype.UUID{Bytes: projectID, Valid: true})
			if err == nil {
				deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
			}
		},
		OnBuild: func(ctx context.Context, id uuid.UUID) {
			buildReconciler.ScheduleNow(id)
		},
		OnVM: func(ctx context.Context, id uuid.UUID) {
			vmReconciler.ScheduleNow(id)
			dep, err := q.DeploymentFindByVMID(ctx, pgtype.UUID{Bytes: id, Valid: true})
			if err == nil {
				deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
			}
			notifyRouteChange()
		},
		OnDomain: func(ctx context.Context, id uuid.UUID) {
			domainReconciler.ScheduleNow(id)
		},
		OnServer: func(ctx context.Context, id uuid.UUID) {
			serverReconciler.ScheduleNow(id)
		},
	})

	go func() {
		if err := wal.Start(ctx); err != nil && ctx.Err() == nil {
			slog.Error("WAL listener failed", "error", err)
		}
	}()
	slog.Info("WAL listener started")

	if snap.EdgeHTTPSAddr != "" {
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
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = edgeSvc.Stop(shutdownCtx)
		}()
	}

	go runIdleScaleDownLoop(ctx, databaseURL, q, deploymentReconciler, cfgMgr)

	// API server
	api := rpc.NewAPI(q, cfgMgr)
	webhookHandler := webhook.NewHandler(q)

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	// Browsers hit / on the API host; the mux would otherwise 404.
	apiMux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/meta", http.StatusFound)
	})
	api.Register(apiMux)
	apiMux.Handle("POST /webhooks/github", webhookHandler)

	dashHostStr, err := dashboardHostnameFromDB(ctx, q)
	if err != nil {
		return fmt.Errorf("read dashboard hostname: %w", err)
	}

	corsOrigins := corsBuildAllowList(ctx, q, dashHostStr)

	distDir := strings.TrimSpace(os.Getenv("KINDLING_DASHBOARD_DIST"))
	if distDir == "" {
		distDir = "web/dashboard/dist"
	}

	protectedAPI := auth.Middleware(q, apiMux)
	var handler http.Handler
	if dashHostStr != "" {
		handler = hostBasedHandler(corsMiddleware(corsOrigins, protectedAPI), dashboardSPAHandler(distDir), dashHostStr)
	} else {
		handler = corsMiddleware(corsOrigins, protectedAPI)
	}

	srv := &http.Server{Addr: listenAddr, Handler: handler}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		srv.Close()
	}()

	slog.Info("listening", "addr", listenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}

	return nil
}

// controlPlaneEdgeHostsFromDB returns unique hostnames (API + dashboard) and the
// loopback origin when at least one HTTPS control-plane hostname is configured.
func controlPlaneEdgeHostsFromDB(ctx context.Context, q *queries.Queries, listenAddr string) ([]string, *url.URL, error) {
	apiHost, err := publicAPIHostnameFromDB(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	dashHost, err := dashboardHostnameFromDB(ctx, q)
	if err != nil {
		return nil, nil, err
	}

	apiURL, err := url.Parse(loopbackAPIOrigin(listenAddr))
	if err != nil {
		return nil, nil, err
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

func seedDashboardPublicHostIfUnset(ctx context.Context, q *queries.Queries, host string) error {
	return seedClusterSettingIfUnset(ctx, q, rpc.ClusterSettingKeyDashboardPublicHost, host)
}

func hostBasedHandler(api http.Handler, dash http.Handler, dashHost string) http.Handler {
	dashHost = strings.ToLower(strings.TrimSpace(dashHost))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, herr := net.SplitHostPort(r.Host)
		if herr != nil || host == "" {
			host = r.Host
		}
		host = strings.ToLower(host)
		if dashHost != "" && host == dashHost {
			dash.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}

func dashboardSPAHandler(distDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fi, err := os.Stat(distDir); err != nil || !fi.IsDir() {
			http.Error(w, "dashboard not built (missing "+distDir+")", http.StatusServiceUnavailable)
			return
		}
		root, err := filepath.Abs(distDir)
		if err != nil {
			http.Error(w, "dashboard path", http.StatusInternalServerError)
			return
		}
		rel := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")
		candidate := filepath.Join(root, rel)
		absFile, err := filepath.Abs(candidate)
		if err != nil {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		if absFile != root && !strings.HasPrefix(absFile, root+string(filepath.Separator)) {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		if fi, err := os.Stat(absFile); err == nil && !fi.IsDir() {
			http.ServeFile(w, r, absFile)
			return
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
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

// runIdleScaleDownLoop periodically marks eligible projects as scaled_to_zero
// so the deployment reconciler can drain instances. Only one process holds the
// advisory lock per cluster at a time.
func runIdleScaleDownLoop(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler, cfgMgr *config.Manager) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			idleSeconds := int64(300)
			if cfgMgr != nil && cfgMgr.Snapshot() != nil {
				idleSeconds = cfgMgr.Snapshot().ScaleToZeroIdleSeconds
			}
			runIdleScaleDownOnce(ctx, databaseURL, q, deploymentReconciler, idleSeconds)
		}
	}
}

func runIdleScaleDownOnce(ctx context.Context, databaseURL string, q *queries.Queries, deploymentReconciler *reconciler.Scheduler, idleSeconds int64) {
	leaderConn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		slog.Debug("idle scaler: db connect", "error", err)
		return
	}
	defer leaderConn.Close(context.Background())

	qLeader := queries.New(leaderConn)
	acquired, err := qLeader.TrySessionAdvisoryLock(ctx, "kindling_idle_scaler")
	if err != nil || !acquired {
		return
	}
	// Lock released when connection closes (defer).

	projects, err := q.ProjectsFindForIdleScaleDown(ctx, idleSeconds)
	if err != nil {
		slog.Warn("idle scaler: list projects", "error", err)
		return
	}
	for _, p := range projects {
		if err := q.ProjectMarkScaledToZero(ctx, p.ID); err != nil {
			slog.Warn("idle scaler: mark scaled", "project_id", p.ID, "error", err)
			continue
		}
		dep, err := q.DeploymentLatestRunningByProjectID(ctx, p.ID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("idle scaler: latest deployment", "project_id", p.ID, "error", err)
			}
			continue
		}
		deploymentReconciler.ScheduleNow(uuid.UUID(dep.ID.Bytes))
		slog.Info("idle scale-to-zero", "project_id", p.ID, "deployment_id", dep.ID)
	}
}

func seedClusterSettingsFromServeFlags(ctx context.Context, q *queries.Queries, opts serveOptions) error {
	if err := seedClusterSettingIfUnset(ctx, q, config.SettingEdgeHTTPSAddr, opts.edgeHTTPSAddr); err != nil {
		return err
	}
	if err := seedClusterSettingIfUnset(ctx, q, config.SettingEdgeHTTPAddr, opts.edgeHTTPAddr); err != nil {
		return err
	}
	if err := seedClusterSettingIfUnset(ctx, q, config.SettingACMEEmail, opts.acmeEmail); err != nil {
		return err
	}
	if opts.acmeStaging {
		if err := seedClusterSettingIfUnset(ctx, q, config.SettingACMEStaging, "true"); err != nil {
			return err
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
		return err
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
		return err
	}
	return q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
		Key:   rpc.ClusterSettingKeyPublicBaseURL,
		Value: fromFlag,
	})
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func mustParseCIDR(s string) netip.Prefix {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		panic(err)
	}
	return p
}

func loadServerID() uuid.UUID {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(home + "/.kindling/server-id")
	if err == nil {
		id, err := uuid.Parse(strings.TrimSpace(string(data)))
		if err == nil {
			slog.Info("loaded server ID", "server_id", id)
			return id
		}
	}

	// First boot — generate and try to persist.
	id := uuid.New()
	dataDir := home + "/.kindling"
	os.MkdirAll(dataDir, 0o755)
	if err := os.WriteFile(dataDir+"/server-id", []byte(id.String()), 0o644); err != nil {
		slog.Warn("could not persist server ID", "error", err)
	}
	slog.Info("generated server ID", "server_id", id)
	return id
}

func corsBuildAllowList(ctx context.Context, q *queries.Queries, dashHost string) []string {
	var out []string
	for _, o := range strings.Split(os.Getenv("KINDLING_CORS_ORIGINS"), ",") {
		if t := strings.TrimSpace(o); t != "" {
			out = append(out, t)
		}
	}
	if dashHost != "" {
		dh := strings.ToLower(strings.TrimSpace(dashHost))
		out = append(out, "https://"+dh, "http://"+dh)
	}
	if v, err := q.ClusterSettingGet(ctx, rpc.ClusterSettingKeyPublicBaseURL); err == nil {
		if u := rpc.NormalizePublicBaseURL(v); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func corsOriginAllowed(origin string, allow []string) bool {
	if origin == "" {
		return false
	}
	origin = strings.TrimRight(origin, "/")
	for _, a := range allow {
		if strings.EqualFold(origin, strings.TrimRight(strings.TrimSpace(a), "/")) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "localhost" || h == "127.0.0.1"
}

func corsMiddleware(allow []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && corsOriginAllowed(origin, allow) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
