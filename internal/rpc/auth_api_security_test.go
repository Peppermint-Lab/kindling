package rpc

import (
	"net/http/httptest"
	"testing"
)

// TestBootstrapClientIP_LoopbackPeerWithXFF_ReturnsForwardedIP verifies that
// when the actual peer is loopback and proxy headers are present, the
// X-Forwarded-For IP is returned as the effective client IP (not the loopback peer).
// This ensures edge-proxied external requests are identified by their real client IP.
// Fulfills: VAL-BOOTSTRAP-001 (loopback peers cannot proxy remote clients via XFF)
func TestBootstrapClientIP_LoopbackPeerWithXFF_ReturnsForwardedIP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		wantIP     string
	}{
		{
			name:       "IPv4 loopback peer with non-loopback XFF",
			remoteAddr: "127.0.0.1:1234",
			xff:        "203.0.113.5",
			wantIP:     "203.0.113.5",
		},
		{
			name:       "IPv4 loopback peer with loopback XFF",
			remoteAddr: "127.0.0.1:1234",
			xff:        "127.0.0.1",
			wantIP:     "127.0.0.1",
		},
		{
			name:       "IPv4 loopback peer with multi-hop XFF returns first IP",
			remoteAddr: "127.0.0.1:1234",
			xff:        "203.0.113.5, 10.0.0.1, 192.168.1.1",
			wantIP:     "203.0.113.5",
		},
		{
			name:       "IPv6 loopback peer with non-loopback XFF",
			remoteAddr: "[::1]:1234",
			xff:        "203.0.113.5",
			wantIP:     "203.0.113.5",
		},
		{
			name:       "IPv6 loopback peer with loopback XFF",
			remoteAddr: "[::1]:1234",
			xff:        "::1",
			wantIP:     "::1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.xff)

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

// TestBootstrapRequestAllowed_LoopbackPeerWithLoopbackXFF_Allowed verifies
// that when the peer is loopback and XFF also contains a loopback IP,
// bootstrap is allowed (the forwarded IP is itself loopback).
func TestBootstrapRequestAllowed_LoopbackPeerWithLoopbackXFF_Allowed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
	}{
		{"IPv4 loopback with loopback XFF", "127.0.0.1:1234", "127.0.0.1"},
		{"IPv6 loopback with IPv6 loopback XFF", "[::1]:1234", "::1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.xff)

			if !bootstrapRequestAllowed(req) {
				t.Fatal("expected loopback peer with loopback XFF to be allowed")
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

	// IPv6 loopback peer with XFF: proxy headers present, so the
	// forwarded IP (non-loopback) should be returned.
	req2 := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req2.RemoteAddr = "[::1]:1234"
	req2.Header.Set("X-Forwarded-For", "203.0.113.5")

	ip2, ok2 := bootstrapClientIP(req2)
	if !ok2 {
		t.Fatal("bootstrapClientIP returned ok=false for [::1] with XFF")
	}
	if ip2.String() != "203.0.113.5" {
		t.Fatalf("expected forwarded IP 203.0.113.5, got %s", ip2.String())
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

// TestBootstrapClientIP_MalformedXFF verifies that malformed X-Forwarded-For
// headers are handled safely (fail closed) when the peer is loopback.
// When proxy headers are present but XFF is malformed, bootstrapClientIP
// should fail closed (return ok=false).
func TestBootstrapClientIP_MalformedXFF(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		wantOK     bool
		wantLoop   bool // only checked if wantOK == true
	}{
		// Empty XFF with no other proxy headers → treated as direct loopback
		{"empty XFF no proxy headers", "127.0.0.1:1234", "", true, true},
		// Whitespace-only XFF: Header.Set with whitespace creates a header
		// entry but hasProxyHeaders sees a non-empty value, yet after trim
		// it's empty → fail closed
		{"whitespace-only XFF", "127.0.0.1:1234", "   ", false, false},
		// Non-IP XFF: proxy header present, but XFF not parseable → fail closed
		{"non-IP XFF", "127.0.0.1:1234", "not-an-ip-address", false, false},
		// Garbage XFF: first entry is not an IP → fail closed
		{"XFF with garbage", "127.0.0.1:1234", "abc, def, ghi", false, false},
		// Empty first entry: comma split yields empty string → fail closed
		{"XFF with empty first entry", "127.0.0.1:1234", ", 203.0.113.5", false, false},
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
			if ok != tc.wantOK {
				t.Fatalf("bootstrapClientIP ok=%v, want %v", ok, tc.wantOK)
			}
			if ok && tc.wantLoop && !ip.IsLoopback() {
				t.Fatalf("expected loopback IP, got %s", ip.String())
			}
		})
	}
}

// TestBootstrapClientIP_ProxyHeadersWithoutXFF verifies that when the peer is
// loopback and a proxy header other than XFF is present (e.g. X-Forwarded-Proto
// or Via) but XFF is absent, bootstrapClientIP fails closed.
func TestBootstrapClientIP_ProxyHeadersWithoutXFF(t *testing.T) {
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

			_, ok := bootstrapClientIP(req)
			if ok {
				t.Fatal("expected bootstrapClientIP to fail closed when proxy header present without XFF")
			}
		})
	}
}
