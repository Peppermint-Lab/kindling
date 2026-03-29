package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
)

func TestDashboardEventBrokerSubscribePublish(t *testing.T) {
	t.Parallel()
	b := NewDashboardEventBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, unsub := b.Subscribe(ctx, []string{TopicProjects})
	defer unsub()

	if b.SubscriberCount() != 1 {
		t.Fatalf("SubscriberCount = %d, want 1", b.SubscriberCount())
	}

	b.Publish(TopicDeployments)
	select {
	case <-ch:
		t.Fatal("unexpected event for unsubscribed topic")
	case <-time.After(50 * time.Millisecond):
	}

	b.Publish(TopicProjects)
	select {
	case ev := <-ch:
		if ev.Topic != TopicProjects {
			t.Fatalf("topic %q", ev.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	unsub()
	if b.SubscriberCount() != 0 {
		t.Fatalf("SubscriberCount = %d, want 0", b.SubscriberCount())
	}
}

func TestParseDashboardTopicsParam(t *testing.T) {
	t.Parallel()
	got := parseDashboardTopicsParam("projects , " + TopicProject(uuid.MustParse("c0000000-0000-4000-a000-000000000042")))
	if len(got) != 2 {
		t.Fatalf("len=%d %v", len(got), got)
	}
	if got[0] != TopicProjects || got[1] != "project:c0000000-0000-4000-a000-000000000042" {
		t.Fatalf("got %v", got)
	}
	if len(parseDashboardTopicsParam("bad topic")) != 0 {
		t.Fatalf("expected empty for invalid")
	}
}

func TestStreamDashboardEvents_Unauthorized(t *testing.T) {
	t.Parallel()
	api := NewAPI(nil, nil, NewDashboardEventBroker())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/events?topics=projects", nil)
	api.streamDashboardEvents(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", rr.Code)
	}
}

func TestStreamDashboardEvents_NoTopics(t *testing.T) {
	t.Parallel()
	api := NewAPI(nil, nil, NewDashboardEventBroker())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	p := auth.Principal{
		UserID:         uuid.MustParse("a0000000-0000-4000-a000-000000000001"),
		OrgID:          uuid.MustParse("c0000000-0000-4000-a000-000000000001"),
		OrganizationID: pgtype.UUID{Bytes: uuid.MustParse("c0000000-0000-4000-a000-000000000001"), Valid: true},
		OrgRole:        "admin",
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	api.streamDashboardEvents(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", rr.Code)
	}
}

func TestStreamDashboardEvents_NilBroker(t *testing.T) {
	t.Parallel()
	api := NewAPI(nil, nil, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/events?topics=projects", nil)
	p := auth.Principal{
		UserID:         uuid.MustParse("a0000000-0000-4000-a000-000000000001"),
		OrgID:          uuid.MustParse("c0000000-0000-4000-a000-000000000001"),
		OrganizationID: pgtype.UUID{Bytes: uuid.MustParse("c0000000-0000-4000-a000-000000000001"), Valid: true},
		OrgRole:        "admin",
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	api.streamDashboardEvents(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", rr.Code)
	}
}

type flushErrorOnlyRecorder struct {
	header http.Header
	mu     sync.Mutex
	buf    strings.Builder
}

func (r *flushErrorOnlyRecorder) Header() http.Header {
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}

func (r *flushErrorOnlyRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *flushErrorOnlyRecorder) WriteHeader(statusCode int) {
	r.Header().Set("X-Status-Code", http.StatusText(statusCode))
}

func (r *flushErrorOnlyRecorder) FlushError() error {
	return nil
}

func (r *flushErrorOnlyRecorder) Body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func TestStreamDashboardEvents_FlushErrorOnlyWriter(t *testing.T) {
	t.Parallel()

	api := NewAPI(nil, nil, NewDashboardEventBroker())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/events?topics=projects", nil).WithContext(ctx)
	p := auth.Principal{
		UserID:         uuid.MustParse("a0000000-0000-4000-a000-000000000001"),
		OrgID:          uuid.MustParse("c0000000-0000-4000-a000-000000000001"),
		OrganizationID: pgtype.UUID{Bytes: uuid.MustParse("c0000000-0000-4000-a000-000000000001"), Valid: true},
		OrgRole:        "admin",
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))

	rr := &flushErrorOnlyRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		api.streamDashboardEvents(rr, req)
	}()

	deadline := time.After(time.Second)
	for {
		if strings.Contains(rr.Body(), ": connected\n\n") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for initial SSE payload")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for handler shutdown")
	}

	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
	if body := rr.Body(); !strings.Contains(body, ": connected\n\n") {
		t.Fatalf("expected SSE prelude, got %q", body)
	}
	if strings.Contains(rr.Body(), "streaming_unsupported") {
		t.Fatalf("unexpected streaming_unsupported response: %q", rr.Body())
	}
}
