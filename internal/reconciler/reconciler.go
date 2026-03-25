// Package reconciler provides a generic scheduler for declarative convergence loops.
//
// Each entity type (VM, build, deployment, server, domain) registers a ReconcileFunc.
// The scheduler dispatches work to a pool of workers. Failed reconciliations are
// retried after a backoff period. Successful reconciliations are re-checked after
// a longer interval.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ReconcileFunc is called to reconcile an entity by its ID.
// Returning an error schedules a retry.
type ReconcileFunc func(ctx context.Context, id uuid.UUID) error

// Scheduler manages reconciliation scheduling for a single entity type.
type Scheduler struct {
	name      string
	reconcile ReconcileFunc
	workers   int

	mu       sync.Mutex
	schedule map[uuid.UUID]time.Time
	running  map[uuid.UUID]struct{}
	work     chan uuid.UUID

	retryAfter   time.Duration
	defaultAfter time.Duration
}

// Config holds scheduler configuration.
type Config struct {
	// Name identifies this scheduler in logs and traces.
	Name string

	// Reconcile is the function called for each entity.
	Reconcile ReconcileFunc

	// Workers is the number of concurrent reconcile goroutines (default: 5).
	Workers int

	// RetryAfter is the delay before retrying a failed reconciliation (default: 5s).
	RetryAfter time.Duration

	// DefaultAfter is the delay before re-reconciling a successful entity (default: 1h).
	DefaultAfter time.Duration
}

// New creates a new reconciler scheduler.
func New(cfg Config) *Scheduler {
	if cfg.Workers <= 0 {
		cfg.Workers = 5
	}
	if cfg.RetryAfter <= 0 {
		cfg.RetryAfter = 5 * time.Second
	}
	if cfg.DefaultAfter <= 0 {
		cfg.DefaultAfter = 1 * time.Hour
	}
	return &Scheduler{
		name:         cfg.Name,
		reconcile:    cfg.Reconcile,
		workers:      cfg.Workers,
		schedule:     make(map[uuid.UUID]time.Time),
		running:      make(map[uuid.UUID]struct{}),
		work:         make(chan uuid.UUID, 100),
		retryAfter:   cfg.RetryAfter,
		defaultAfter: cfg.DefaultAfter,
	}
}

// Schedule queues an entity for reconciliation at the given time.
// If the entity is already scheduled, the earlier time wins.
func (s *Scheduler) Schedule(id uuid.UUID, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.schedule[id]; ok && !existing.IsZero() && existing.Before(at) {
		return // already scheduled earlier
	}
	s.schedule[id] = at
}

// ScheduleNow queues an entity for immediate reconciliation.
func (s *Scheduler) ScheduleNow(id uuid.UUID) {
	s.Schedule(id, time.Now())
}

// Start launches the scheduler tick loop and worker pool. Blocks until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	var wg sync.WaitGroup

	// Start workers
	for range s.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx)
		}()
	}

	// Tick loop: scan schedule and dispatch due items
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			close(s.work)
			wg.Wait()
			return
		case <-ticker.C:
			s.dispatchDue()
		}
	}
}

func (s *Scheduler) dispatchDue() {
	now := time.Now()
	var due []uuid.UUID

	s.mu.Lock()
	for id, at := range s.schedule {
		if at.IsZero() || at.After(now) {
			continue
		}
		if _, ok := s.running[id]; ok {
			continue
		}
		s.running[id] = struct{}{}
		s.schedule[id] = time.Time{} // clear schedule
		due = append(due, id)
	}
	s.mu.Unlock()

	for _, id := range due {
		s.work <- id
	}
}

func (s *Scheduler) worker(ctx context.Context) {
	logger := slog.With("reconciler", s.name)

	for id := range s.work {
		reconcileCtx := context.WithValue(ctx, contextKeyReconcileID{}, id)
		logger := logger.With("id", id)

		logger.InfoContext(reconcileCtx, "reconciling")

		err := s.reconcile(reconcileCtx, id)

		s.mu.Lock()
		delete(s.running, id)
		if err != nil {
			logger.ErrorContext(reconcileCtx, "reconcile failed, retrying", "error", err)
			s.schedule[id] = time.Now().Add(s.retryAfter)
		} else {
			logger.InfoContext(reconcileCtx, "reconcile done")
			// Only schedule default re-check if reconcile didn't already schedule something
			if s.schedule[id].IsZero() {
				s.schedule[id] = time.Now().Add(s.defaultAfter)
			}
		}
		s.mu.Unlock()
	}
}

type contextKeyReconcileID struct{}

// ReconcileID extracts the entity ID from a reconcile context.
func ReconcileID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(contextKeyReconcileID{}).(uuid.UUID)
	return id, ok
}

// String returns a descriptive string for the scheduler.
func (s *Scheduler) String() string {
	return fmt.Sprintf("reconciler(%s, workers=%d)", s.name, s.workers)
}
