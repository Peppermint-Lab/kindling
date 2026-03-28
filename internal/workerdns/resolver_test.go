package workerdns

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/miekg/dns"
)

func TestResolverAllowsProdWithinOrg(t *testing.T) {
	orgID := uuid.New()
	serviceID := uuid.New()
	projectID := uuid.New()
	deploymentID := uuid.New()
	store := &fakeStore{
		callerByIP: map[netip.Addr]callerIdentity{
			netip.MustParseAddr("10.0.0.2"): {
				OrganizationID: orgID,
				ProjectID:      projectID,
				EnvSlug:        "prod",
			},
		},
		candidates: []endpointCandidate{{
			ServiceID:      serviceID,
			ProjectID:      projectID,
			OrganizationID: orgID,
			ProjectName:    "Payments API",
		}},
		productionDeployments: map[uuid.UUID]uuid.UUID{serviceID: deploymentID},
		backendIPs: map[uuid.UUID][]netip.Addr{
			deploymentID: {netip.MustParseAddr("10.0.0.42")},
		},
	}

	resolver := NewResolverWithStore(store)
	got, err := resolver.Resolve(context.Background(), netip.MustParseAddr("10.0.0.2"), "web.api.payments-api.prod.default.kindling.internal.", dns.TypeA)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Handled || got.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected success, got %+v", got)
	}
	if len(got.Answers) != 1 {
		t.Fatalf("expected one answer, got %d", len(got.Answers))
	}
	rr, ok := got.Answers[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A record, got %T", got.Answers[0])
	}
	if rr.A.String() != "10.0.0.42" {
		t.Fatalf("expected backend IP, got %s", rr.A.String())
	}
}

func TestResolverRejectsCrossEnvironmentPreviewLookup(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	store := &fakeStore{
		callerByIP: map[netip.Addr]callerIdentity{
			netip.MustParseAddr("10.0.0.2"): {
				OrganizationID:        orgID,
				ProjectID:             projectID,
				EnvSlug:               "pr-42",
				PreviewEnvironmentID:  uuid.New(),
				HasPreviewEnvironment: true,
			},
		},
		candidates: []endpointCandidate{{
			ServiceID:      uuid.New(),
			ProjectID:      projectID,
			OrganizationID: orgID,
			ProjectName:    "Payments API",
		}},
	}

	resolver := NewResolverWithStore(store)
	got, err := resolver.Resolve(context.Background(), netip.MustParseAddr("10.0.0.2"), "web.api.payments-api.prod.default.kindling.internal.", dns.TypeA)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Handled || got.Rcode != dns.RcodeNameError {
		t.Fatalf("expected NXDOMAIN, got %+v", got)
	}
}

func TestResolverRestrictsPreviewToExactProjectPreviewEnvironment(t *testing.T) {
	orgID := uuid.New()
	callerPreviewID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	deploymentID := uuid.New()
	store := &fakeStore{
		callerByIP: map[netip.Addr]callerIdentity{
			netip.MustParseAddr("10.0.0.2"): {
				OrganizationID:        orgID,
				ProjectID:             projectID,
				EnvSlug:               "pr-42",
				PreviewEnvironmentID:  callerPreviewID,
				HasPreviewEnvironment: true,
			},
		},
		candidates: []endpointCandidate{{
			ServiceID:      serviceID,
			ProjectID:      projectID,
			OrganizationID: orgID,
			ProjectName:    "Payments API",
		}},
		previewEnvironments: map[uuid.UUID][]previewEnvironmentRef{
			projectID: {{ID: callerPreviewID}},
		},
		previewDeployments: map[previewDeploymentKey]uuid.UUID{
			{serviceID: serviceID, previewEnvironmentID: callerPreviewID}: deploymentID,
		},
		backendIPs: map[uuid.UUID][]netip.Addr{
			deploymentID: {netip.MustParseAddr("10.0.0.99")},
		},
	}

	resolver := NewResolverWithStore(store)
	got, err := resolver.Resolve(context.Background(), netip.MustParseAddr("10.0.0.2"), "web.api.payments-api.pr-42.default.kindling.internal.", dns.TypeA)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Rcode != dns.RcodeSuccess || len(got.Answers) != 1 {
		t.Fatalf("expected preview answer, got %+v", got)
	}
}

