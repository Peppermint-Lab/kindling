package rpc

import (
	"net/http/httptest"
	"testing"
)

// TestBootstrapClientIP_LoopbackPeerIgnoresXFF verifies that when the actual
// peer address is loopback (IPv4 or IPv6), X-Forwarded-For is completely
// ignored and the returned IP is the loopback peer itself.
// Fulfills: VAL-BOOTSTRAP-001 (loopback peers cannot proxy remote clients via XFF)
func TestBootstrapClientIP_LoopbackPeerIgnoresXFF(t *testing.T) {
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
			wantIP:     "127.0.0.1",
		},
		{
			name:       "IPv4 loopback peer with loopback XFF",
			remoteAddr: "127.0.0.1:1234",
			xff:        "127.0.0.1",
			wantIP:     "127.0.0.1",
		},
		{
			name:       "IPv4 loopback peer with multi-hop XFF",
			remoteAddr: "127.0.0.1:1234",
			xff:        "203.0.113.5, 10.0.0.1, 192.168.1.1",
			wantIP:     "127.0.0.1",
		},
		{
			name:       "IPv6 loopback peer with non-loopback XFF",
			remoteAddr: "[::1]:1234",
			xff:        "203.0.113.5",
			wantIP:     "::1",
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

// TestBootstrapRequestAllowed_LoopbackPeerWithXFF_StillAllowed verifies that
// when the peer is loopback and XFF is present (any value), XFF is ignored and
// the request is allowed (peer is treated as loopback).
// Fulfills: VAL-BOOTSTRAP-001 (XFF ignored for loopback peers)
func TestBootstrapRequestAllowed_LoopbackPeerWithXFF_StillAllowed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
	}{
		{"IPv4 loopback with non-loopback XFF", "127.0.0.1:1234", "203.0.113.5"},
		{"IPv4 loopback with loopback XFF", "127.0.0.1:1234", "127.0.0.1"},
		{"IPv6 loopback with non-loopback XFF", "[::1]:1234", "203.0.113.5"},
		{"IPv6 loopback with IPv6 loopback XFF", "[::1]:1234", "::1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.xff)

			if !bootstrapRequestAllowed(req) {
				t.Fatal("expected loopback peer to be allowed even with XFF present")
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

	// Direct IPv6 loopback
	req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req.RemoteAddr = "[::1]:1234"

	ip, ok := bootstrapClientIP(req)
	if !ok {
		t.Fatal("bootstrapClientIP returned ok=false for [::1]")
	}
	if !ip.IsLoopback() {
		t.Fatalf("expected [::1] to be detected as loopback, got %s (IsLoopback=%v)", ip.String(), ip.IsLoopback())
	}

	// IPv6 loopback peer with spoofed XFF should still return loopback
	req2 := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req2.RemoteAddr = "[::1]:1234"
	req2.Header.Set("X-Forwarded-For", "203.0.113.5")

	ip2, ok2 := bootstrapClientIP(req2)
	if !ok2 {
		t.Fatal("bootstrapClientIP returned ok=false for [::1] with XFF")
	}
	if !ip2.IsLoopback() {
		t.Fatalf("expected [::1] peer to be loopback even with XFF, got %s", ip2.String())
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
func TestBootstrapClientIP_MalformedXFF(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
	}{
		{"empty XFF value", "127.0.0.1:1234", ""},
		{"whitespace-only XFF", "127.0.0.1:1234", "   "},
		{"non-IP XFF", "127.0.0.1:1234", "not-an-ip-address"},
		{"XFF with garbage", "127.0.0.1:1234", "abc, def, ghi"},
		{"XFF with empty first entry", "127.0.0.1:1234", ", 203.0.113.5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}

			// With the fix: loopback peer always returns loopback IP,
			// XFF is ignored entirely. So even malformed XFF should be fine.
			ip, ok := bootstrapClientIP(req)
			if !ok {
				t.Fatal("bootstrapClientIP returned ok=false, want true (loopback peer should always succeed)")
			}
			if !ip.IsLoopback() {
				t.Fatalf("expected loopback peer to return loopback IP, got %s", ip.String())
			}
		})
	}
}
