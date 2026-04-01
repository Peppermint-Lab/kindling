package edgeproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestPickBackend_empty(t *testing.T) {
	var s Service
	_, ok := s.pickBackend(Route{})
	if ok {
		t.Fatal("expected no backend")
	}
}

func TestPickBackend_nonEmpty(t *testing.T) {
	var s Service
	r := Route{Backends: []Backend{{IP: "127.0.0.1", Port: 3000}}}
	be, ok := s.pickBackend(r)
	if !ok || be.Port != 3000 {
		t.Fatalf("backend %+v ok=%v", be, ok)
	}
}

func TestPreviewLookupShouldReturnGone(t *testing.T) {
	t.Parallel()

	if !previewLookupShouldReturnGone(queries.DomainEdgeLookupRow{
		DomainKind:      "preview_stable",
		PreviewClosedAt: pgtype.Timestamptz{Valid: true},
	}) {
		t.Fatal("expected closed preview lookup to return gone")
	}

	if previewLookupShouldReturnGone(queries.DomainEdgeLookupRow{
		DomainKind:      "production",
		PreviewClosedAt: pgtype.Timestamptz{Valid: true},
	}) {
		t.Fatal("production domain should not return gone")
	}

	if previewLookupShouldReturnGone(queries.DomainEdgeLookupRow{
		DomainKind: "preview_immutable",
	}) {
		t.Fatal("open preview domain should not return gone")
	}
}

func TestReverseProxy_RetriesHungBackendWithinTimeout(t *testing.T) {
	t.Parallel()

	var healthyHits atomic.Int32
	hungBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hungBackend.Close()

	healthyBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer healthyBackend.Close()

	hung := backendForURL(t, hungBackend.URL)
	healthy := backendForURL(t, healthyBackend.URL)
	host := "kindling.systems"
	reloadedRoute := Route{Backends: []Backend{hung, healthy}}
	svc := &Service{
		q:                           queries.New(fakeRouteDBTX{rows: routeRowsFor(host, reloadedRoute)}),
		routes:                      map[string]Route{host: reloadedRoute},
		backendResponseHeaderTimeout: 50 * time.Millisecond,
	}

	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc.reverseProxy(w, r, host, reloadedRoute)
	}))
	defer edge.Close()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	req, err := http.NewRequest(http.MethodGet, edge.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = host
	req.RemoteAddr = "203.0.113.10:4444"

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if healthyHits.Load() == 0 {
		t.Fatal("expected retry to reach the healthy backend")
	}
	if elapsed := time.Since(start); elapsed >= 400*time.Millisecond {
		t.Fatalf("elapsed = %v, want retry before client timeout", elapsed)
	}
}

func backendForURL(t *testing.T, raw string) Backend {
	t.Helper()

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse backend url: %v", err)
	}
	port, err := net.LookupPort("tcp", u.Port())
	if err != nil {
		t.Fatalf("lookup port: %v", err)
	}
	return Backend{IP: u.Hostname(), Port: int32(port)}
}

type fakeRouteDBTX struct {
	rows []fakeRouteRow
}

func (f fakeRouteDBTX) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f fakeRouteDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return &fakeRouteRows{rows: f.rows, idx: -1}, nil
}

func (f fakeRouteDBTX) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return fakeRouteQueryRow{}
}

type fakeRouteQueryRow struct{}

func (fakeRouteQueryRow) Scan(...any) error { return nil }

type fakeRouteRow struct {
	domainName         string
	projectID          pgtype.UUID
	deploymentID       pgtype.UUID
	redirectTo         pgtype.Text
	redirectStatusCode pgtype.Int4
	deploymentKind     string
	vmIP               *netip.Addr
	vmPort             pgtype.Int4
	serverID           pgtype.UUID
	vmID               pgtype.UUID
}

func routeRowsFor(host string, route Route) []fakeRouteRow {
	rows := make([]fakeRouteRow, 0, len(route.Backends))
	for _, backend := range route.Backends {
		ip := netip.MustParseAddr(backend.IP)
		rows = append(rows, fakeRouteRow{
			domainName:         host,
			projectID:          pgtype.UUID{},
			deploymentID:       pgtype.UUID{},
			redirectTo:         pgtype.Text{},
			redirectStatusCode: pgtype.Int4{},
			deploymentKind:     "production",
			vmIP:               &ip,
			vmPort:             pgtype.Int4{Int32: backend.Port, Valid: true},
			serverID:           pgtype.UUID{},
			vmID:               pgtype.UUID{},
		})
	}
	return rows
}

type fakeRouteRows struct {
	rows []fakeRouteRow
	idx  int
}

func (f *fakeRouteRows) Close() {}

func (f *fakeRouteRows) Err() error { return nil }

func (f *fakeRouteRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }

func (f *fakeRouteRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (f *fakeRouteRows) Next() bool {
	if f.idx+1 >= len(f.rows) {
		return false
	}
	f.idx++
	return true
}

func (f *fakeRouteRows) Scan(dest ...any) error {
	row := f.rows[f.idx]
	values := []any{
		row.domainName,
		row.projectID,
		row.deploymentID,
		row.redirectTo,
		row.redirectStatusCode,
		row.deploymentKind,
		row.vmIP,
		row.vmPort,
		row.serverID,
		row.vmID,
	}
	for i, d := range dest {
		switch out := d.(type) {
		case *string:
			*out = values[i].(string)
		case *pgtype.UUID:
			*out = values[i].(pgtype.UUID)
		case *pgtype.Text:
			*out = values[i].(pgtype.Text)
		case *pgtype.Int4:
			*out = values[i].(pgtype.Int4)
		case **netip.Addr:
			*out = values[i].(*netip.Addr)
		default:
			return nil
		}
	}
	return nil
}

func (f *fakeRouteRows) Values() ([]any, error) { return nil, nil }

func (f *fakeRouteRows) RawValues() [][]byte { return nil }

func (f *fakeRouteRows) Conn() *pgx.Conn { return nil }
