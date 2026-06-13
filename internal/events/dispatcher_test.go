package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// fakeInvoker records Invoke calls; optional block channel to test bounding.
type fakeInvoker struct {
	mu      sync.Mutex
	calls   []invokeCall
	failFor map[uuid.UUID]bool
	block   chan struct{} // when non-nil, Invoke waits for a receive
	active  atomic.Int32
	maxSeen atomic.Int32
}

func TestDispatcherWithLoggerWritesStructuredJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	d := NewDispatcher(NewMemStore(), &fakeAssignments{}, &fakeInvoker{}, 1, time.Hour).WithLogger(logger)

	d.logf("dispatcher: rescan: %v", fmt.Errorf("db down"))

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("dispatcher log is not JSON: %v\n%s", err, buf.String())
	}
	if got["level"] != "WARN" {
		t.Fatalf("level = %v, want WARN", got["level"])
	}
	if got["msg"] != "dispatcher" {
		t.Fatalf("msg = %v, want dispatcher", got["msg"])
	}
	if got["message"] != "dispatcher: rescan: db down" {
		t.Fatalf("message = %v", got["message"])
	}
}

type invokeCall struct {
	assignmentID uuid.UUID
	triggerKind  string
	eventID      string
	params       map[string]any
}

func (f *fakeInvoker) Invoke(_ context.Context, assignmentID uuid.UUID, triggerKind string, eventID *string, params map[string]any) (*domain.Run, error) {
	cur := f.active.Add(1)
	for {
		max := f.maxSeen.Load()
		if cur <= max || f.maxSeen.CompareAndSwap(max, cur) {
			break
		}
	}
	defer f.active.Add(-1)
	if f.block != nil {
		<-f.block
	}
	id := ""
	if eventID != nil {
		id = *eventID
	}
	f.mu.Lock()
	f.calls = append(f.calls, invokeCall{assignmentID, triggerKind, id, params})
	f.mu.Unlock()
	if f.failFor != nil && f.failFor[assignmentID] {
		return nil, fmt.Errorf("invoke failed")
	}
	return &domain.Run{ID: uuid.New(), Status: domain.RunStatusSucceeded}, nil
}

type fakeAssignments struct{ list []*domain.Assignment }

func (f *fakeAssignments) List(_ context.Context) ([]*domain.Assignment, error) { return f.list, nil }

func subAssignment(filter map[string]any) *domain.Assignment {
	return &domain.Assignment{
		ID: uuid.New(), AgentID: uuid.New(), DutyID: uuid.New(), Enabled: true,
		Trigger: domain.TriggerConfig{Kind: "event-subscription", Filter: filter},
	}
}

