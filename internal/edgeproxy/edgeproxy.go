// Package edgeproxy provides the HTTP reverse proxy with automatic TLS
// via CertMagic. Routes are loaded from PostgreSQL and refreshed via
// WAL change notifications.
package edgeproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// Edge proxy duration constants.
const defaultColdStartTimeout = 2 * time.Minute   // default cold start wait for first-request wake
const defaultHTTPSWriteTimeout = 30 * time.Second // HTTPS server write timeout baseline
const coldStartMargin = 15 * time.Second          // extra margin added to HTTPS write timeout for cold starts
const httpServerReadTimeout = 30 * time.Second    // HTTP server read timeout
const httpServerWriteTimeout = 30 * time.Second   // HTTP server write timeout
const coldStartLookupPollInterval = 200 * time.Millisecond

// edgeProxyRetryKey marks a request so we only reload-and-retry once per client request.
type edgeProxyRetryKey struct{}

// Config holds edge proxy configuration.
type Config struct {
	// HTTPAddr is the HTTP listen address (default ":80").
	HTTPAddr string

	// HTTPSAddr is the HTTPS listen address (default ":443").
	HTTPSAddr string

	// ACMEEmail is the email for Let's Encrypt registration.
	ACMEEmail string

	// ACMEStaging uses Let's Encrypt staging environment.
	ACMEStaging bool

	// Pool is the shared database connection pool.
	Pool *pgxpool.Pool

	// RouteChangeNotify receives signals when routes may have changed.
	RouteChangeNotify <-chan struct{}

	// WakeDeployment optionally nudges the deployment reconciler immediately
	// when the edge requests a cold start.
	WakeDeployment func(uuid.UUID)

	// ColdStartTimeout is how long the edge waits for backends after waking a
	// scaled-to-zero deployment (default 2m). The HTTPS server write timeout is
	// sized from this.
	ColdStartTimeout time.Duration

	// ControlPlaneHosts are lowercase hostnames for the control plane: the API
	// (from public_base_url) and optional dashboard host. HTTPS on these hosts
	// is proxied to APIBackend (Kindling process on loopback).
	ControlPlaneHosts []string

	// APIBackend is the http://127.0.0.1:port origin for the control plane proxy.
	APIBackend *url.URL

	// ServerID identifies this kindling host for usage rollups (HTTP metrics).
	ServerID uuid.UUID
}

// Backend is one upstream target (IP:port) for a hostname.
type Backend struct {
	IP   string
	Port int32
}

// Route represents routing information for a domain.
type Route struct {
	ProjectID          pgtype.UUID
	DeploymentID       pgtype.UUID
	DeploymentKind     string
	Backends           []Backend
	RedirectTo         string
	RedirectStatusCode int32
}

func normalizeControlPlaneHosts(hosts []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

// Service is the edge proxy.
type Service struct {
	cfg               Config
	q                 *queries.Queries
	httpServer        *http.Server
	httpsServer       *http.Server
	certConfig        *certmagic.Config
	routes            map[string]Route
	mu                sync.RWMutex
	backendRR         atomic.Uint64
	cancel            context.CancelFunc
	coldStartTimeout  time.Duration
	httpsWriteTimeout time.Duration
	serverID          pgtype.UUID
}

func previewLookupShouldReturnGone(lookup queries.DomainEdgeLookupRow) bool {
	return lookup.PreviewClosedAt.Valid && strings.HasPrefix(lookup.DomainKind, "preview_")
}

// New creates a new edge proxy service.
func New(cfg Config) (*Service, error) {
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":80"
	}
	if cfg.HTTPSAddr == "" {
		cfg.HTTPSAddr = ":443"
	}
	cold := cfg.ColdStartTimeout
	if cold <= 0 {
		cold = defaultColdStartTimeout
	}
	httpsWT := defaultHTTPSWriteTimeout
	if cold+coldStartMargin > httpsWT {
		httpsWT = cold + coldStartMargin
	}

	q := queries.New(cfg.Pool)
	storage := NewPostgreSQLStorage(cfg.Pool)

	cpHosts := normalizeControlPlaneHosts(cfg.ControlPlaneHosts)

	// Configure CertMagic.
	certConfig := certmagic.NewDefault()
	certConfig.Storage = storage

	if cfg.ACMEEmail != "" {
		issuer := certmagic.NewACMEIssuer(certConfig, certmagic.ACMEIssuer{
			Email:                   cfg.ACMEEmail,
			Agreed:                  true,
			DisableHTTPChallenge:    true,
			DisableTLSALPNChallenge: false,
		})
		if cfg.ACMEStaging {
			issuer.CA = certmagic.LetsEncryptStagingCA
		}
		certConfig.Issuers = []certmagic.Issuer{issuer}
	}

	// On-demand TLS: verified app domains, plus control plane hostnames (API + dashboard).
	certConfig.OnDemand = &certmagic.OnDemandConfig{
		DecisionFunc: func(ctx context.Context, name string) error {
			for _, h := range cpHosts {
				if h != "" && strings.EqualFold(name, h) {
					return nil
				}
			}
			_, err := q.DomainVerified(ctx, name)
			if err != nil {
				return fmt.Errorf("domain not authorized: %s", name)
			}
			return nil
		},
	}

	cfg.ControlPlaneHosts = cpHosts
	sid := pgtype.UUID{Bytes: cfg.ServerID, Valid: cfg.ServerID != uuid.Nil}
	s := &Service{
		cfg:               cfg,
		q:                 q,
		certConfig:        certConfig,
		routes:            make(map[string]Route),
		coldStartTimeout:  cold,
		httpsWriteTimeout: httpsWT,
		serverID:          sid,
	}

	s.httpServer = &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      http.HandlerFunc(s.serveHTTP),
		ReadTimeout:  httpServerReadTimeout,
		WriteTimeout: httpServerWriteTimeout,
	}

	return s, nil
}

