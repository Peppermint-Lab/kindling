package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
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
