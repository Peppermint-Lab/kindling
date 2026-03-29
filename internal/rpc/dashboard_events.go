package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
)

const dashboardSSEKeepaliveInterval = 25 * time.Second // SSE keepalive ping interval

const (
	// Dashboard invalidate topics (coarse; clients refetch REST).
	TopicProjects = "projects"
	// TopicDeployments signals org-wide deployment lists may have changed.
	TopicDeployments = "deployments"
	// TopicCIJobs signals org-wide CI lists may have changed.
	TopicCIJobs  = "ci_jobs"
	TopicServers = "servers"
)

// TopicProject is the invalidate topic for a single project's metadata.
func TopicProject(id uuid.UUID) string {
	return "project:" + id.String()
}

// TopicProjectDeployments is the invalidate topic for a project's deployment list tab.
func TopicProjectDeployments(id uuid.UUID) string {
	return "project_deployments:" + id.String()
}

// TopicProjectCIJobs is the invalidate topic for a project's CI jobs tab.
func TopicProjectCIJobs(id uuid.UUID) string {
	return "project_ci_jobs:" + id.String()
}

// TopicCIJob is the invalidate topic for a single CI job detail page.
func TopicCIJob(id uuid.UUID) string {
	return "ci_job:" + id.String()
}

// DashboardInvalidateEvent is a tiny notification for dashboard refetch.
type DashboardInvalidateEvent struct {
	Topic string    `json:"topic"`
	At    time.Time `json:"at"`
}

// DashboardEventBroker fans out invalidation events to SSE subscribers in-process.
type DashboardEventBroker struct {
	mu     sync.Mutex
	subs   map[int64]*dashSub
	nextID int64
}

type dashSub struct {
	id     int64
	topics map[string]struct{}
	ch     chan DashboardInvalidateEvent
}

// NewDashboardEventBroker returns a broadcaster for dashboard SSE clients.
func NewDashboardEventBroker() *DashboardEventBroker {
	return &DashboardEventBroker{subs: make(map[int64]*dashSub)}
}

// Publish sends an event to all subscribers interested in topic.
func (b *DashboardEventBroker) Publish(topic string) {
	if b == nil || topic == "" {
		return
	}
	ev := DashboardInvalidateEvent{Topic: topic, At: time.Now().UTC()}
	b.mu.Lock()
	subs := make([]*dashSub, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, s := range subs {
		if _, ok := s.topics[topic]; !ok {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			// Drop under backpressure; invalidation is idempotent.
		}
	}
}

// PublishMany publishes several topics in order.
func (b *DashboardEventBroker) PublishMany(topics ...string) {
	for _, t := range topics {
		b.Publish(t)
	}
}

// SubscriberCount returns the number of active subscribers (for tests).
func (b *DashboardEventBroker) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Subscribe registers for topics until parent is cancelled, then unregisters and closes ch.
func (b *DashboardEventBroker) Subscribe(parent context.Context, topics []string) (<-chan DashboardInvalidateEvent, context.CancelFunc) {
	ch := make(chan DashboardInvalidateEvent, 64)
	want := make(map[string]struct{})
	for _, t := range topics {
		t = strings.TrimSpace(t)
		if t == "" || !validDashboardTopic(t) {
			continue
		}
		want[t] = struct{}{}
	}

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = &dashSub{id: id, topics: want, ch: ch}
	b.mu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
		close(ch)
		close(done)
	}()

	unsub := func() {
		cancel()
		<-done
	}
	return ch, unsub
}

func validDashboardTopic(s string) bool {
	if len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r == ':' || r == '_' || r == '-' || r == '.':
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			continue
		default:
			return false
		}
	}
	return true
}

func parseDashboardTopicsParam(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" && validDashboardTopic(p) {
			out = append(out, p)
		}
	}
	return out
}

func (a *API) streamDashboardEvents(w http.ResponseWriter, r *http.Request) {
	if a.dashboardEvents == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "events_unavailable", "dashboard events not configured")
		return
	}
	if _, ok := mustPrincipal(w, r); !ok {
		return
	}

	topics := parseDashboardTopicsParam(r.URL.Query().Get("topics"))
	if len(topics) == 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_topics", "topics query required (comma-separated list)")
		return
	}
	if len(topics) > 24 {
		writeAPIError(w, http.StatusBadRequest, "invalid_topics", "too many topics")
		return
	}

	flush := func() error {
		return http.NewResponseController(w).Flush()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := a.dashboardEvents.Subscribe(r.Context(), topics)
	defer unsub()

	ticker := time.NewTicker(dashboardSSEKeepaliveInterval)
	defer ticker.Stop()

	writeEvent := func(name string, payload any) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal dashboard event %s: %w", name, err)
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); err != nil {
			return fmt.Errorf("write dashboard event %s: %w", name, err)
		}
		return flush()
	}

	// Initial comment so proxies flush early.
	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	if err := flush(); err != nil {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			if err := flush(); err != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := writeEvent("invalidate", ev); err != nil {
				return
			}
		}
	}
}
