package events

import (
	"context"
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

func TestIngestor_InsertsAndNotifies(t *testing.T) {
	store := NewMemStore()
	var notified []uuid.UUID
	ing := NewIngestor(store, func(id uuid.UUID) { notified = append(notified, id) })

	evs := []domain.Event{
		{SourcePlugin: "gitlab", EventType: "mr_opened", DedupKey: "mr:a/b:1:sha1", PayloadNorm: map[string]any{}},
		{SourcePlugin: "gitlab", EventType: "mr_opened", DedupKey: "mr:a/b:2:sha2", PayloadNorm: map[string]any{}},
		{SourcePlugin: "gitlab", EventType: "mr_opened", DedupKey: "mr:a/b:1:sha1", PayloadNorm: map[string]any{}}, // dup
	}
	n, err := ing.Ingest(context.Background(), evs)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("inserted = %d, want 2 (third is a dedup)", n)
	}
	if len(notified) != 2 {
		t.Errorf("notified = %d, want 2", len(notified))
	}
	pending, _ := store.ListPending(context.Background(), 10)
	if len(pending) != 2 {
		t.Errorf("pending = %d", len(pending))
	}
}

func TestIngestor_NilNotify(t *testing.T) {
	ing := NewIngestor(NewMemStore(), nil)
	n, err := ing.Ingest(context.Background(), []domain.Event{
		{SourcePlugin: "x", EventType: "t", DedupKey: "k", PayloadNorm: map[string]any{}},
	})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
