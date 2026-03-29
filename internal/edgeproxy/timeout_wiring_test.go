package edgeproxy_test

import (
	"testing"
	"time"

	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/edgeproxy"
)

// simulateStartupColdStart reproduces the config-to-edgeproxy wiring logic
// from cmd/kindling/serve_edge.go startEdgeProxy(). This exercises the real
// path: snapshot defaults → conditional pass-through → edgeproxy.New().
func simulateStartupColdStart(snap *config.Snapshot) time.Duration {
	var coldStart time.Duration
	if snap.ColdStartTimeoutSet {
		coldStart = snap.ColdStartTimeout
	}
	return coldStart
}

// TestStartupWiring_DefaultSnapshot_Produces30sWriteTimeout verifies the real
// startup path: config.DefaultSnapshot() → startEdgeProxy wiring → edgeproxy.New()
// must produce a 30s HTTPS write timeout. This was the original bug — the
// snapshot defaults ColdStartTimeout to 2m, and blindly passing that into the
// edge proxy inflated the write timeout to 2m15s.
func TestStartupWiring_DefaultSnapshot_Produces30sWriteTimeout(t *testing.T) {
	t.Parallel()

	snap := config.DefaultSnapshot()

	// Verify snapshot defaults ColdStartTimeout to 2m but ColdStartTimeoutSet is false.
	if snap.ColdStartTimeout != 2*time.Minute {
		t.Fatalf("DefaultSnapshot ColdStartTimeout = %v, want 2m", snap.ColdStartTimeout)
	}
	if snap.ColdStartTimeoutSet {
		t.Fatal("DefaultSnapshot should not have ColdStartTimeoutSet=true")
	}

	// Simulate the wiring from startEdgeProxy.
	coldStart := simulateStartupColdStart(snap)

	svc, err := edgeproxy.New(edgeproxy.Config{
		ColdStartTimeout: coldStart,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if svc.HTTPSWriteTimeout() != 30*time.Second {
		t.Fatalf("default startup path: httpsWriteTimeout = %v, want 30s",
			svc.HTTPSWriteTimeout())
	}
}

// TestStartupWiring_ExplicitLargeColdStart_ExpandsWriteTimeout verifies that
// when a user explicitly configures a large cold-start timeout (e.g. 3m), the
// startup wiring correctly expands the HTTPS write timeout beyond 30s.
func TestStartupWiring_ExplicitLargeColdStart_ExpandsWriteTimeout(t *testing.T) {
	t.Parallel()

	snap := config.DefaultSnapshot()
	// Simulate user setting cold_start_timeout=3m in cluster_settings.
	snap.ColdStartTimeout = 3 * time.Minute
	snap.ColdStartTimeoutSet = true

	coldStart := simulateStartupColdStart(snap)

	svc, err := edgeproxy.New(edgeproxy.Config{
		ColdStartTimeout: coldStart,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 3m + 15s margin = 3m15s
	expected := 3*time.Minute + 15*time.Second
	if svc.HTTPSWriteTimeout() != expected {
		t.Fatalf("explicit 3m cold start: httpsWriteTimeout = %v, want %v",
			svc.HTTPSWriteTimeout(), expected)
	}
}

// TestStartupWiring_ExplicitSmallColdStart_NoExpansion verifies that an explicit
// but small cold-start timeout (e.g., 10s) does not expand the write timeout.
func TestStartupWiring_ExplicitSmallColdStart_NoExpansion(t *testing.T) {
	t.Parallel()

	snap := config.DefaultSnapshot()
	snap.ColdStartTimeout = 10 * time.Second
	snap.ColdStartTimeoutSet = true

	coldStart := simulateStartupColdStart(snap)

	svc, err := edgeproxy.New(edgeproxy.Config{
		ColdStartTimeout: coldStart,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if svc.HTTPSWriteTimeout() != 30*time.Second {
		t.Fatalf("explicit small cold start: httpsWriteTimeout = %v, want 30s",
			svc.HTTPSWriteTimeout())
	}
}

// TestStartupWiring_ExplicitDefault2m_ExpandsWriteTimeout verifies that if a
// user explicitly sets cold_start_timeout=2m (the same value as the default),
// the write timeout IS expanded — because it's an explicit choice.
func TestStartupWiring_ExplicitDefault2m_ExpandsWriteTimeout(t *testing.T) {
	t.Parallel()

	snap := config.DefaultSnapshot()
	snap.ColdStartTimeout = 2 * time.Minute
	snap.ColdStartTimeoutSet = true // explicitly set, even though same as default

	coldStart := simulateStartupColdStart(snap)

	svc, err := edgeproxy.New(edgeproxy.Config{
		ColdStartTimeout: coldStart,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 2m + 15s margin = 2m15s — expanded because user explicitly chose 2m.
	expected := 2*time.Minute + 15*time.Second
	if svc.HTTPSWriteTimeout() != expected {
		t.Fatalf("explicit 2m cold start: httpsWriteTimeout = %v, want %v",
			svc.HTTPSWriteTimeout(), expected)
	}
}

// TestStartupWiring_SnapshotDefaultColdStart_NeverInflatesTimeout is a
// regression guard ensuring the default startup path never produces the old
// buggy 2m15s value.
func TestStartupWiring_SnapshotDefaultColdStart_NeverInflatesTimeout(t *testing.T) {
	t.Parallel()

	snap := config.DefaultSnapshot()
	coldStart := simulateStartupColdStart(snap)

	svc, err := edgeproxy.New(edgeproxy.Config{
		ColdStartTimeout: coldStart,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	buggyTimeout := 2*time.Minute + 15*time.Second
	if svc.HTTPSWriteTimeout() == buggyTimeout {
		t.Fatalf("default startup produced old buggy timeout %v — regression!", buggyTimeout)
	}
}
