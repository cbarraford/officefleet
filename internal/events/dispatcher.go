package events

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

const (
	busCapacity      = 256
	defaultWorkers   = 4
	defaultRescan    = 30 * time.Second
	rescanBatchLimit = 100
)

// AssignmentLister supplies the assignments to match against;
// *repo.AssignmentRepo satisfies it.
type AssignmentLister interface {
	List(ctx context.Context) ([]*domain.Assignment, error)
}

// Invoker executes one matched assignment; *run.Invoker satisfies it.
type Invoker interface {
	Invoke(ctx context.Context, assignmentID uuid.UUID, triggerKind string, eventID *string, params map[string]any) (*domain.Run, error)
}

// Dispatcher matches pending events against event-subscription assignments
// and executes the matches. Events are processed one at a time; a bounded
// worker pool parallelizes the matched runs WITHIN an event. The bus channel
// is only a wakeup — the events table is the source of truth, and the rescan
// loop (startup + interval) provides crash recovery, channel-overflow
// catch-up, and replay pickup.
type Dispatcher struct {
	store          EventStore
	assignments    AssignmentLister
	invoker        Invoker
	workers        int
	rescanInterval time.Duration
	bus            chan uuid.UUID
	logf           func(format string, args ...any)
}

func NewDispatcher(store EventStore, assignments AssignmentLister, invoker Invoker, workers int, rescanInterval time.Duration) *Dispatcher {
	if workers <= 0 {
		workers = defaultWorkers
	}
	if rescanInterval <= 0 {
		rescanInterval = defaultRescan
	}
	return &Dispatcher{
		store: store, assignments: assignments, invoker: invoker,
		workers: workers, rescanInterval: rescanInterval,
		bus:  make(chan uuid.UUID, busCapacity),
		logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}
}

func (d *Dispatcher) WithLogger(logger *slog.Logger) *Dispatcher {
	if logger != nil {
		d.logf = func(format string, args ...any) {
			logger.Warn("dispatcher", "message", fmt.Sprintf(format, args...))
		}
	}
	return d
}

// Notify wakes the dispatcher for a newly ingested event. Never blocks: if
// the bus is full the rescan loop will deliver instead.
func (d *Dispatcher) Notify(id uuid.UUID) {
	select {
	case d.bus <- id:
	default:
	}
}

// Run blocks until ctx is done. It rescans immediately on startup (crash
// recovery before new ingestion), then serves bus nudges and interval rescans.
func (d *Dispatcher) Run(ctx context.Context) {
	d.rescan(ctx)
	ticker := time.NewTicker(d.rescanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-d.bus:
			d.dispatch(ctx, id)
		case <-ticker.C:
			d.rescan(ctx)
		}
	}
}

func (d *Dispatcher) rescan(ctx context.Context) {
	pending, err := d.store.ListPending(ctx, rescanBatchLimit)
	if err != nil {
		d.logf("dispatcher: rescan: %v", err)
		return
	}
	for _, ev := range pending {
		if ctx.Err() != nil {
			return
		}
		d.dispatch(ctx, ev.ID)
	}
}

// dispatch processes one event: match, run all matches through the worker
// pool, and mark dispatched only after every matched run was ATTEMPTED
// (success, failure, or skip all count). A crash before the mark leaves the
// event pending; redelivery is safe because per-assignment dedup downstream
// records skips instead of duplicating outputs.
func (d *Dispatcher) dispatch(ctx context.Context, id uuid.UUID) {
	ev, err := d.store.GetByID(ctx, id)
	if err != nil {
		d.logf("dispatcher: get event %s: %v", id, err)
		return
	}
	if ev.Status != domain.EventStatusPending {
		return
	}

	all, err := d.assignments.List(ctx)
	if err != nil {
		d.logf("dispatcher: list assignments: %v", err)
		return // event stays pending; rescan retries
	}

	sem := make(chan struct{}, d.workers)
	var wg sync.WaitGroup
	eventIDStr := ev.ID.String()
	// params is shared READ-ONLY by all matched-run goroutines; everything
	// downstream (pipeline, template render, output delivery) must not write
	// to it — outputs.renderParams builds its own enriched copy.
	params := buildEventParams(ev)
	matched := 0
	for _, a := range all {
		if !a.Enabled || a.Trigger.Kind != "event-subscription" || !Matches(a.Trigger.Filter, ev) {
			continue
		}
		matched++
		wg.Add(1)
		sem <- struct{}{}
		go func(a *domain.Assignment) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := d.invoker.Invoke(ctx, a.ID, "event-subscription", &eventIDStr, params); err != nil {
				d.logf("dispatcher: event %s assignment %s: %v", ev.ID, a.ID, err)
			}
		}(a)
	}
	wg.Wait()

	if err := d.store.MarkDispatched(ctx, ev.ID); err != nil {
		d.logf("dispatcher: mark dispatched %s: %v", ev.ID, err)
	}
	if matched == 0 {
		d.logf("dispatcher: event %s (%s/%s) matched no assignments", ev.ID, ev.SourcePlugin, ev.EventType)
	}
}

// buildEventParams merges payload_norm with reserved meta keys (meta wins).
func buildEventParams(ev *domain.Event) map[string]any {
	p := make(map[string]any, len(ev.PayloadNorm)+5)
	for k, v := range ev.PayloadNorm {
		p[k] = v
	}
	p["source"] = ev.SourcePlugin
	p["event_type"] = ev.EventType
	p["identity"] = ev.Identity
	p["dedup_key"] = ev.DedupKey
	p["event_id"] = ev.ID.String()
	return p
}