func TestServerForwardsPublicNames(t *testing.T) {
	upstreamCalled := false
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		upstreamCalled = true
		resp := new(dns.Msg)
		resp.SetReply(r)
		resp.Answer = []dns.RR{
			&dns.A{
				Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
				A:   net.ParseIP("127.0.0.9"),
			},
		}
		_ = w.WriteMsg(resp)
	})

	upstreamAddr, shutdown := startTestDNSServer(t, handler)
	defer shutdown()

	server := NewServer(Config{
		Addr:                "127.0.0.1:0",
		AllowedClientPrefix: netip.MustParsePrefix("127.0.0.0/8"),
		Upstreams:           []string{upstreamAddr},
	}, NewResolverWithStore(&fakeStore{}))

	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	writer := &fakeResponseWriter{remoteAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53000}}
	server.ServeDNS(writer, req)
	if writer.msg == nil {
		t.Fatal("expected forwarded response")
	}
	if writer.msg.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected success, got rcode=%d", writer.msg.Rcode)
	}
	if !upstreamCalled {
		t.Fatal("expected upstream to be called")
	}
}

type previewDeploymentKey struct {
	serviceID            uuid.UUID
	previewEnvironmentID uuid.UUID
}

type fakeStore struct {
	callerByIP            map[netip.Addr]callerIdentity
	candidates            []endpointCandidate
	previewEnvironments   map[uuid.UUID][]previewEnvironmentRef
	productionDeployments map[uuid.UUID]uuid.UUID
	previewDeployments    map[previewDeploymentKey]uuid.UUID
	backendIPs            map[uuid.UUID][]netip.Addr
}

func (f *fakeStore) LookupCallerByIP(_ context.Context, ip netip.Addr) (callerIdentity, bool, error) {
	caller, ok := f.callerByIP[ip]
	return caller, ok, nil
}

func (f *fakeStore) LookupEndpointCandidates(_ context.Context, _, _, _ string) ([]endpointCandidate, error) {
	return append([]endpointCandidate(nil), f.candidates...), nil
}

func (f *fakeStore) LookupPreviewEnvironmentsByProjectAndPR(_ context.Context, projectID uuid.UUID, _ int32) ([]previewEnvironmentRef, error) {
	return append([]previewEnvironmentRef(nil), f.previewEnvironments[projectID]...), nil
}

func (f *fakeStore) LookupLatestRunningProductionDeployment(_ context.Context, serviceID uuid.UUID) (uuid.UUID, bool, error) {
	deploymentID, ok := f.productionDeployments[serviceID]
	return deploymentID, ok, nil
}

func (f *fakeStore) LookupLatestRunningPreviewDeployment(_ context.Context, serviceID, previewEnvironmentID uuid.UUID) (uuid.UUID, bool, error) {
	deploymentID, ok := f.previewDeployments[previewDeploymentKey{serviceID: serviceID, previewEnvironmentID: previewEnvironmentID}]
	return deploymentID, ok, nil
}

func (f *fakeStore) LookupRunningBackendIPs(_ context.Context, deploymentID uuid.UUID) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), f.backendIPs[deploymentID]...), nil
}

type fakeResponseWriter struct {
	msg        *dns.Msg
	remoteAddr net.Addr
}

func (f *fakeResponseWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53}
}
func (f *fakeResponseWriter) RemoteAddr() net.Addr      { return f.remoteAddr }
func (f *fakeResponseWriter) WriteMsg(m *dns.Msg) error { f.msg = m; return nil }
func (f *fakeResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (f *fakeResponseWriter) Close() error              { return nil }
func (f *fakeResponseWriter) TsigStatus() error         { return nil }
func (f *fakeResponseWriter) TsigTimersOnly(bool)       {}
func (f *fakeResponseWriter) Hijack()                   {}

func startTestDNSServer(t *testing.T, handler dns.Handler) (string, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: handler}
	go func() {
		_ = srv.ActivateAndServe()
	}()
	return pc.LocalAddr().String(), func() {
		_ = srv.Shutdown()
		_ = pc.Close()
	}
}
