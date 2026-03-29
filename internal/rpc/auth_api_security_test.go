package rpc

import (
	"net/http/httptest"
	"testing"
)

// TestBootstrapClientIP_AlwaysReturnsPeerIP verifies that bootstrapClientIP
// ALWAYS returns the peer IP from RemoteAddr, never trusting X-Forwarded-For.
// XFF is completely ignored for bootstrap authorization.
func TestBootstrapClientIP_AlwaysReturnsPeerIP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string // may be empty
		wantIP     string
	}{
		{
			name:       "IPv4 loopback peer with non-loopback XFF returns peer IP",
			remoteAddr: "127.0.0.1:1234",
			xff:        "203.0.113.5",
			wantIP:     "127.0.0.1",
		},
		{
			name:       "IPv4 loopback peer with loopback XFF returns peer IP",
			remoteAddr: "127.0.0.1:1234",
			xff:        "127.0.0.1",
			wantIP:     "127.0.0.1",
		},
		{
			name:       "IPv4 loopback peer with multi-hop XFF returns peer IP",
			remoteAddr: "127.0.0.1:1234",
			xff:        "203.0.113.5, 10.0.0.1, 192.168.1.1",
			wantIP:     "127.0.0.1",
		},
		{
			name:       "IPv6 loopback peer with non-loopback XFF returns peer IP",
			remoteAddr: "[::1]:1234",
			xff:        "203.0.113.5",
			wantIP:     "::1",
		},
		{
			name:       "External peer with loopback XFF returns peer IP",
			remoteAddr: "203.0.113.20:443",
			xff:        "127.0.0.1",
			wantIP:     "203.0.113.20",
		},
		{
			name:       "External peer without XFF returns peer IP",
			remoteAddr: "203.0.113.20:443",
			xff:        "",
			wantIP:     "203.0.113.20",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}

			ip, ok := bootstrapClientIP(req)
			if !ok {
				t.Fatal("bootstrapClientIP returned ok=false, want true")
			}
			if ip.String() != tc.wantIP {
				t.Fatalf("bootstrapClientIP returned %s, want %s", ip.String(), tc.wantIP)
			}
		})
	}
}

// TestBootstrapClientIP_DirectLoopbackNoXFF verifies that a direct loopback
// request (no X-Forwarded-For) is correctly identified as loopback.
// Fulfills: VAL-BOOTSTRAP-003 (direct loopback bootstrap remains allowed)
func TestBootstrapClientIP_DirectLoopbackNoXFF(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
	}{
		{"IPv4 loopback", "127.0.0.1:1234"},
		{"IPv6 loopback", "[::1]:1234"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr

			ip, ok := bootstrapClientIP(req)
			if !ok {
				t.Fatal("bootstrapClientIP returned ok=false, want true")
			}
			if !ip.IsLoopback() {
				t.Fatalf("bootstrapClientIP returned %s which is not loopback", ip.String())
			}
		})
	}
}

// TestBootstrapRequestAllowed_LoopbackPeerWithNonLoopbackXFF_Rejected verifies
// that when the peer is loopback but XFF contains a non-loopback IP (indicating
// the request was edge-proxied from an external client), bootstrap is REJECTED.
// Fulfills: VAL-BOOTSTRAP-001 (loopback peers cannot proxy remote clients via XFF)
func TestBootstrapRequestAllowed_LoopbackPeerWithNonLoopbackXFF_Rejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
	}{
		{"IPv4 loopback with non-loopback XFF", "127.0.0.1:1234", "203.0.113.5"},
		{"IPv6 loopback with non-loopback XFF", "[::1]:1234", "203.0.113.5"},
		{"IPv4 loopback with multi-hop XFF starting non-loopback", "127.0.0.1:1234", "203.0.113.5, 10.0.0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.xff)

			if bootstrapRequestAllowed(req) {
				t.Fatal("expected edge-proxied request with non-loopback XFF to be rejected")
			}
		})
	}
}

// TestBootstrapRequestAllowed_ProxiedLoopbackAlwaysRequiresToken verifies that
// when the peer is loopback but proxy headers are present, bootstrap is ALWAYS
// rejected (token required), regardless of what X-Forwarded-For says — even if
// XFF claims the client is also loopback. XFF is never trusted for bootstrap.
func TestBootstrapRequestAllowed_ProxiedLoopbackAlwaysRequiresToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
	}{
		{"IPv4 loopback peer with loopback XFF", "127.0.0.1:1234", "127.0.0.1"},
		{"IPv6 loopback peer with IPv6 loopback XFF", "[::1]:1234", "::1"},
		{"IPv4 loopback peer with non-loopback XFF", "127.0.0.1:1234", "203.0.113.5"},
		{"IPv6 loopback peer with non-loopback XFF", "[::1]:1234", "203.0.113.5"},
		{"loopback peer with multi-hop XFF", "127.0.0.1:1234", "203.0.113.5, 10.0.0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.xff)

			if bootstrapRequestAllowed(req) {
				t.Fatal("expected proxied loopback request to be rejected (token required)")
			}
		})
	}
}

