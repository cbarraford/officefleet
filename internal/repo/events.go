package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EventRepo struct{ db *pgxpool.Pool }

func NewEventRepo(db *pgxpool.Pool) *EventRepo { return &EventRepo{db: db} }

// Insert stores an event. Returns false when an event with the same
// (source_plugin, dedup_key) already exists (ON CONFLICT DO NOTHING).
func (r *EventRepo) Insert(ctx context.Context, ev *domain.Event) (bool, error) {
	if ev.ID == uuid.Nil {
		ev.ID = uuid.New()
	}
	if ev.Status == "" {
		ev.Status = domain.EventStatusPending
	}
	raw := ev.PayloadRaw
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	normJSON, err := json.Marshal(ev.PayloadNorm)
	if err != nil {
		return false, fmt.Errorf("marshal payload_norm: %w", err)
	}
	tag, err := r.db.Exec(ctx,
		`INSERT INTO events (id, source_plugin, event_type, payload_raw, payload_norm, identity, dedup_key, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (source_plugin, dedup_key) DO NOTHING`,
		ev.ID, ev.SourcePlugin, ev.EventType, []byte(raw), normJSON, ev.Identity, ev.DedupKey, ev.Status)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *EventRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	row := r.db.QueryRow(ctx, eventSelect+" WHERE id=$1", id)
	return scanEvent(row)
}

// ListPending returns pending events oldest-first (dispatch order).
func (r *EventRepo) ListPending(ctx context.Context, limit int) ([]*domain.Event, error) {
	rows, err := r.db.Query(ctx, eventSelect+" WHERE status='pending' ORDER BY received_at ASC LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ListRecent returns events newest-first, optionally filtered by status ("" = all).
func (r *EventRepo) ListRecent(ctx context.Context, status string, limit int) ([]*domain.Event, error) {
	q := eventSelect
	args := []any{}
	if status != "" {
		q += " WHERE status=$1 ORDER BY received_at DESC LIMIT $2"
		args = append(args, status, limit)
	} else {
		q += " ORDER BY received_at DESC LIMIT $1"
		args = append(args, limit)
	}
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (r *EventRepo) MarkDispatched(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, "UPDATE events SET status='dispatched', dispatched_at=NOW() WHERE id=$1", id)
	return err
}

// MarkPending re-queues an event for dispatch (replay).
func (r *EventRepo) MarkPending(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, "UPDATE events SET status='pending', dispatched_at=NULL WHERE id=$1", id)
	return err
}

const eventSelect = "SELECT id, source_plugin, event_type, payload_raw, payload_norm, identity, dedup_key, status, received_at, dispatched_at FROM events"

func scanEvent(s scanner) (*domain.Event, error) {
	var ev domain.Event
	var rawJSON, normJSON []byte
	if err := s.Scan(&ev.ID, &ev.SourcePlugin, &ev.EventType, &rawJSON, &normJSON,
		&ev.Identity, &ev.DedupKey, &ev.Status, &ev.ReceivedAt, &ev.DispatchedAt); err != nil {
		return nil, fmt.Errorf("scan event: %w", err)
	}
	ev.PayloadRaw = json.RawMessage(rawJSON)
	_ = json.Unmarshal(normJSON, &ev.PayloadNorm)
	return &ev, nil
}
