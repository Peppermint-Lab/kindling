package rpc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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

// sandboxWebsocketUpgrader returns a websocket.Upgrader configured to validate
// the Origin header against the API's trusted origin list.
func (a *API) sandboxWebsocketUpgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				// No Origin header (not a browser client) — allow.
				// Proxy auth is still required below.
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

func (a *API) sandboxIsLocalOwner(sb queries.Sandbox) bool {
	if !sb.ServerID.Valid {
		return true
	}
	return a.sandboxSvc != nil && a.sandboxSvc.ServerID != uuid.Nil && uuid.UUID(sb.ServerID.Bytes) == a.sandboxSvc.ServerID
}

func (a *API) sandboxOwnerOrigin(ctx context.Context, sb queries.Sandbox) (*url.URL, error) {
	if !sb.ServerID.Valid {
		return nil, fmt.Errorf("sandbox has no assigned worker")
	}
	server, err := a.q.ServerFindByID(ctx, sb.ServerID)
	if err != nil {
		return nil, fmt.Errorf("find sandbox server: %w", err)
	}
	settings, err := a.q.ServerSettingGet(ctx, sb.ServerID)
	if err != nil {
		return nil, fmt.Errorf("find sandbox server settings: %w", err)
	}
	host := strings.TrimSpace(server.InternalIp)
	if host == "" {
		return nil, fmt.Errorf("sandbox server internal ip is not configured")
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
		case "connection", "upgrade", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding":
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

func (a *API) proxySandboxHTTPRequest(w http.ResponseWriter, r *http.Request, sb queries.Sandbox) bool {
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
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), strings.NewReader(string(body)))
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

func (a *API) proxySandboxWebsocket(w http.ResponseWriter, r *http.Request, sb queries.Sandbox) bool {
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
	headers := make(http.Header)
	copySandboxProxyableHeaders(headers, r.Header)
	proxyReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, wsURL.String(), nil)
	copySandboxProxyableHeaders(proxyReq.Header, headers)
	a.addSandboxProxyHeaders(proxyReq)
	dialHeaders := proxyReq.Header.Clone()
	remoteConn, resp, err := websocket.DefaultDialer.DialContext(r.Context(), wsURL.String(), dialHeaders)
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