// Start begins serving HTTP and HTTPS traffic.
func (s *Service) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Initial route load.
	if err := s.loadRoutes(ctx); err != nil {
		slog.Warn("initial route load failed", "error", err)
	}

	// Route refresh loop.
	go s.refreshLoop(ctx)

	// HTTPS server.
	s.httpsServer = &http.Server{
		Addr:         s.cfg.HTTPSAddr,
		Handler:      http.HandlerFunc(s.serveHTTPS),
		ReadTimeout:  httpServerReadTimeout,
		WriteTimeout: s.httpsWriteTimeout,
		TLSConfig:    s.certConfig.TLSConfig(),
	}

	go func() {
		slog.Info("edge proxy HTTP", "addr", s.cfg.HTTPAddr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	go func() {
		slog.Info("edge proxy HTTPS", "addr", s.cfg.HTTPSAddr)
		if err := s.httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTPS server error", "error", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the edge proxy.
func (s *Service) Stop(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.httpServer != nil {
		s.httpServer.Shutdown(ctx)
	}
	if s.httpsServer != nil {
		s.httpsServer.Shutdown(ctx)
	}
	return nil
}

func (s *Service) loadRoutes(ctx context.Context) error {
	rows, err := s.q.RouteFindActive(ctx)
	if err != nil {
		return fmt.Errorf("load active routes: %w", err)
	}

	redirects := make(map[string]Route)
	// domain -> vm_id (hex string) -> struct{} for deduplication
	proxySeen := make(map[string]map[string]struct{})
	proxyBackends := make(map[string][]Backend)
	proxyProjectID := make(map[string]pgtype.UUID)
	proxyDeploymentID := make(map[string]pgtype.UUID)
	proxyDeploymentKind := make(map[string]string)

	for _, row := range rows {
		if row.RedirectTo.Valid {
			redirects[row.DomainName] = Route{
				ProjectID:          row.ProjectID,
				DeploymentID:       row.DeploymentID,
				DeploymentKind:     row.DeploymentKind,
				RedirectTo:         row.RedirectTo.String,
				RedirectStatusCode: row.RedirectStatusCode.Int32,
			}
			continue
		}
		if row.VmIp == nil || !row.VmIp.IsValid() {
			continue
		}
		vmKey := ""
		if row.VmID.Valid {
			vmKey = fmt.Sprintf("%x", row.VmID.Bytes)
		}
		if proxySeen[row.DomainName] == nil {
			proxySeen[row.DomainName] = make(map[string]struct{})
		}
		if vmKey != "" {
			if _, dup := proxySeen[row.DomainName][vmKey]; dup {
				continue
			}
			proxySeen[row.DomainName][vmKey] = struct{}{}
		}
		proxyBackends[row.DomainName] = append(proxyBackends[row.DomainName], Backend{
			IP:   row.VmIp.String(),
			Port: row.VmPort.Int32,
		})
		proxyProjectID[row.DomainName] = row.ProjectID
		proxyDeploymentID[row.DomainName] = row.DeploymentID
		if row.DeploymentKind != "" {
			proxyDeploymentKind[row.DomainName] = row.DeploymentKind
		}
	}

	newRoutes := make(map[string]Route)
	for domain, r := range redirects {
		newRoutes[domain] = r
	}
	for domain, bes := range proxyBackends {
		if _, hasRedir := newRoutes[domain]; hasRedir {
			continue
		}
		if len(bes) == 0 {
			continue
		}
		newRoutes[domain] = Route{
			Backends:       bes,
			ProjectID:      proxyProjectID[domain],
			DeploymentID:   proxyDeploymentID[domain],
			DeploymentKind: proxyDeploymentKind[domain],
		}
	}

	s.mu.Lock()
	s.routes = newRoutes
	s.mu.Unlock()

	slog.Debug("routes loaded", "count", len(newRoutes))
	return nil
}

func (s *Service) refreshLoop(ctx context.Context) {
	const debounce = 100 * time.Millisecond
	const fallback = 60 * time.Second

	fallbackTicker := time.NewTicker(fallback)
	defer fallbackTicker.Stop()

	var timer *time.Timer

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return

		case <-s.cfg.RouteChangeNotify:
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounce, func() {
				s.loadRoutes(ctx)
			})

		case <-fallbackTicker.C:
			s.loadRoutes(ctx)
		}
	}
}