func storedEvent(t *testing.T, store *MemStore) *domain.Event {
	t.Helper()
	ev := &domain.Event{
		SourcePlugin: "gitlab", EventType: "mr_opened",
		PayloadNorm: map[string]any{"project": "org/repo", "mr_iid": float64(7)},
		Identity:    "alice", DedupKey: "mr:org/repo:7:sha1",
	}
	if _, err := store.Insert(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestDispatcher_MatchInvokesAndMarks(t *testing.T) {
	store := NewMemStore()
	ev := storedEvent(t, store)
	matching := subAssignment(map[string]any{"source": "gitlab", "event_type": "mr_opened", "project": "org/repo"})
	nonMatching := subAssignment(map[string]any{"source": "gitlab", "event_type": "mr_merged"})
	cronAssignment := &domain.Assignment{ID: uuid.New(), Enabled: true, Trigger: domain.TriggerConfig{Kind: "cron", Schedule: "* * * * *"}}
	disabled := subAssignment(map[string]any{"source": "gitlab", "event_type": "mr_opened"})
	disabled.Enabled = false

	inv := &fakeInvoker{}
	d := NewDispatcher(store, &fakeAssignments{list: []*domain.Assignment{matching, nonMatching, cronAssignment, disabled}}, inv, 4, time.Hour)

	d.dispatch(context.Background(), ev.ID)

	if len(inv.calls) != 1 {
		t.Fatalf("invokes = %d, want 1", len(inv.calls))
	}
	call := inv.calls[0]
	if call.assignmentID != matching.ID {
		t.Errorf("invoked %s, want matching assignment", call.assignmentID)
	}
	if call.triggerKind != "event-subscription" {
		t.Errorf("trigger kind = %q", call.triggerKind)
	}
	if call.eventID != ev.ID.String() {
		t.Errorf("eventID = %q", call.eventID)
	}
	// Params = payload_norm + reserved meta keys.
	for _, key := range []string{"project", "mr_iid", "source", "event_type", "identity", "dedup_key", "event_id"} {
		if _, ok := call.params[key]; !ok {
			t.Errorf("params missing %q: %v", key, call.params)
		}
	}
	if call.params["dedup_key"] != "mr:org/repo:7:sha1" || call.params["source"] != "gitlab" {
		t.Errorf("meta params wrong: %v", call.params)
	}

	got, _ := store.GetByID(context.Background(), ev.ID)
	if got.Status != domain.EventStatusDispatched {
		t.Errorf("event status = %q, want dispatched", got.Status)
	}
	if got.DispatchedAt == nil {
		t.Error("DispatchedAt not set")
	}
}

func TestDispatcher_ZeroMatchesStillDispatched(t *testing.T) {
	store := NewMemStore()
	ev := storedEvent(t, store)
	inv := &fakeInvoker{}
	d := NewDispatcher(store, &fakeAssignments{}, inv, 4, time.Hour)

	d.dispatch(context.Background(), ev.ID)

	if len(inv.calls) != 0 {
		t.Errorf("invokes = %d, want 0", len(inv.calls))
	}
	got, _ := store.GetByID(context.Background(), ev.ID)
	if got.Status != domain.EventStatusDispatched {
		t.Errorf("status = %q, want dispatched (auditable no-op)", got.Status)
	}
}

func TestDispatcher_OneFailureDoesNotBlockOthersOrMark(t *testing.T) {
	store := NewMemStore()
	ev := storedEvent(t, store)
	a1 := subAssignment(map[string]any{"source": "gitlab", "event_type": "mr_opened"})
	a2 := subAssignment(map[string]any{"source": "gitlab", "event_type": "mr_opened"})
	inv := &fakeInvoker{failFor: map[uuid.UUID]bool{a1.ID: true}}
	d := NewDispatcher(store, &fakeAssignments{list: []*domain.Assignment{a1, a2}}, inv, 4, time.Hour)

	d.dispatch(context.Background(), ev.ID)

	if len(inv.calls) != 2 {
		t.Errorf("invokes = %d, want 2 (failure must not block sibling)", len(inv.calls))
	}
	got, _ := store.GetByID(context.Background(), ev.ID)
	if got.Status != domain.EventStatusDispatched {
		t.Errorf("status = %q, want dispatched (attempted = done)", got.Status)
	}
}

func TestDispatcher_SkipsNonPending(t *testing.T) {
	store := NewMemStore()
	ev := storedEvent(t, store)
	_ = store.MarkDispatched(context.Background(), ev.ID)
	inv := &fakeInvoker{}
	d := NewDispatcher(store, &fakeAssignments{list: []*domain.Assignment{
		subAssignment(map[string]any{"source": "gitlab", "event_type": "mr_opened"}),
	}}, inv, 4, time.Hour)

	d.dispatch(context.Background(), ev.ID)

	if len(inv.calls) != 0 {
		t.Errorf("invokes = %d, want 0 for non-pending event", len(inv.calls))
	}
}

func TestDispatcher_WorkerBound(t *testing.T) {
	store := NewMemStore()
	ev := storedEvent(t, store)
	var matched []*domain.Assignment
	for i := 0; i < 6; i++ {
		matched = append(matched, subAssignment(map[string]any{"source": "gitlab", "event_type": "mr_opened"}))
	}
	inv := &fakeInvoker{block: make(chan struct{})}
	d := NewDispatcher(store, &fakeAssignments{list: matched}, inv, 2, time.Hour)

	done := make(chan struct{})
	go func() {
		d.dispatch(context.Background(), ev.ID)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // let workers saturate
	for i := 0; i < 6; i++ {
		inv.block <- struct{}{}
	}
	<-done
	if max := inv.maxSeen.Load(); max > 2 {
		t.Errorf("max concurrent invokes = %d, want <= 2", max)
	}
	if len(inv.calls) != 6 {
		t.Errorf("total invokes = %d, want 6", len(inv.calls))
	}
}

func TestDispatcher_NotifyNonBlocking(t *testing.T) {
	d := NewDispatcher(NewMemStore(), &fakeAssignments{}, &fakeInvoker{}, 1, time.Hour)
	// Fill the bus far past capacity; Notify must never block.
	doneCh := make(chan struct{})
	go func() {
		for i := 0; i < busCapacity+50; i++ {
			d.Notify(uuid.New())
		}
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify blocked")
	}
}

func TestDispatcher_RunDeliversNotifiedAndRescansPending(t *testing.T) {
	store := NewMemStore()
	evA := storedEvent(t, store) // will be Notified
	evB := &domain.Event{SourcePlugin: "gitlab", EventType: "mr_opened",
		PayloadNorm: map[string]any{}, DedupKey: "mr:org/repo:8:sha8"}
	_, _ = store.Insert(context.Background(), evB) // pending, NOT notified -> rescan must find it
	inv := &fakeInvoker{}
	d := NewDispatcher(store, &fakeAssignments{list: []*domain.Assignment{
		subAssignment(map[string]any{"source": "gitlab", "event_type": "mr_opened"}),
	}}, inv, 2, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	d.Notify(evA.ID)

	deadline := time.After(3 * time.Second)
	for {
		a, _ := store.GetByID(context.Background(), evA.ID)
		b, _ := store.GetByID(context.Background(), evB.ID)
		if a.Status == domain.EventStatusDispatched && b.Status == domain.EventStatusDispatched {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("events not dispatched: a=%s b=%s", a.Status, b.Status)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
}
