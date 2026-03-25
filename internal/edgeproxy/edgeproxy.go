// Package edgeproxy provides the HTTP reverse proxy with automatic TLS
// via CertMagic. Routes are loaded from PostgreSQL and refreshed via
// WAL change notifications.
package edgeproxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
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
}

// Route represents routing information for a domain.
type Route struct {
	IP                 string
	Port               int32
	RedirectTo         string
	RedirectStatusCode int32
}

// Service is the edge proxy.
type Service struct {
	cfg         Config
	q           *queries.Queries
	httpServer  *http.Server
	httpsServer *http.Server
	certConfig  *certmagic.Config
	routes      map[string]Route
	mu          sync.RWMutex
	cancel      context.CancelFunc
}

// New creates a new edge proxy service.
func New(cfg Config) (*Service, error) {
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":80"
	}
	if cfg.HTTPSAddr == "" {
		cfg.HTTPSAddr = ":443"
	}

	q := queries.New(cfg.Pool)
	storage := NewPostgreSQLStorage(cfg.Pool)

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

	// On-demand TLS: only provision certs for verified domains.
	certConfig.OnDemand = &certmagic.OnDemandConfig{
		DecisionFunc: func(ctx context.Context, name string) error {
			_, err := q.DomainVerified(ctx, name)
			if err != nil {
				return fmt.Errorf("domain not authorized: %s", name)
			}
			return nil
		},
	}

	s := &Service{
		cfg:        cfg,
		q:          q,
		certConfig: certConfig,
		routes:     make(map[string]Route),
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
		WriteTimeout: 30 * time.Second,
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

	newRoutes := make(map[string]Route)
	for _, row := range rows {
		if row.RedirectTo.Valid {
			newRoutes[row.DomainName] = Route{
				RedirectTo:         row.RedirectTo.String,
				RedirectStatusCode: row.RedirectStatusCode.Int32,
			}
			continue
		}

		if !row.VmIp.IsValid() {
			continue
		}

		newRoutes[row.DomainName] = Route{
			IP:   row.VmIp.String(),
			Port: row.VmPort.Int32,
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

// serveHTTPS is the main proxy handler.
func (s *Service) serveHTTPS(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}

	s.mu.RLock()
	route, ok := s.routes[host]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "Service Not Found", http.StatusNotFound)
		return
	}

	// Handle redirects.
	if route.RedirectTo != "" {
		code := int(route.RedirectStatusCode)
		if code < 300 || code > 399 {
			code = http.StatusMovedPermanently
		}
		http.Redirect(w, r, route.RedirectTo, code)
		return
	}

	// Reverse proxy to VM.
	targetURL := fmt.Sprintf("http://%s:%d", route.IP, route.Port)
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
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("Server", "Kindling")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Warn("proxy error", "host", host, "target", targetURL, "error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}