// serveHTTP redirects all HTTP traffic to HTTPS.
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	target := "https://" + r.Host + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

func (s *Service) pickBackend(route Route) (Backend, bool) {
	n := len(route.Backends)
	if n == 0 {
		return Backend{}, false
	}
	i := (s.backendRR.Add(1) - 1) % uint64(n)
	return route.Backends[i], true
}

// serveHTTPS is the main proxy handler.
func (s *Service) serveHTTPS(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}

	if s.cfg.APIBackend != nil {
		for _, h := range s.cfg.ControlPlaneHosts {
			if h != "" && strings.EqualFold(host, h) {
				s.proxyControlPlane(w, r, host)
				return
			}
		}
	}

	s.mu.RLock()
	route, ok := s.routes[host]
	s.mu.RUnlock()

	if ok && route.RedirectTo != "" {
		s.serveRedirect(w, r, route)
		return
	}
	if ok && len(route.Backends) > 0 {
		s.reverseProxy(w, r, host, route)
		return
	}

	// Scale-to-zero cold path or unknown host: resolve from DB.
	lookup, err := s.q.DomainEdgeLookup(r.Context(), host)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "Service Not Found", http.StatusNotFound)
			return
		}
		slog.Error("domain edge lookup", "host", host, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if lookup.RedirectTo.Valid {
		code := int(lookup.RedirectStatusCode.Int32)
		if code < 300 || code > 399 {
			code = http.StatusMovedPermanently
		}
		http.Redirect(w, r, lookup.RedirectTo.String, code)
		return
	}

	if previewLookupShouldReturnGone(lookup) {
		http.Error(w, "Preview Environment Gone", http.StatusGone)
		return
	}

	if !lookup.DeploymentID.Valid {
		http.Error(w, "Service Not Found", http.StatusNotFound)
		return
	}

	if lookup.RunningBackendCount > 0 {
		if err := s.loadRoutes(r.Context()); err != nil {
			slog.Warn("route reload after race", "error", err)
		}
		s.mu.RLock()
		route, ok = s.routes[host]
		s.mu.RUnlock()
		if ok && len(route.Backends) > 0 {
			s.reverseProxy(w, r, host, route)
			return
		}
	}

	coldStartBegan := time.Now()
	slog.Info("edge cold start request admitted",
		"host", host,
		"project_id", uuid.UUID(lookup.ProjectID.Bytes),
		"deployment_id", uuid.UUID(lookup.DeploymentID.Bytes),
	)
	if err := s.requestColdStart(r.Context(), lookup); err != nil {
		slog.Error("cold start wake", "host", host, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	slog.Info("edge cold start wake scheduled",
		"host", host,
		"deployment_id", uuid.UUID(lookup.DeploymentID.Bytes),
		"duration_ms", time.Since(coldStartBegan).Milliseconds(),
	)

	if route, routeOK := s.waitForBackend(r.Context(), host); routeOK {
		slog.Info("edge route available after wake",
			"host", host,
			"deployment_id", uuid.UUID(lookup.DeploymentID.Bytes),
			"duration_ms", time.Since(coldStartBegan).Milliseconds(),
		)
		s.reverseProxy(w, r, host, route)
		return
	}
	if err := r.Context().Err(); err != nil {
		http.Error(w, "Request Timeout", http.StatusRequestTimeout)
		return
	}
	http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
}

func (s *Service) serveRedirect(w http.ResponseWriter, r *http.Request, route Route) {
	code := int(route.RedirectStatusCode)
	if code < 300 || code > 399 {
		code = http.StatusMovedPermanently
	}
	http.Redirect(w, r, route.RedirectTo, code)
}

func (s *Service) requestColdStart(ctx context.Context, lookup queries.DomainEdgeLookupRow) error {
	s.recordRequestActivity(lookup.DeploymentKind.String, lookup.ProjectID, lookup.DeploymentID)
	isPreview := lookup.DeploymentKind.Valid && lookup.DeploymentKind.String == "preview"
	if isPreview {
		if err := s.q.DeploymentPreviewClearScaledToZero(ctx, lookup.DeploymentID); err != nil {
			return fmt.Errorf("clear preview scaled_to_zero: %w", err)
		}
	} else {
		if err := s.q.ProjectClearScaledToZero(ctx, lookup.ProjectID); err != nil {
			return fmt.Errorf("clear scaled_to_zero: %w", err)
		}
	}
	if _, err := s.q.DeploymentRequestWake(ctx, lookup.DeploymentID); err != nil {
		return fmt.Errorf("request wake: %w", err)
	}
	if s.cfg.WakeDeployment != nil {
		s.cfg.WakeDeployment(uuid.UUID(lookup.DeploymentID.Bytes))
	}
	return nil
}

func (s *Service) waitForBackend(ctx context.Context, host string) (Route, bool) {
	deadline := time.Now().Add(s.coldStartTimeout)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		route, ok := s.routes[host]
		s.mu.RUnlock()
		if ok && len(route.Backends) > 0 {
			return route, true
		}

		lookup, err := s.q.DomainEdgeLookup(ctx, host)
		if err == nil && lookup.RunningBackendCount > 0 {
			if loadErr := s.loadRoutes(ctx); loadErr != nil {
				slog.Warn("route reload during cold start", "host", host, "error", loadErr)
			}
			s.mu.RLock()
			route, ok = s.routes[host]
			s.mu.RUnlock()
			if ok && len(route.Backends) > 0 {
				return route, true
			}
		}

		wait := coldStartLookupPollInterval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait <= 0 {
			break
		}
		select {
		case <-ctx.Done():
			return Route{}, false
		case <-time.After(wait):
		}
	}
	return Route{}, false
}

// stripPort removes the port portion from an address (e.g. "1.2.3.4:5678" → "1.2.3.4").
// If no port is present the address is returned as-is.
func stripPort(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr // no port or already bare IP
	}
	return host
}

