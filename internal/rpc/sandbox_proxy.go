package rpc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

const (
	sandboxProxyHeaderRequest   = "X-Kindling-Proxy-Request"
	sandboxProxyHeaderTimestamp = "X-Kindling-Proxy-Timestamp"
	sandboxProxyHeaderSignature = "X-Kindling-Proxy-Signature"
	sandboxProxyClockSkew       = 2 * time.Minute
)

// sandboxProxyWebsocketDialer enforces TCP + WebSocket handshake deadlines so a
// mis-routed worker address cannot leave dashboard clients stuck on "Connecting…".
var sandboxProxyWebsocketDialer = &websocket.Dialer{
	HandshakeTimeout: 45 * time.Second,
	Proxy:            http.ProxyFromEnvironment,
	NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		d.Timeout = 12 * time.Second
		return d.DialContext(ctx, network, addr)
	},
}

// sandboxWebsocketUpgrader validates Origin against trusted dashboard origins.
func (a *API) sandboxWebsocketUpgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			return auth.OriginMatchesAny(origin, auth.TrustedOrigins(r.Context(), a.q))
		},
	}
}

func (a *API) sandboxProxySecret() string {
	if a.cfg == nil || a.cfg.Snapshot() == nil {
		return ""
	}
	return strings.TrimSpace(a.cfg.Snapshot().InterServerProxySharedKey)
}

func sandboxProxySignature(secret, method, requestURI string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, strconv.FormatInt(ts, 10))
	_, _ = io.WriteString(mac, "\n")
	_, _ = io.WriteString(mac, strings.ToUpper(strings.TrimSpace(method)))
	_, _ = io.WriteString(mac, "\n")
	_, _ = io.WriteString(mac, strings.TrimSpace(requestURI))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *API) validateSandboxProxyRequest(r *http.Request) error {
	if strings.TrimSpace(r.Header.Get(sandboxProxyHeaderRequest)) == "" {
		return nil
	}
	secret := a.sandboxProxySecret()
	if secret == "" {
		return fmt.Errorf("proxy secret unavailable")
	}
	tsRaw := strings.TrimSpace(r.Header.Get(sandboxProxyHeaderTimestamp))
	sig := strings.TrimSpace(r.Header.Get(sandboxProxyHeaderSignature))
	if tsRaw == "" || sig == "" {
		return fmt.Errorf("proxy authentication headers missing")
	}
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid proxy timestamp")
	}
	if delta := time.Since(time.Unix(ts, 0)); delta > sandboxProxyClockSkew || delta < -sandboxProxyClockSkew {
		return fmt.Errorf("proxy timestamp out of range")
	}
	expected := sandboxProxySignature(secret, r.Method, r.URL.RequestURI(), ts)
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return fmt.Errorf("proxy signature mismatch")
	}
	return nil
}

func (a *API) requireValidSandboxProxyIfPresent(w http.ResponseWriter, r *http.Request) bool {
	if err := a.validateSandboxProxyRequest(r); err != nil {
		writeAPIError(w, http.StatusForbidden, "sandbox_proxy", err.Error())
		return false
	}
	return true
}

func (a *API) sandboxIsLocalOwner(sb queries.RemoteVm) bool {
	if !sb.ServerID.Valid {
		return true
	}
	return a.sandboxSvc != nil && a.sandboxSvc.Runtime != nil && a.sandboxSvc.ServerID != uuid.Nil && uuid.UUID(sb.ServerID.Bytes) == a.sandboxSvc.ServerID
}

func (a *API) sandboxOwnerOrigin(ctx context.Context, sb queries.RemoteVm) (*url.URL, error) {
	if !sb.ServerID.Valid {
		return nil, fmt.Errorf("remote VM has no assigned worker")
	}
	server, err := a.q.ServerFindByID(ctx, sb.ServerID)
	if err != nil {
		return nil, fmt.Errorf("find worker for remote VM: %w", err)
	}
	settings, err := a.q.ServerSettingGet(ctx, sb.ServerID)
	if err != nil {
		return nil, fmt.Errorf("find worker settings for remote VM: %w", err)
	}
	host, err := sandboxProxyDialHost(server, settings)
	if err != nil {
		return nil, err
	}
	port := settings.InternalApiPort
	if port <= 0 {
		port = 8080
	}
	return &url.URL{
		Scheme: "http",
		Host:   netJoinHostPort(host, int(port)),
	}, nil
}

// sandboxProxyDialHost picks the TCP address the control plane uses to reach a
// worker for sandbox proxying (shell, ssh-ws, exec, etc.).
//
// Workers often auto-register a RFC1918/interface IP (e.g. docker0) as
// servers.internal_ip while server_settings.advertise_host holds the
// Internet-reachable address used for runtime_url. The control plane is usually
// outside that private network, so we prefer advertise_host when internal_ip is
// private and advertise_host is set.
//
// Operators who genuinely route over a VPC should set KINDLING_INTERNAL_IP (or DB
// servers.internal_ip) to the address reachable from their API, or leave
// advertise_host empty so this fallback does not apply.
func sandboxProxyDialHost(server queries.Server, settings queries.ServerSetting) (string, error) {
	internal := strings.TrimSpace(server.InternalIp)
	advertise := strings.TrimSpace(settings.AdvertiseHost)

	if internal != "" {
		h, err := validateSandboxProxyHost(internal)
		if err == nil {
			if shouldPreferAdvertiseHostForProxy(internal) && advertise != "" {
				if adv, errAdv := validateSandboxProxyHost(advertise); errAdv == nil {
					return adv, nil
				}
			}
			return h, nil
		}
	}
	if advertise != "" {
		return validateSandboxProxyHost(advertise)
	}
	if internal == "" {
		return validateSandboxProxyHost("")
	}
	return validateSandboxProxyHost(internal)
}