// TestBootstrapRequestAllowed_SpoofedXFFLoopbackThroughProxy verifies that
// an attacker sending X-Forwarded-For: 127.0.0.1 through the edge proxy
// is rejected without a token. This is the core XFF spoofing attack vector.
func TestBootstrapRequestAllowed_SpoofedXFFLoopbackThroughProxy(t *testing.T) {
	t.Parallel()

	// Simulates: external attacker → edge proxy → API (loopback peer)
	// with spoofed XFF: 127.0.0.1 header.
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		extraHdr   string
		extraVal   string
	}{
		{
			name:       "spoofed XFF 127.0.0.1 via edge proxy",
			remoteAddr: "127.0.0.1:1234",
			xff:        "127.0.0.1",
			extraHdr:   "X-Forwarded-Proto",
			extraVal:   "https",
		},
		{
			name:       "spoofed XFF ::1 via edge proxy",
			remoteAddr: "127.0.0.1:1234",
			xff:        "::1",
			extraHdr:   "X-Forwarded-Proto",
			extraVal:   "https",
		},
		{
			name:       "spoofed XFF 127.0.0.1 with Via header",
			remoteAddr: "[::1]:1234",
			xff:        "127.0.0.1",
			extraHdr:   "Via",
			extraVal:   "1.1 edge-proxy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.xff)
			if tc.extraHdr != "" {
				req.Header.Set(tc.extraHdr, tc.extraVal)
			}

			if bootstrapRequestAllowed(req) {
				t.Fatal("expected spoofed XFF loopback through proxy to be rejected")
			}
		})
	}
}

// TestBootstrapRequestAllowed_ProxiedLoopbackWithTokenAllowed verifies that
// proxied loopback requests ARE allowed when a valid bootstrap token is provided.
func TestBootstrapRequestAllowed_ProxiedLoopbackWithTokenAllowed(t *testing.T) {
	t.Setenv(bootstrapTokenEnv, "test-bootstrap-secret")

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		token      string
		wantAllow  bool
	}{
		{
			name:       "proxied loopback with correct token",
			remoteAddr: "127.0.0.1:1234",
			xff:        "203.0.113.5",
			token:      "test-bootstrap-secret",
			wantAllow:  true,
		},
		{
			name:       "proxied loopback with wrong token",
			remoteAddr: "127.0.0.1:1234",
			xff:        "203.0.113.5",
			token:      "wrong-token",
			wantAllow:  false,
		},
		{
			name:       "proxied loopback with spoofed XFF loopback and correct token",
			remoteAddr: "127.0.0.1:1234",
			xff:        "127.0.0.1",
			token:      "test-bootstrap-secret",
			wantAllow:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.xff)
			if tc.token != "" {
				req.Header.Set(bootstrapTokenHeader, tc.token)
			}

			got := bootstrapRequestAllowed(req)
			if got != tc.wantAllow {
				t.Fatalf("bootstrapRequestAllowed = %v, want %v", got, tc.wantAllow)
			}
		})
	}
}

// TestBootstrapRequestAllowed_ExternalPeerSpoofedXFFLoopback verifies that an
// external peer cannot gain loopback privileges by spoofing X-Forwarded-For.
// Fulfills: VAL-BOOTSTRAP-002 (external peers cannot self-authorize by spoofing XFF)
func TestBootstrapRequestAllowed_ExternalPeerSpoofedXFFLoopback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
	}{
		{"External peer spoofs IPv4 loopback XFF", "203.0.113.20:443", "127.0.0.1"},
		{"External peer spoofs IPv6 loopback XFF", "203.0.113.20:443", "::1"},
		{"External peer spoofs multi-hop XFF ending in loopback", "203.0.113.20:443", "127.0.0.1, 10.0.0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.xff)

			if bootstrapRequestAllowed(req) {
				t.Fatal("expected external peer with spoofed loopback XFF to be rejected")
			}
		})
	}
}

// TestBootstrapRequestAllowed_TokenBypass verifies that a valid bootstrap token
// allows bootstrap from any IP, and an invalid token is rejected.
// Fulfills: VAL-BOOTSTRAP-004 (valid bootstrap token bypasses IP restrictions)
func TestBootstrapRequestAllowed_TokenBypass(t *testing.T) {
	t.Setenv(bootstrapTokenEnv, "test-bootstrap-secret")

	cases := []struct {
		name       string
		remoteAddr string
		token      string
		wantAllow  bool
	}{
		{
			name:       "Remote peer with correct token",
			remoteAddr: "203.0.113.20:443",
			token:      "test-bootstrap-secret",
			wantAllow:  true,
		},
		{
			name:       "Remote peer with wrong token",
			remoteAddr: "203.0.113.20:443",
			token:      "wrong-token",
			wantAllow:  false,
		},
		{
			name:       "Remote peer with empty token",
			remoteAddr: "203.0.113.20:443",
			token:      "",
			wantAllow:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.token != "" {
				req.Header.Set(bootstrapTokenHeader, tc.token)
			}

			got := bootstrapRequestAllowed(req)
			if got != tc.wantAllow {
				t.Fatalf("bootstrapRequestAllowed = %v, want %v", got, tc.wantAllow)
			}
		})
	}
}