func (s *Service) proxyControlPlane(w http.ResponseWriter, r *http.Request, host string) {
	target := s.cfg.APIBackend
	clientIP := stripPort(r.RemoteAddr)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = r.Host
			// Edge-generated forwarding headers only — client-supplied headers
			// are already stripped by Rewrite (it removes Forwarded, X-Forwarded-*
			// from Out before calling this function).
			pr.Out.Header.Set("X-Forwarded-For", clientIP)
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
			pr.Out.Header.Set("X-Forwarded-Host", r.Host)
			pr.Out.Header.Set("X-Real-IP", clientIP)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("control plane proxy error", "host", host, "error", err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

func (s *Service) reverseProxy(w http.ResponseWriter, r *http.Request, host string, route Route) {
	be, ok := s.pickBackend(route)
	if !ok {
		http.Error(w, "Service Not Found", http.StatusNotFound)
		return
	}

	targetURL := fmt.Sprintf("http://%s:%d", be.IP, be.Port)
	target, err := url.Parse(targetURL)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var inBytes int64
	if r.Body != nil {
		r.Body = &countReadCloser{ReadCloser: r.Body, n: &inBytes}
	}
	mw := &meteredResponseWriter{ResponseWriter: w}

	clientIP := stripPort(r.RemoteAddr)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = r.Host
			// Edge-generated forwarding headers only — client-supplied headers
			// are already stripped by Rewrite (it removes Forwarded, X-Forwarded-*
			// from Out before calling this function).
			pr.Out.Header.Set("X-Forwarded-For", clientIP)
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
			pr.Out.Header.Set("X-Real-IP", clientIP)
		},
	}
	projID := route.ProjectID
	depID := route.DeploymentID
	statusCaptured := 0
	proxy.ModifyResponse = func(resp *http.Response) error {
		statusCaptured = resp.StatusCode
		resp.Header.Set("Server", "Kindling")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Warn("proxy error", "host", host, "target", targetURL, "error", err)
		if r.Context().Value(edgeProxyRetryKey{}) != nil {
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		}
		// Stale route after redeploy / scale: DB already points at new backends but the in-memory
		// map (or round-robin pick) may still target a dead port. Reload once and retry.
		if loadErr := s.loadRoutes(r.Context()); loadErr != nil {
			slog.Warn("reload routes after proxy error", "host", host, "error", loadErr)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		}
		s.mu.RLock()
		newRoute, ok := s.routes[host]
		s.mu.RUnlock()
		if !ok || len(newRoute.Backends) == 0 {
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		}
		ctx := context.WithValue(r.Context(), edgeProxyRetryKey{}, true)
		s.reverseProxy(w, r.WithContext(ctx), host, newRoute)
	}

	s.recordRequestActivity(route.DeploymentKind, projID, depID)
	proxy.ServeHTTP(mw, r)

	if statusCaptured == 0 {
		return // Retry path or abandoned request; inner reverseProxy records if applicable.
	}
	if !s.serverID.Valid || !projID.Valid || !depID.Valid {
		return
	}
	// context.Background() is intentional: this is a fire-and-forget goroutine that
	// records HTTP usage after the request completes; the request context may already be done.
	go s.recordAppHTTPUsage(context.Background(), projID, depID, statusCaptured, inBytes, mw.n)
}