func shouldPreferAdvertiseHostForProxy(internal string) bool {
	host := normalizeSandboxProxyHost(internal)
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return false
}

func validateSandboxProxyHost(raw string) (string, error) {
	host := normalizeSandboxProxyHost(raw)
	if host == "" {
		return "", fmt.Errorf("worker internal IP is not configured for this remote VM")
	}
	switch strings.ToLower(host) {
	case "localhost":
		return "", fmt.Errorf("worker internal IP %q is not usable for remote proxying", raw)
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return "", fmt.Errorf("worker internal IP %q is not usable for remote proxying", raw)
	}
	return host, nil
}

func normalizeSandboxProxyHost(raw string) string {
	host := strings.TrimSpace(raw)
	host = strings.Trim(host, "[]")
	host = strings.TrimSuffix(host, ".")
	return host
}

func netJoinHostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host + ":" + strconv.Itoa(port)
}

func (a *API) addSandboxProxyHeaders(req *http.Request) {
	secret := a.sandboxProxySecret()
	if secret == "" {
		return
	}
	ts := time.Now().Unix()
	req.Header.Set(sandboxProxyHeaderRequest, "1")
	req.Header.Set(sandboxProxyHeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(sandboxProxyHeaderSignature, sandboxProxySignature(secret, req.Method, req.URL.RequestURI(), ts))
}

func copySandboxProxyableHeaders(dst, src http.Header) {
	for k, values := range src {
		switch strings.ToLower(k) {
		case "connection", "upgrade", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding",
			"sec-websocket-key", "sec-websocket-version", "sec-websocket-protocol", "sec-websocket-extensions":
			continue
		default:
			for _, v := range values {
				dst.Add(k, v)
			}
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, values := range src {
		switch strings.ToLower(k) {
		case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
			continue
		default:
			for _, v := range values {
				dst.Add(k, v)
			}
		}
	}
}

func (a *API) proxySandboxHTTPRequest(w http.ResponseWriter, r *http.Request, sb queries.RemoteVm) bool {
	if a.sandboxIsLocalOwner(sb) {
		return false
	}
	origin, err := a.sandboxOwnerOrigin(r.Context(), sb)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "sandbox_proxy", err.Error())
		return true
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "sandbox_proxy", "read request body")
		return true
	}
	_ = r.Body.Close()
	targetURL := *origin
	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), bytes.NewReader(body))
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "sandbox_proxy", "build proxy request")
		return true
	}
	copySandboxProxyableHeaders(req.Header, r.Header)
	a.addSandboxProxyHeaders(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "sandbox_proxy", err.Error())
		return true
	}
	defer resp.Body.Close()
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return true
}

func (a *API) proxySandboxWebsocket(w http.ResponseWriter, r *http.Request, sb queries.RemoteVm) bool {
	if a.sandboxIsLocalOwner(sb) {
		return false
	}
	origin, err := a.sandboxOwnerOrigin(r.Context(), sb)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "sandbox_proxy", err.Error())
		return true
	}
	wsURL := &url.URL{
		Scheme:   "ws",
		Host:     origin.Host,
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
	}
	proxyReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, wsURL.String(), nil)
	copySandboxProxyableHeaders(proxyReq.Header, r.Header)
	a.addSandboxProxyHeaders(proxyReq)
	dialHeaders := proxyReq.Header.Clone()
	remoteConn, resp, err := sandboxProxyWebsocketDialer.DialContext(r.Context(), wsURL.String(), dialHeaders)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			msg, _ := io.ReadAll(resp.Body)
			writeAPIError(w, http.StatusBadGateway, "sandbox_proxy", strings.TrimSpace(string(msg)))
		} else {
			writeAPIError(w, http.StatusBadGateway, "sandbox_proxy", err.Error())
		}
		return true
	}
	defer remoteConn.Close()
	localConn, err := a.sandboxWebsocketUpgrader().Upgrade(w, r, nil)
	if err != nil {
		return true
	}
	defer localConn.Close()
	bridgeWebsocketPair(localConn, remoteConn)
	return true
}

func bridgeWebsocketPair(left, right *websocket.Conn) {
	done := make(chan struct{}, 2)
	pump := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			mt, payload, err := src.ReadMessage()
			if err != nil {
				_ = dst.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := dst.WriteMessage(mt, payload); err != nil {
				return
			}
		}
	}
	go pump(right, left)
	go pump(left, right)
	<-done
}
