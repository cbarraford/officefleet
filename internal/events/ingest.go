package events

import (
	"context"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// EventStore is the durable event persistence the eventing core needs.
// *repo.EventRepo and MemStore satisfy it.
type EventStore interface {
	Insert(ctx context.Context, ev *domain.Event) (bool, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Event, error)
	ListPending(ctx context.Context, limit int) ([]*domain.Event, error)
	MarkDispatched(ctx context.Context, id uuid.UUID) error
}

// Ingestor persists plugin-produced events and nudges the dispatcher for
// each newly inserted one. Duplicate arrivals (same source_plugin+dedup_key)
// are silently collapsed by the store.
type Ingestor struct {
	store  EventStore
	notify func(uuid.UUID)
}

func NewIngestor(store EventStore, notify func(uuid.UUID)) *Ingestor {
	return &Ingestor{store: store, notify: notify}
}

// Ingest stores events, returning how many were newly inserted.
func (i *Ingestor) Ingest(ctx context.Context, evs []domain.Event) (int, error) {
	inserted := 0
	for idx := range evs {
		ev := &evs[idx]
		ev.Status = domain.EventStatusPending
		ok, err := i.store.Insert(ctx, ev)
		if err != nil {
			return inserted, err
		}
		if ok {
			inserted++
			if i.notify != nil {
				i.notify(ev.ID)
			}
		}
	}
	return inserted, nil
}