func (s *Service) recordRequestActivity(deploymentKind string, projectID, deploymentID pgtype.UUID) {
	go func() {
		ctx := context.Background()
		if strings.EqualFold(deploymentKind, "preview") {
			if deploymentID.Valid {
				if err := s.q.DeploymentPreviewUpdateLastRequestAt(ctx, deploymentID); err != nil {
					slog.Warn("preview_last_request_at update", "error", err)
				}
			}
			return
		}
		if projectID.Valid {
			if err := s.q.ProjectUpdateLastRequestAt(ctx, projectID); err != nil {
				slog.Warn("last_request_at update", "error", err)
			}
		}
	}()
}

type meteredResponseWriter struct {
	http.ResponseWriter
	n int64
}

func (m *meteredResponseWriter) Write(b []byte) (int, error) {
	nn, err := m.ResponseWriter.Write(b)
	m.n += int64(nn)
	return nn, err
}

func (m *meteredResponseWriter) Unwrap() http.ResponseWriter {
	return m.ResponseWriter
}

func (m *meteredResponseWriter) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type countReadCloser struct {
	io.ReadCloser
	n *int64
}

func (c *countReadCloser) Read(p []byte) (int, error) {
	nn, err := c.ReadCloser.Read(p)
	*c.n += int64(nn)
	return nn, err
}

func (s *Service) recordAppHTTPUsage(
	ctx context.Context,
	projectID, deploymentID pgtype.UUID,
	statusCode int,
	bytesIn, bytesOut int64,
) {
	var n2, n4, n5 int64
	switch {
	case statusCode >= 200 && statusCode < 400:
		n2 = 1
	case statusCode >= 400 && statusCode < 500:
		n4 = 1
	default:
		n5 = 1
	}
	bucket := time.Now().UTC().Truncate(time.Minute)
	if err := s.q.ProjectHTTPUsageRollupIncrement(ctx, queries.ProjectHTTPUsageRollupIncrementParams{
		ServerID:     s.serverID,
		ProjectID:    projectID,
		DeploymentID: deploymentID,
		BucketStart:  pgtype.Timestamptz{Time: bucket, Valid: true},
		RequestCount: 1,
		Status2xx:    n2,
		Status4xx:    n4,
		Status5xx:    n5,
		BytesIn:      bytesIn,
		BytesOut:     bytesOut,
	}); err != nil && ctx.Err() == nil {
		slog.Warn("ProjectHTTPUsageRollupIncrement", "error", err)
	}
}
