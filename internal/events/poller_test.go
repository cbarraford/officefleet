package events

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// scriptedPollSource returns canned (events, cursor, err) per call.
type scriptedPollSource struct {
	mu      sync.Mutex
	calls   []string // cursors received
	results []pollResult
}

type pollResult struct {
	events []domain.Event
	cursor string
	err    error
}

func (s *scriptedPollSource) Poll(_ context.Context, cursor string) ([]domain.Event, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, cursor)
	i := len(s.calls) - 1
	if i >= len(s.results) {
		return nil, cursor, nil
	}
	r := s.results[i]
	return r.events, r.cursor, r.err
}

func (s *scriptedPollSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

type recordingIngest struct {
	mu     sync.Mutex
	events []domain.Event
	err    error
}

func (r *recordingIngest) ingest(_ context.Context, evs []domain.Event) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evs...)
	return len(evs), r.err
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal(msg)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestRunPoller_AdvancesCursorAndIngests(t *testing.T) {
	src := &scriptedPollSource{results: []pollResult{
		{events: []domain.Event{{SourcePlugin: "p", EventType: "t", DedupKey: "k1"}}, cursor: "c1"},
		{events: nil, cursor: "c1"},
	}}
	cursors := NewMemStore()
	ing := &recordingIngest{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go RunPoller(ctx, "p", src, 20*time.Millisecond, cursors, ing.ingest, func(string, ...any) {})

	waitFor(t, func() bool { return src.callCount() >= 2 }, "poller did not tick twice")
	cancel()

	if got, _ := cursors.Get(context.Background(), "p"); got != "c1" {
		t.Errorf("cursor = %q, want c1", got)
	}
	ing.mu.Lock()
	defer ing.mu.Unlock()
	if len(ing.events) != 1 {
		t.Errorf("ingested = %d, want 1", len(ing.events))
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.calls[0] != "" {
		t.Errorf("first poll cursor = %q, want empty", src.calls[0])
	}
	if src.calls[1] != "c1" {
		t.Errorf("second poll cursor = %q, want c1", src.calls[1])
	}
}

func TestRunPoller_PollErrorKeepsCursor(t *testing.T) {
	src := &scriptedPollSource{results: []pollResult{
		{err: fmt.Errorf("gitlab down")},
		{events: nil, cursor: "c1"},
	}}
	cursors := NewMemStore()
	_ = cursors.Set(context.Background(), "p", "c0")
	ing := &recordingIngest{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go RunPoller(ctx, "p", src, 20*time.Millisecond, cursors, ing.ingest, func(string, ...any) {})
	waitFor(t, func() bool { return src.callCount() >= 2 }, "poller did not retry after error")
	cancel()

	src.mu.Lock()
	if src.calls[1] != "c0" {
		t.Errorf("cursor after error = %q, want unchanged c0", src.calls[1])
	}
	src.mu.Unlock()
}

func TestRunPoller_IngestErrorKeepsCursor(t *testing.T) {
	src := &scriptedPollSource{results: []pollResult{
		{events: []domain.Event{{SourcePlugin: "p", EventType: "t", DedupKey: "k"}}, cursor: "c9"},
	}}
	cursors := NewMemStore()
	ing := &recordingIngest{err: fmt.Errorf("db down")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go RunPoller(ctx, "p", src, 20*time.Millisecond, cursors, ing.ingest, func(string, ...any) {})
	waitFor(t, func() bool { return src.callCount() >= 1 }, "poller never polled")
	time.Sleep(30 * time.Millisecond) // give the failed ingest a beat
	cancel()

	if got, _ := cursors.Get(context.Background(), "p"); got != "" {
		t.Errorf("cursor = %q, want unchanged empty (ingest failed)", got)
	}
}
