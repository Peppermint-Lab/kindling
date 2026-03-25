package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/kindlingvm/kindling/internal/builder"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/listener"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/vmm"
	"github.com/spf13/cobra"
	"github.com/google/uuid"
)

func serveCmd() *cobra.Command {
	var (
		listenAddr  string
		databaseURL string
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
			return runServe(cmd.Context(), listenAddr, databaseURL)
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", ":8080", "API listen address")
	cmd.Flags().StringVar(&databaseURL, "database-url", "", "PostgreSQL connection string")

	return cmd
}

func runServe(ctx context.Context, listenAddr, databaseURL string) error {
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
	serverID := uuid.New() // TODO: use real server ID from /data/server-id
	q := queries.New(db.Pool)

	vmmgr := vmm.NewManager(vmm.Defaults(), serverID, q)
	defer vmmgr.Stop()

	bldr := builder.New(builder.Config{
		RegistryURL: "ghcr.io",
	}, q, serverID)

	// Set up reconcilers
	deploymentReconciler := reconciler.New(reconciler.Config{
		Name: "deployment",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			slog.Info("reconciling deployment", "id", id)
			// TODO: implement deployment reconciler
			return nil
		},
	})

	buildReconciler := reconciler.New(reconciler.Config{
		Name:      "build",
		Reconcile: bldr.ReconcileBuild,
	})

	vmReconciler := reconciler.New(reconciler.Config{
		Name: "vm",
		Reconcile: vmmgr.ReconcileVM,
	})

	domainReconciler := reconciler.New(reconciler.Config{
		Name: "domain",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			slog.Info("reconciling domain", "id", id)
			// TODO: implement domain reconciler
			return nil
		},
	})

	serverReconciler := reconciler.New(reconciler.Config{
		Name: "server",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			slog.Info("reconciling server", "id", id)
			// TODO: implement server reconciler
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
		OnBuild: func(ctx context.Context, id uuid.UUID) {
			buildReconciler.ScheduleNow(id)
		},
		OnVM: func(ctx context.Context, id uuid.UUID) {
			vmReconciler.ScheduleNow(id)
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

	// API server (placeholder — serves health check)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{Addr: listenAddr, Handler: mux}

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
