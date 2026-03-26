// Package edgeproxy provides the HTTP reverse proxy with automatic TLS
// via CertMagic. Routes are loaded from PostgreSQL and refreshed via
// WAL change notifications.
package edgeproxy

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

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
}

// Backend is one upstream target (IP:port) for a hostname.
type Backend struct {
	IP   string
	Port int32
}

// Route represents routing information for a domain.
type Route struct {
	ProjectID          pgtype.UUID
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
		cold = 2 * time.Minute
	}
	httpsWT := 30 * time.Second
	if cold+15*time.Second > httpsWT {
		httpsWT = cold + 15*time.Second
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
	s := &Service{
		cfg:               cfg,
		q:                 q,
		certConfig:        certConfig,
		routes:            make(map[string]Route),
		coldStartTimeout:  cold,
		httpsWriteTimeout: httpsWT,
	}

	s.httpServer = &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      http.HandlerFunc(s.serveHTTP),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
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
		ReadTimeout:  30 * time.Second,
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
		return err
	}

	redirects := make(map[string]Route)
	// domain -> vm_id (hex string) -> struct{} for deduplication
	proxySeen := make(map[string]map[string]struct{})
	proxyBackends := make(map[string][]Backend)
	proxyProjectID := make(map[string]pgtype.UUID)

	for _, row := range rows {
		if row.RedirectTo.Valid {
			redirects[row.DomainName] = Route{
				ProjectID:          row.ProjectID,
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
		newRoutes[domain] = Route{Backends: bes, ProjectID: proxyProjectID[domain]}
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

	if err := s.requestColdStart(r.Context(), lookup); err != nil {
		slog.Error("cold start wake", "host", host, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	deadline := time.Now().Add(s.coldStartTimeout)
	for time.Now().Before(deadline) {
		if err := s.loadRoutes(r.Context()); err != nil {
			slog.Warn("route reload during cold start", "error", err)
		}
		s.mu.RLock()
		route, routeOK := s.routes[host]
		s.mu.RUnlock()
		if routeOK && len(route.Backends) > 0 {
			s.reverseProxy(w, r, host, route)
			return
		}
		select {
		case <-r.Context().Done():
			http.Error(w, "Request Timeout", http.StatusRequestTimeout)
			return
		case <-time.After(50 * time.Millisecond):
		}
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
	if err := s.q.ProjectClearScaledToZero(ctx, lookup.ProjectID); err != nil {
		return fmt.Errorf("clear scaled_to_zero: %w", err)
	}
	if _, err := s.q.DeploymentRequestWake(ctx, lookup.DeploymentID); err != nil {
		return fmt.Errorf("request wake: %w", err)
	}
	return nil
}

func (s *Service) proxyControlPlane(w http.ResponseWriter, r *http.Request, host string) {
	proxy := httputil.NewSingleHostReverseProxy(s.cfg.APIBackend)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = r.Host
		if req.Header.Get("X-Forwarded-For") == "" {
			req.Header.Set("X-Forwarded-For", r.RemoteAddr)
		}
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", r.Host)
		req.Header.Set("X-Real-IP", r.RemoteAddr)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Warn("control plane proxy error", "host", host, "error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
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

	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = r.Host
		req.Header.Set("X-Forwarded-For", r.RemoteAddr)
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Real-IP", r.RemoteAddr)
	}
	projID := route.ProjectID
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("Server", "Kindling")
		if projID.Valid && resp.StatusCode < 500 {
			if err := s.q.ProjectUpdateLastRequestAt(resp.Request.Context(), projID); err != nil {
				slog.Warn("last_request_at update", "error", err)
			}
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Warn("proxy error", "host", host, "target", targetURL, "error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}
