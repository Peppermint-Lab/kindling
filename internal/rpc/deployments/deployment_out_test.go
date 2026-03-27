package deployments

import (
	"net/netip"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestDecorateDeploymentOutWithUnavailableVolumeSetsBlockedReason(t *testing.T) {
	t.Parallel()

	out := DeploymentOut{}
	dep := queries.Deployment{}
	vol := queries.ProjectVolume{
		ID:        pgtype.UUID{Valid: true},
		ProjectID: pgtype.UUID{Valid: true},
		MountPath: "/data",
		SizeGb:    10,
		Status:    "unavailable",
		LastError: "pinned server server-a is dead",
	}

	decorateDeploymentOutWithVolume(&out, dep, &vol)

	if out.PersistentVolume == nil {
		t.Fatal("expected persistent volume")
	}
	if out.BlockedReason != "pinned server server-a is dead" {
		t.Fatalf("blocked_reason = %q", out.BlockedReason)
	}
}

func TestDecorateDeploymentOutWithRunningDeploymentKeepsBlockedReasonEmpty(t *testing.T) {
	t.Parallel()

	out := DeploymentOut{}
	dep := queries.Deployment{
		RunningAt: pgtype.Timestamptz{Valid: true},
	}
	vol := queries.ProjectVolume{
		ID:        pgtype.UUID{Valid: true},
		ProjectID: pgtype.UUID{Valid: true},
		MountPath: "/data",
		SizeGb:    10,
		Status:    "unavailable",
		LastError: "pinned server server-a is dead",
	}

	decorateDeploymentOutWithVolume(&out, dep, &vol)

	if out.PersistentVolume == nil {
		t.Fatal("expected persistent volume")
	}
	if out.BlockedReason != "" {
		t.Fatalf("blocked_reason = %q, want empty", out.BlockedReason)
	}
}

func TestBuildDeploymentReachabilityRuntimeOnly(t *testing.T) {
	t.Parallel()

	vm := &queries.Vm{
		IpAddress: netip.MustParseAddr("127.0.0.1"),
		Port:      pgtype.Int4{Int32: 32768, Valid: true},
	}

	got := BuildDeploymentReachabilityFromVMs([]*queries.Vm{vm}, nil)
	if got == nil {
		t.Fatal("expected reachability")
	}
	if got.RuntimeURL != "http://127.0.0.1:32768" {
		t.Fatalf("runtime_url = %q", got.RuntimeURL)
	}
	if got.PublicURL != "" {
		t.Fatalf("public_url = %q, want empty", got.PublicURL)
	}
	if got.Port == nil || *got.Port != 32768 {
		t.Fatalf("port = %#v", got.Port)
	}
}

func TestBuildDeploymentReachabilityWithMixedPublicEndpoints(t *testing.T) {
	t.Parallel()

	vm := &queries.Vm{
		IpAddress: netip.MustParseAddr("192.168.64.2"),
		Port:      pgtype.Int4{Int32: 3000, Valid: true},
	}
	domains := []queries.Domain{
		{
			DomainName:         "www.example.com",
			VerifiedAt:         pgtype.Timestamptz{Valid: true},
			RedirectStatusCode: pgtype.Int4{Int32: 302, Valid: true},
			RedirectTo:         pgtype.Text{String: "https://kindling.example.com", Valid: true},
		},
		{
			DomainName: "app.example.com",
			VerifiedAt: pgtype.Timestamptz{Valid: true},
		},
	}

	got := BuildDeploymentReachabilityFromVMs([]*queries.Vm{vm}, domains)
	if got == nil {
		t.Fatal("expected reachability")
	}
	if got.PublicURL != "https://app.example.com" {
		t.Fatalf("primary public_url = %q", got.PublicURL)
	}
	if got.Domain != "app.example.com" {
		t.Fatalf("primary domain = %q", got.Domain)
	}
	if got.ProxiesToDeployment == nil || !*got.ProxiesToDeployment {
		t.Fatalf("primary proxies_to_deployment = %#v", got.ProxiesToDeployment)
	}
	if len(got.PublicEndpoints) != 2 {
		t.Fatalf("public_endpoints len = %d", len(got.PublicEndpoints))
	}
	if got.PublicEndpoints[0].Domain != "app.example.com" {
		t.Fatalf("first endpoint domain = %q", got.PublicEndpoints[0].Domain)
	}
	if got.PublicEndpoints[1].RedirectTo != "https://kindling.example.com" {
		t.Fatalf("redirect_to = %q", got.PublicEndpoints[1].RedirectTo)
	}
	if got.PublicEndpoints[1].ProxiesToDeployment == nil || *got.PublicEndpoints[1].ProxiesToDeployment {
		t.Fatalf("redirect endpoint proxies_to_deployment = %#v", got.PublicEndpoints[1].ProxiesToDeployment)
	}
}

func TestBuildDeploymentReachabilityFormatsIPv6RuntimeURL(t *testing.T) {
	t.Parallel()

	vm := &queries.Vm{
		IpAddress: netip.MustParseAddr("2001:db8::1"),
		Port:      pgtype.Int4{Int32: 3000, Valid: true},
	}

	got := BuildDeploymentReachabilityFromVMs([]*queries.Vm{vm}, nil)
	if got == nil {
		t.Fatal("expected reachability")
	}
	if got.RuntimeURL != "http://[2001:db8::1]:3000" {
		t.Fatalf("runtime_url = %q", got.RuntimeURL)
	}
}

func TestBuildDeploymentReachabilityReturnsNilWhenEmpty(t *testing.T) {
	t.Parallel()

	if got := BuildDeploymentReachabilityFromVMs(nil, nil); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}
