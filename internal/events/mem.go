package events

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// MemStore is an in-memory EventStore + cursor store for tests.
type MemStore struct {
	mu      sync.Mutex
	events  map[uuid.UUID]*domain.Event
	cursors map[string]string
	seq     int // insertion order for stable ListPending sorting
	order   map[uuid.UUID]int
}

func NewMemStore() *MemStore {
	return &MemStore{
		events:  map[uuid.UUID]*domain.Event{},
		cursors: map[string]string{},
		order:   map[uuid.UUID]int{},
	}
}

func (m *MemStore) Insert(_ context.Context, ev *domain.Event) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.events {
		if existing.SourcePlugin == ev.SourcePlugin && existing.DedupKey == ev.DedupKey {
			return false, nil
		}
	}
	if ev.ID == uuid.Nil {
		ev.ID = uuid.New()
	}
	if ev.Status == "" {
		ev.Status = domain.EventStatusPending
	}
	if ev.ReceivedAt.IsZero() {
		ev.ReceivedAt = time.Now()
	}
	cp := *ev
	m.events[ev.ID] = &cp
	m.order[ev.ID] = m.seq
	m.seq++
	return true, nil
}

func (m *MemStore) GetByID(_ context.Context, id uuid.UUID) (*domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev, ok := m.events[id]
	if !ok {
		return nil, fmt.Errorf("event %s not found", id)
	}
	cp := *ev
	return &cp, nil
}

func (m *MemStore) ListPending(_ context.Context, limit int) ([]*domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*domain.Event
	for _, ev := range m.events {
		if ev.Status == domain.EventStatusPending {
			cp := *ev
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return m.order[out[i].ID] < m.order[out[j].ID] })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) MarkDispatched(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ev, ok := m.events[id]; ok {
		ev.Status = domain.EventStatusDispatched
		now := time.Now()
		ev.DispatchedAt = &now
	}
	return nil
}

func (m *MemStore) MarkPending(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ev, ok := m.events[id]; ok {
		ev.Status = domain.EventStatusPending
		ev.DispatchedAt = nil
	}
	return nil
}

// Get / Set implement the poller's CursorStore.
func (m *MemStore) Get(_ context.Context, plugin string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursors[plugin], nil
}

func (m *MemStore) Set(_ context.Context, plugin, cursor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursors[plugin] = cursor
	return nil
}