// TestBootstrapClientIP_IPv6Loopback verifies that IPv6 loopback (::1) is
// treated the same as IPv4 loopback (127.0.0.1) in bootstrapClientIP.
// XFF is never used — bootstrapClientIP always returns the peer IP.
// Fulfills: VAL-BOOTSTRAP-005 (IPv6 loopback treated same as IPv4 loopback)
func TestBootstrapClientIP_IPv6Loopback(t *testing.T) {
	t.Parallel()

	// Direct IPv6 loopback (no proxy headers)
	req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req.RemoteAddr = "[::1]:1234"

	ip, ok := bootstrapClientIP(req)
	if !ok {
		t.Fatal("bootstrapClientIP returned ok=false for [::1]")
	}
	if !ip.IsLoopback() {
		t.Fatalf("expected [::1] to be detected as loopback, got %s (IsLoopback=%v)", ip.String(), ip.IsLoopback())
	}

	// IPv6 loopback peer with XFF: bootstrapClientIP always returns the
	// peer IP (XFF is never trusted for bootstrap).
	req2 := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req2.RemoteAddr = "[::1]:1234"
	req2.Header.Set("X-Forwarded-For", "203.0.113.5")

	ip2, ok2 := bootstrapClientIP(req2)
	if !ok2 {
		t.Fatal("bootstrapClientIP returned ok=false for [::1] with XFF")
	}
	if ip2.String() != "::1" {
		t.Fatalf("expected peer IP ::1, got %s", ip2.String())
	}
}

// TestBootstrapClientIP_ExternalPeerXFFNeverGrantsLoopback verifies that for
// external peers, X-Forwarded-For is completely ignored — external peers are
// always treated as external regardless of XFF.
// Fulfills: VAL-BOOTSTRAP-002
func TestBootstrapClientIP_ExternalPeerXFFNeverGrantsLoopback(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req.RemoteAddr = "203.0.113.20:443"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")

	ip, ok := bootstrapClientIP(req)
	if !ok {
		t.Fatal("bootstrapClientIP returned ok=false for external peer")
	}
	if ip.IsLoopback() {
		t.Fatalf("external peer with spoofed XFF: 127.0.0.1 was treated as loopback, got IP %s", ip.String())
	}
}

// TestBootstrapClientIP_IgnoresXFF verifies that bootstrapClientIP never
// trusts X-Forwarded-For, always returning the peer IP from RemoteAddr.
// Malformed, empty, or valid XFF values are all irrelevant.
func TestBootstrapClientIP_IgnoresXFF(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
	}{
		{"empty XFF no proxy headers", "127.0.0.1:1234", ""},
		{"whitespace-only XFF", "127.0.0.1:1234", "   "},
		{"non-IP XFF", "127.0.0.1:1234", "not-an-ip-address"},
		{"XFF with garbage", "127.0.0.1:1234", "abc, def, ghi"},
		{"XFF with empty first entry", "127.0.0.1:1234", ", 203.0.113.5"},
		{"valid non-loopback XFF", "127.0.0.1:1234", "203.0.113.5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}

			ip, ok := bootstrapClientIP(req)
			if !ok {
				t.Fatal("bootstrapClientIP returned ok=false, want true (always returns peer IP)")
			}
			if !ip.IsLoopback() {
				t.Fatalf("expected loopback peer IP, got %s", ip.String())
			}
		})
	}
}

// TestBootstrapClientIP_ProxyHeadersDontAffectClientIP verifies that
// bootstrapClientIP always returns the peer IP regardless of proxy headers.
// Proxy header detection is only used by bootstrapRequestAllowed, not
// by bootstrapClientIP.
func TestBootstrapClientIP_ProxyHeadersDontAffectClientIP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		header string
		value  string
	}{
		{"X-Forwarded-Proto only", "X-Forwarded-Proto", "https"},
		{"Via only", "Via", "1.1 edge-proxy"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set(tc.header, tc.value)

			ip, ok := bootstrapClientIP(req)
			if !ok {
				t.Fatal("bootstrapClientIP returned ok=false, want true (always returns peer IP)")
			}
			if ip.String() != "127.0.0.1" {
				t.Fatalf("expected peer IP 127.0.0.1, got %s", ip.String())
			}
		})
	}
}

// TestBootstrapRequestAllowed_ProxyHeadersWithoutXFF verifies that when the
// peer is loopback and a proxy header other than XFF is present (e.g.
// X-Forwarded-Proto or Via), the request is rejected (token required).
func TestBootstrapRequestAllowed_ProxyHeadersWithoutXFF(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		header string
		value  string
	}{
		{"X-Forwarded-Proto only", "X-Forwarded-Proto", "https"},
		{"Via only", "Via", "1.1 edge-proxy"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set(tc.header, tc.value)

			if bootstrapRequestAllowed(req) {
				t.Fatal("expected proxied loopback request to be rejected (token required)")
			}
		})
	}
}
