package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/ci"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/ratelimit"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/rpc"
	"github.com/kindlingvm/kindling/internal/sandbox"
	"github.com/kindlingvm/kindling/internal/webhook"
)

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

func requestHostIsLocal(r *http.Request) bool {
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	host = strings.ToLower(host)
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func corsOriginAllowed(r *http.Request, origin string, allow []string) bool {
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
	return requestHostIsLocal(r) && (h == "localhost" || h == "127.0.0.1" || h == "::1")
}

func corsMiddleware(allow []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && corsOriginAllowed(r, origin, allow) {
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

// startAPIServer creates and starts the API HTTP server. It blocks until the
// server shuts down.
func startAPIServer(
	ctx context.Context,
	q *queries.Queries,
	cfgMgr *config.Manager,
	dashboardEvents *rpc.DashboardEventBroker,
	deploymentReconciler *reconciler.Scheduler,
	ciJobReconciler *reconciler.Scheduler,
	sandboxReconciler *reconciler.Scheduler,
	sandboxTemplateReconciler *reconciler.Scheduler,
	sandboxService *sandbox.Service,
	ciJobCanceller interface {
		Cancel(context.Context, uuid.UUID) error
		CreateLocalWorkflowJob(context.Context, ci.CreateJobRequest) (queries.CiJob, error)
		HandleGitHubWorkflowJobEvent(context.Context, ci.GitHubWorkflowJobEvent) (ci.GitHubWorkflowJobHandleResult, error)
	},
	listenAddr string,
) error {
	api := rpc.NewAPI(q, cfgMgr, dashboardEvents)
	api.SetDeploymentReconciler(deploymentReconciler)
	api.SetCIJobRuntime(ciJobReconciler, ciJobCanceller)
	api.SetSandboxRuntime(sandboxReconciler, sandboxTemplateReconciler, sandboxService)
	webhookHandler := webhook.NewHandler(q, cfgMgr)
	webhookHandler.SetDeploymentReconciler(deploymentReconciler)
	webhookHandler.SetCIJobRuntime(ciJobReconciler, ciJobCanceller)

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
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

	// Rate limit auth endpoints: 10 requests per minute per IP.
	authLimiter := ratelimit.NewWithDefaults(10, 60*time.Second)
	defer authLimiter.Stop()
	rateLimitedTargets := map[string]bool{
		"POST /api/auth/login":     true,
		"POST /api/auth/bootstrap": true,
	}

	protectedAPI := auth.Middleware(q, apiMux)
	rateLimitedAPI := authLimiter.PathMiddleware(rateLimitedTargets, protectedAPI)
	// Enforce request body size limits to prevent OOM DoS via unbounded
	// JSON decoding. Applied before rate limiting so oversized payloads
	// are rejected as early as possible.
	sizeLimitedAPI := bodyLimitMiddleware(maxJSONBodySize, rateLimitedAPI)
	var handler http.Handler
	if dashHostStr != "" {
		handler = hostBasedHandler(corsMiddleware(corsOrigins, sizeLimitedAPI), dashboardSPAHandler(distDir), dashHostStr)
	} else {
		handler = corsMiddleware(corsOrigins, sizeLimitedAPI)
	}

	srv := &http.Server{Addr: listenAddr, Handler: handler}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		srv.Close()
	}()

	slog.Info("listening", "addr", listenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}
