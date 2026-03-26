package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/builder"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/deploy"
	"github.com/kindlingvm/kindling/internal/listener"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/rpc"
	crunrt "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/webhook"
	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	var (
		listenAddr    string
		databaseURL   string
		publicBaseURL string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the kindling server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if databaseURL == "" {
				databaseURL = os.Getenv("DATABASE_URL")
			}
			if databaseURL == "" {
				return fmt.Errorf("--database-url or DATABASE_URL is required")
			}
			if publicBaseURL == "" {
				publicBaseURL = os.Getenv("KINDLING_PUBLIC_URL")
			}
			return runServe(cmd.Context(), listenAddr, databaseURL, publicBaseURL)
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", ":8080", "API listen address")
	cmd.Flags().StringVar(&databaseURL, "database-url", "", "PostgreSQL connection string")
	cmd.Flags().StringVar(&publicBaseURL, "public-url", "", "Optional seed for cluster_settings.public_base_url when that row is missing (e.g. first boot). Also KINDLING_PUBLIC_URL.")

	return cmd
}

func runServe(ctx context.Context, listenAddr, databaseURL, publicBaseURL string) error {
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
	ghTok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))

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

	if err := seedPublicBaseURLIfUnset(ctx, q, publicBaseURL); err != nil {
		return fmt.Errorf("seed public base url: %w", err)
	}

	// Detect and create runtime.
	rt := crunrt.NewDetectedRuntime()
	defer rt.StopAll()
	slog.Info("runtime detected", "runtime", rt.Name())

	regURL := strings.TrimSpace(os.Getenv("KINDLING_REGISTRY_URL"))
	if regURL == "" {
		regURL = "kindling"
	}
	bldr := builder.New(builder.Config{
		RegistryURL:      regURL,
		GitHubToken:      ghTok,
		RegistryUsername: strings.TrimSpace(os.Getenv("KINDLING_REGISTRY_USERNAME")),
		RegistryPassword: strings.TrimSpace(os.Getenv("KINDLING_REGISTRY_PASSWORD")),
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

	// API server
	api := rpc.NewAPI(q, ghTok)
	webhookHandler := webhook.NewHandler(q)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	api.Register(mux)
	mux.Handle("POST /webhooks/github", webhookHandler)

	// CORS wrapper for Vite dev server
	handler := corsMiddleware(mux)

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

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
