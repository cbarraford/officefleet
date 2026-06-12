# SP3 — Event Bus & Dispatcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make OfficeFleet promptless: events from plugin webhooks/polling persist to Postgres and dispatch matching `event-subscription` assignments through the existing run pipeline, hosted by a unified `fleet serve` daemon.

**Architecture:** A durable `events` table with `pending → dispatched` status gives at-least-once delivery; an in-process channel is only a wakeup, with a rescan loop as crash recovery. Additive `WebhookSource`/`PollSource` plugin interfaces feed an `Ingestor`; a `Dispatcher` matches `TriggerConfig.Filter` against events and executes matches via a new shared `run.Invoker` (extracted from the cron scheduler's inline logic). The GitLab plugin proves both ingestion surfaces with identical normalization.

**Tech Stack:** Go 1.26 stdlib (net/http with 1.22+ pattern routing, crypto/subtle). Existing: pgx, cobra, yaml.v3, uuid. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-10-sp3-event-bus-design.md`

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `internal/domain/types.go` | Modify | Add `Event`, `EventStatus` (additive) |
| `internal/db/migrations/004_events.sql` | Create | `events` + `poll_cursors` tables |
| `internal/plugin/plugin.go` | Modify | Add `WebhookSource`, `PollSource`, `AuthError` (additive) |
| `internal/repo/events.go` | Create | `EventRepo` (pgx; insert-with-dedup, status transitions, listings) |
| `internal/repo/cursors.go` | Create | `CursorRepo` (pgx; poll cursor get/set) |
| `internal/events/match.go` | Create | Pure filter matching |
| `internal/events/mem.go` | Create | In-memory `MemStore` (EventStore + cursors) for tests, mirroring `state.NewMemStore` precedent |
| `internal/events/ingest.go` | Create | `Ingestor`: persist + notify |
| `internal/events/dispatcher.go` | Create | `Dispatcher`: bus, matching, worker pool, mark-after-attempt, rescan |
| `internal/events/poller.go` | Create | `RunPoller`: interval loop, cursor persistence |
| `internal/run/pipeline.go` | Modify | `ExecuteRequest.EventID` stamped onto Runs (additive) |
| `internal/run/invoker.go` | Create | `Invoker`: load assignment/agent/duty → resolve backend → Execute (shared by cron + dispatcher) |
| `internal/plugins/gitlab/events.go` | Create | Webhook handler + poll + shared MR normalization |
| `internal/server/server.go` | Create | Webhook mux (`POST /webhooks/{plugin}`) + `/healthz` |
| `internal/config/config.go` | Modify | `ServeConfig` + event-subscription/serve validation |
| `cmd/fleet/main.go` | Modify | `serveCmd`, `eventsCmd` (list/replay), scheduler-loop extraction, deprecate `schedule` |
| `configs/fleet.yaml` | Modify | `serve` block, gitlab poll config, event-subscription sample assignment |
| `internal/run/event_integration_test.go` | Create | Full vertical: webhook POST → dispatch → Run (in-package for Pipeline access) |

Dependency order: Task 1 (types) → 2 (repos) ∥ 3 (matcher) ∥ 4 (pipeline EventID) → 5 (Invoker) → 6 (dispatcher/ingestor) → 7 (poller) → 8 (gitlab) → 9 (server) → 10 (config) → 11 (CLI + sample config) → 12 (integration) → 13 (verify/push).

Run all commands from the repo root. Commit directly to `master` (consented project convention). Do not push until Task 13.

---

### Task 1: Event domain, migration, plugin capability interfaces

**Files:**
- Modify: `internal/domain/types.go` (append)
- Create: `internal/db/migrations/004_events.sql`
- Modify: `internal/plugin/plugin.go` (append)
- Test: `internal/plugin/plugin_test.go` (create)

- [x] **Step 1: Append the Event envelope to `internal/domain/types.go`**

```go
// EventStatus is the dispatch lifecycle state of an Event.
type EventStatus string

const (
	EventStatusPending    EventStatus = "pending"
	EventStatusDispatched EventStatus = "dispatched"
)

// Event is the normalized envelope for one inbound occurrence from a plugin
// event source. Persisted for durability and replay; dispatch is
// at-least-once (per-assignment dedup makes redelivery a recorded skip).
type Event struct {
	ID           uuid.UUID       `db:"id"`
	SourcePlugin string          `db:"source_plugin"` // e.g. "gitlab"
	EventType    string          `db:"event_type"`    // e.g. "mr_opened"
	PayloadRaw   json.RawMessage `db:"payload_raw"`   // verbatim from the source
	PayloadNorm  map[string]any  `db:"payload_norm"`  // plugin-normalized, template-friendly
	Identity     string          `db:"identity"`      // who triggered it (author/sender)
	DedupKey     string          `db:"dedup_key"`     // stable "already processed" key
	Status       EventStatus     `db:"status"`
	ReceivedAt   time.Time       `db:"received_at"`
	DispatchedAt *time.Time      `db:"dispatched_at"`
}
```

Add `"encoding/json"` to domain's imports.

- [x] **Step 2: Create `internal/db/migrations/004_events.sql`**

```sql
-- +migrate Up

CREATE TABLE IF NOT EXISTS events (
    id              UUID PRIMARY KEY,
    source_plugin   TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    payload_raw     JSONB NOT NULL DEFAULT '{}',
    payload_norm    JSONB NOT NULL DEFAULT '{}',
    identity        TEXT NOT NULL DEFAULT '',
    dedup_key       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    received_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispatched_at   TIMESTAMPTZ
);

-- Event-level dedup: the same occurrence arriving twice (webhook + poll
-- overlap, webhook retries) stores one row via ON CONFLICT DO NOTHING.
CREATE UNIQUE INDEX IF NOT EXISTS events_source_dedup_unique ON events(source_plugin, dedup_key);
CREATE INDEX IF NOT EXISTS events_status_idx ON events(status);
CREATE INDEX IF NOT EXISTS events_source_type_idx ON events(source_plugin, event_type);

CREATE TABLE IF NOT EXISTS poll_cursors (
    plugin      TEXT PRIMARY KEY,
    cursor      TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +migrate Down

DROP TABLE IF EXISTS poll_cursors;
DROP TABLE IF EXISTS events;
```

- [x] **Step 3: Append the capability interfaces to `internal/plugin/plugin.go`**

```go
// WebhookSource is implemented by plugins that accept push ingestion.
// HandleWebhook authenticates and parses one inbound HTTP request and returns
// normalized events. The PLATFORM persists them and writes the HTTP response:
// a returned *AuthError -> 401; any other error -> 400; storage failures -> 500.
type WebhookSource interface {
	HandleWebhook(ctx context.Context, r *http.Request) ([]domain.Event, error)
}

// AuthError marks a webhook authentication failure (-> 401).
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return e.Msg }

// PollSource is implemented by plugins that support interval polling.
// Poll returns events newer than cursor plus the new cursor. An empty cursor
// means "first poll"; the plugin decides its own cursor encoding. On partial
// failure (some sub-sources succeeded) a plugin returns the gathered events
// with the UNCHANGED cursor and a nil error; total failure returns an error.
type PollSource interface {
	Poll(ctx context.Context, cursor string) ([]domain.Event, string, error)
}
```

Add `"net/http"` and `"github.com/cbarraford/office-fleet/internal/domain"` to plugin.go's imports.

- [x] **Step 4: Write the test** — `internal/plugin/plugin_test.go`:

```go
package plugin

import (
	"errors"
	"testing"
)

func TestAuthError(t *testing.T) {
	var err error = &AuthError{Msg: "bad token"}
	if err.Error() != "bad token" {
		t.Errorf("Error() = %q", err.Error())
	}
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Error("errors.As failed to match *AuthError")
	}
}
```

- [x] **Step 5: Run and verify**

Run: `go build ./... && go test ./internal/plugin/ ./internal/domain/ ./internal/db/ -v`
Expected: build clean; new test passes; existing domain/db tests pass.

- [x] **Step 6: Commit**

```bash
git add internal/domain/types.go internal/db/migrations/004_events.sql internal/plugin/plugin.go internal/plugin/plugin_test.go
git commit -m "feat(sp3): event envelope, events migration, plugin ingestion interfaces"
```

---

### Task 2: EventRepo + CursorRepo

**Files:**
- Create: `internal/repo/events.go`
- Create: `internal/repo/cursors.go`

Repos follow the SP1 precedent: thin pgx SQL, no unit tests (logic is tested through fakes; SQL is exercised by `fleet migrate` + manual smoke). Match the style of `internal/repo/runs.go`.

- [x] **Step 1: Create `internal/repo/events.go`**

```go
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
```

(`scanner` is the existing package-level interface used by `scanRun` — check `internal/repo/` for its definition; reuse it.)

- [x] **Step 2: Create `internal/repo/cursors.go`**

```go
package repo

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CursorRepo struct{ db *pgxpool.Pool }

func NewCursorRepo(db *pgxpool.Pool) *CursorRepo { return &CursorRepo{db: db} }

// Get returns the stored cursor for a plugin, or "" when none exists.
func (r *CursorRepo) Get(ctx context.Context, plugin string) (string, error) {
	var cursor string
	err := r.db.QueryRow(ctx, "SELECT cursor FROM poll_cursors WHERE plugin=$1", plugin).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return cursor, err
}

func (r *CursorRepo) Set(ctx context.Context, plugin, cursor string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO poll_cursors (plugin, cursor, updated_at) VALUES ($1,$2,NOW())
		 ON CONFLICT (plugin) DO UPDATE SET cursor=EXCLUDED.cursor, updated_at=NOW()`,
		plugin, cursor)
	return err
}
```

- [x] **Step 3: Build, vet, commit**

Run: `go build ./... && go vet ./internal/repo/`
Expected: clean.

```bash
git add internal/repo/events.go internal/repo/cursors.go
git commit -m "feat(sp3): event and poll-cursor repositories"
```

---

### Task 3: Filter matcher

**Files:**
- Create: `internal/events/match.go`
- Test: `internal/events/match_test.go`

- [x] **Step 1: Write the failing tests** — `internal/events/match_test.go`:

```go
package events

import (
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
)

func ev(source, eventType string, norm map[string]any) *domain.Event {
	return &domain.Event{SourcePlugin: source, EventType: eventType, PayloadNorm: norm}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		name   string
		filter map[string]any
		event  *domain.Event
		want   bool
	}{
		{"source+type match", map[string]any{"source": "gitlab", "event_type": "mr_opened"},
			ev("gitlab", "mr_opened", nil), true},
		{"source mismatch", map[string]any{"source": "github", "event_type": "mr_opened"},
			ev("gitlab", "mr_opened", nil), false},
		{"type mismatch", map[string]any{"source": "gitlab", "event_type": "mr_merged"},
			ev("gitlab", "mr_opened", nil), false},
		{"missing source -> never matches", map[string]any{"event_type": "mr_opened"},
			ev("gitlab", "mr_opened", nil), false},
		{"missing event_type -> never matches", map[string]any{"source": "gitlab"},
			ev("gitlab", "mr_opened", nil), false},
		{"empty filter -> never matches", map[string]any{},
			ev("gitlab", "mr_opened", nil), false},
		{"extra key exact match", map[string]any{"source": "gitlab", "event_type": "mr_opened", "project": "org/repo"},
			ev("gitlab", "mr_opened", map[string]any{"project": "org/repo"}), true},
		{"extra key mismatch", map[string]any{"source": "gitlab", "event_type": "mr_opened", "project": "org/other"},
			ev("gitlab", "mr_opened", map[string]any{"project": "org/repo"}), false},
		{"extra key absent from norm -> no match", map[string]any{"source": "gitlab", "event_type": "mr_opened", "project": "org/repo"},
			ev("gitlab", "mr_opened", map[string]any{}), false},
		// YAML filters parse numbers as int; JSON payload_norm parses them as float64.
		{"numeric coercion int vs float64", map[string]any{"source": "gitlab", "event_type": "mr_opened", "mr_iid": 42},
			ev("gitlab", "mr_opened", map[string]any{"mr_iid": float64(42)}), true},
		{"numeric mismatch", map[string]any{"source": "gitlab", "event_type": "mr_opened", "mr_iid": 42},
			ev("gitlab", "mr_opened", map[string]any{"mr_iid": float64(43)}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Matches(c.filter, c.event); got != c.want {
				t.Errorf("Matches(%v, %v/%v %v) = %v, want %v",
					c.filter, c.event.SourcePlugin, c.event.EventType, c.event.PayloadNorm, got, c.want)
			}
		})
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/events/ -v`
Expected: FAIL (package missing / `Matches` undefined).

- [x] **Step 3: Implement** — `internal/events/match.go`:

```go
// Package events implements the SP3 eventing core: ingestion, the in-process
// bus, the dispatcher, and the poll loop. The events table is the durable
// source of truth; delivery is at-least-once with per-assignment dedup
// downstream making redelivery safe.
package events

import (
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// Matches reports whether an event-subscription filter matches an event.
// The filter must contain non-empty "source" and "event_type" strings
// (config validation enforces this; absence here is defensively a non-match).
// Every other key must exactly match the same-named top-level payload_norm
// field; values are compared as strings via fmt.Sprint so YAML ints match
// JSON float64s.
func Matches(filter map[string]any, ev *domain.Event) bool {
	src, _ := filter["source"].(string)
	typ, _ := filter["event_type"].(string)
	if src == "" || typ == "" || src != ev.SourcePlugin || typ != ev.EventType {
		return false
	}
	for k, v := range filter {
		if k == "source" || k == "event_type" {
			continue
		}
		nv, ok := ev.PayloadNorm[k]
		if !ok || fmt.Sprint(v) != fmt.Sprint(nv) {
			return false
		}
	}
	return true
}
```

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/events/ -v`
Expected: PASS (11 subtests).

- [x] **Step 5: Commit**

```bash
git add internal/events/match.go internal/events/match_test.go
git commit -m "feat(sp3): event-subscription filter matching"
```

---

### Task 4: Pipeline EventID (the one SP1/SP2-shared-code touch)

**Files:**
- Modify: `internal/run/pipeline.go`
- Test: `internal/run/pipeline_test.go` (append)

`domain.Run.EventID` and the `runs.event_id` column have existed since SP1 but are never populated. This task adds `EventID *string` to `ExecuteRequest` and stamps it onto the Run in BOTH record paths (the pause-gate skip record and the main record).

It ALSO fixes a latent dedup-precedence conflict the dispatcher would otherwise trip: `deriveDedupKey` currently checks `mr_iid` BEFORE `dedup_key`. Event params carry both (`payload_norm.mr_iid` + the envelope's `dedup_key` meta key), so a re-pushed MR (new SHA → new event `dedup_key`, same `mr_iid`) would derive `mr_iid:7` and be wrongly skipped — contradicting spec §5.3 ("the dedup key changes only when the MR head SHA changes"). The explicit `dedup_key` must take precedence over inferred keys. Manual/cron runs without a `dedup_key` param are unaffected.

- [x] **Step 1: Write the failing test** (append to `internal/run/pipeline_test.go`):

```go
func TestPipelineExecute_EventIDStamped(t *testing.T) {
	ctx := context.Background()
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "ok"})
	store := state.NewMemStore()
	backendName := "eventid-backend"
	cfg := &config.Config{Backends: []config.Backend{{
		Name: backendName, Kind: "claude", Model: "claude-3-5-sonnet",
		DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
	}}}
	rr := newFakeRunRepo()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: store}

	agentID, dutyID := uuid.New(), uuid.New()
	eventID := "11111111-2222-3333-4444-555555555555"
	run, err := pipeline.Execute(ctx, ExecuteRequest{
		Assignment: &domain.Assignment{
			ID: uuid.New(), AgentID: agentID, DutyID: dutyID, Enabled: true,
			Backend: &domain.BackendRef{Name: backendName}, Config: map[string]any{},
		},
		Agent:       &domain.Agent{ID: agentID, Name: "ev-agent", Role: "t", SystemPrompt: "s", Enabled: true},
		Duty:        &domain.Duty{ID: dutyID, Name: "ev-duty", Role: "t", Description: "d", Prompt: "p"},
		TriggerKind: "event-subscription",
		EventID:     &eventID,
		EventParams: map[string]any{},
		Executor:    fakeExec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.EventID == nil || *run.EventID != eventID {
		t.Errorf("run.EventID = %v, want %q", run.EventID, eventID)
	}
	if stored := rr.runs[run.ID]; stored.EventID == nil || *stored.EventID != eventID {
		t.Errorf("stored EventID = %v", stored.EventID)
	}
}

func TestPipelineExecute_EventIDStampedOnPausedSkip(t *testing.T) {
	pipeline, req, rr, _ := pausedTestFixture(false, true)
	eventID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	req.EventID = &eventID

	run, err := pipeline.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if run.EventID == nil || *run.EventID != eventID {
		t.Errorf("skip-run EventID = %v, want %q", run.EventID, eventID)
	}
	_ = rr
}

func TestDeriveDedupKey_ExplicitKeyWins(t *testing.T) {
	// An explicit dedup_key (event envelope) must beat inferred keys: a
	// re-pushed MR has a new dedup_key but the same mr_iid, and must NOT be
	// collapsed onto the mr_iid-derived key.
	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"explicit beats mr_iid", map[string]any{"mr_iid": 7, "dedup_key": "mr:org/repo:7:sha2"},
			"dedup_key:mr:org/repo:7:sha2"},
		{"mr_iid alone (SP1 manual run)", map[string]any{"mr_iid": "7"}, "mr_iid:7"},
		{"commit_sha alone", map[string]any{"commit_sha": "abc"}, "sha:abc"},
		{"nothing", map[string]any{"foo": "bar"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveDedupKey(c.params); got != c.want {
				t.Errorf("deriveDedupKey(%v) = %q, want %q", c.params, got, c.want)
			}
		})
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/run/ -run TestPipelineExecute_EventID -v`
Expected: FAIL (unknown field `EventID` in ExecuteRequest).

- [x] **Step 3: Implement** — in `internal/run/pipeline.go`:

(a) Add to `ExecuteRequest` (after `TriggerKind string`):
```go
	EventID     *string        // id of the triggering event, if any (event-subscription)
```

(b) In the pause-gate skip block, add to the `run := &domain.Run{...}` literal (after `TriggerKind: req.TriggerKind,`):
```go
			EventID:      req.EventID,
```

(c) In the main run-start block, add to the `run := &domain.Run{...}` literal (after `TriggerKind: req.TriggerKind,`):
```go
		EventID:              req.EventID,
```

(d) Reorder `deriveDedupKey` so the explicit key wins — replace its body with:

```go
// deriveDedupKey extracts a deduplication key from event params. An explicit
// dedup_key (set by the event envelope) takes precedence over inferred keys:
// a re-pushed MR carries a NEW dedup_key but the SAME mr_iid, and must not be
// collapsed onto the mr_iid-derived key.
func deriveDedupKey(params map[string]any) string {
	if v, ok := params["dedup_key"]; ok {
		return fmt.Sprintf("dedup_key:%v", v)
	}
	if v, ok := params["mr_iid"]; ok {
		return fmt.Sprintf("mr_iid:%v", v)
	}
	if v, ok := params["commit_sha"]; ok {
		return fmt.Sprintf("sha:%v", v)
	}
	return ""
}
```

Check existing tests for reliance on the old order: `TestPipelineExecute_DedupSkip` pre-marks `"mr_iid:42"` and passes params `{"mr_iid": "42"}` (no dedup_key) — unaffected. `TestPipelineExecute_ModelReportedFailure` uses `{"mr_iid": "99"}` and asserts `HasProcessed(..., "mr_iid:99")` is false — unaffected.

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/run/ -v`
Expected: PASS — both new tests plus every pre-existing pipeline test.

- [x] **Step 5: Commit**

```bash
git add internal/run/pipeline.go internal/run/pipeline_test.go
git commit -m "feat(sp3): stamp event id onto runs; explicit dedup_key takes precedence"
```

---

### Task 5: run.Invoker (shared cron/dispatcher execution path)

**Files:**
- Create: `internal/run/invoker.go`
- Test: `internal/run/invoker_test.go`

The cron scheduler in `cmd/fleet/main.go` inlines "GetByID assignment → find agent/duty in List → resolve backend → build executor → Execute". The dispatcher needs the same. Extract it as `Invoker`, with interface seams (`*repo.X` satisfy them structurally) and a `buildExecutor` test seam.

- [x] **Step 1: Write the failing tests** — `internal/run/invoker_test.go`:

```go
package run

import (
	"context"
	"fmt"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/google/uuid"
)

type fakeAssignmentGetter struct{ byID map[uuid.UUID]*domain.Assignment }

func (f *fakeAssignmentGetter) GetByID(_ context.Context, id uuid.UUID) (*domain.Assignment, error) {
	a, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("assignment %s not found", id)
	}
	return a, nil
}

type fakeAgentLister struct{ agents []*domain.Agent }

func (f *fakeAgentLister) List(_ context.Context) ([]*domain.Agent, error) { return f.agents, nil }

type fakeDutyLister struct{ duties []*domain.Duty }

func (f *fakeDutyLister) List(_ context.Context) ([]*domain.Duty, error) { return f.duties, nil }

func invokerFixture(t *testing.T) (*Invoker, *fakeRunRepo, uuid.UUID, *executor.FakeExecutor) {
	t.Helper()
	backendName := "inv-backend"
	agentID, dutyID, assignmentID := uuid.New(), uuid.New(), uuid.New()
	cfg := &config.Config{
		Backends: []config.Backend{{
			Name: backendName, Kind: "claude", Model: "claude-3-5-sonnet",
			DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
		}},
		Agents: []config.AgentConfig{{Name: "inv-agent", DefaultBackend: domain.BackendRef{Name: backendName}}},
		Duties: []config.DutyConfig{{Name: "inv-duty"}},
		Assignments: []config.AssignmentConfig{{Agent: "inv-agent", Duty: "inv-duty"}},
	}
	rr := newFakeRunRepo()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: state.NewMemStore()}
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "invoked"})

	inv := &Invoker{
		cfg:      cfg,
		pipeline: pipeline,
		assignments: &fakeAssignmentGetter{byID: map[uuid.UUID]*domain.Assignment{
			assignmentID: {ID: assignmentID, AgentID: agentID, DutyID: dutyID, Enabled: true, Config: map[string]any{}},
		}},
		agents: &fakeAgentLister{agents: []*domain.Agent{{
			ID: agentID, Name: "inv-agent", Role: "t", SystemPrompt: "s",
			DefaultBackend: domain.BackendRef{Name: backendName}, Enabled: true,
		}}},
		duties: &fakeDutyLister{duties: []*domain.Duty{{
			ID: dutyID, Name: "inv-duty", Role: "t", Description: "d", Prompt: "p",
		}}},
		buildExecutor: func(_ *config.Config, _ *config.Backend) (executor.Executor, error) {
			return fakeExec, nil
		},
	}
	return inv, rr, assignmentID, fakeExec
}

func TestInvoker_Invoke(t *testing.T) {
	inv, rr, assignmentID, fakeExec := invokerFixture(t)
	eventID := "deadbeef-0000-0000-0000-000000000000"
	run, err := inv.Invoke(context.Background(), assignmentID, "event-subscription",
		&eventID, map[string]any{"mr_iid": "9"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("status = %q", run.Status)
	}
	if run.TriggerKind != "event-subscription" {
		t.Errorf("trigger kind = %q", run.TriggerKind)
	}
	if run.EventID == nil || *run.EventID != eventID {
		t.Errorf("EventID = %v", run.EventID)
	}
	if fakeExec.LastReq.Prompt == "" {
		t.Error("executor was not called")
	}
	if len(rr.runs) != 1 {
		t.Errorf("recorded runs = %d", len(rr.runs))
	}
}

func TestInvoker_UnknownAssignment(t *testing.T) {
	inv, _, _, _ := invokerFixture(t)
	_, err := inv.Invoke(context.Background(), uuid.New(), "cron", nil, map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown assignment")
	}
}

func TestInvoker_DefaultBuildExecutor(t *testing.T) {
	// nil backend -> claude default; defined backend -> factory dispatch.
	ex, err := defaultBuildExecutor(&config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ex.(*executor.ClaudeExecutor); !ok {
		t.Errorf("nil backend built %T, want *executor.ClaudeExecutor", ex)
	}
	cfg := &config.Config{}
	ex, err = defaultBuildExecutor(cfg, &config.Backend{
		Name: "e", Kind: "openai-compatible", BaseURI: "http://x/v1", Model: "m",
		Auth: config.BackendAuth{Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ex.(*executor.EndpointExecutor); !ok {
		t.Errorf("endpoint backend built %T", ex)
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/run/ -run TestInvoker -v`
Expected: FAIL (`Invoker` undefined).

- [x] **Step 3: Implement** — `internal/run/invoker.go`:

```go
package run

import (
	"context"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/google/uuid"
)

// AssignmentGetter, AgentLister, and DutyLister are the repo capabilities the
// Invoker needs; *repo.AssignmentRepo, *repo.AgentRepo, *repo.DutyRepo satisfy
// them structurally.
type AssignmentGetter interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Assignment, error)
}

type AgentLister interface {
	List(ctx context.Context) ([]*domain.Agent, error)
}

type DutyLister interface {
	List(ctx context.Context) ([]*domain.Duty, error)
}

// Invoker executes one assignment by id: it loads the assignment/agent/duty,
// resolves the backend from config, builds the executor, and runs the
// pipeline. The cron scheduler and the event dispatcher share this path.
type Invoker struct {
	cfg         *config.Config
	pipeline    *Pipeline
	assignments AssignmentGetter
	agents      AgentLister
	duties      DutyLister
	// buildExecutor is a test seam; defaults to factory-based resolution.
	buildExecutor func(cfg *config.Config, b *config.Backend) (executor.Executor, error)
}

func NewInvoker(cfg *config.Config, pipeline *Pipeline, assignments AssignmentGetter, agents AgentLister, duties DutyLister) *Invoker {
	return &Invoker{
		cfg: cfg, pipeline: pipeline,
		assignments: assignments, agents: agents, duties: duties,
		buildExecutor: defaultBuildExecutor,
	}
}

// defaultBuildExecutor keeps SP1's behavior: no resolvable backend means the
// subscription claude CLI; otherwise the factory dispatches on kind.
func defaultBuildExecutor(cfg *config.Config, b *config.Backend) (executor.Executor, error) {
	if b == nil {
		return executor.NewClaudeExecutor(""), nil
	}
	return executor.FromBackend(cfg, b)
}

// Invoke runs one assignment end-to-end and returns the recorded Run.
func (inv *Invoker) Invoke(ctx context.Context, assignmentID uuid.UUID, triggerKind string, eventID *string, params map[string]any) (*domain.Run, error) {
	assignment, err := inv.assignments.GetByID(ctx, assignmentID)
	if err != nil {
		return nil, fmt.Errorf("get assignment: %w", err)
	}

	allAgents, err := inv.agents.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	var agent *domain.Agent
	for _, a := range allAgents {
		if a.ID == assignment.AgentID {
			agent = a
			break
		}
	}
	if agent == nil {
		return nil, fmt.Errorf("agent %s not found", assignment.AgentID)
	}

	allDuties, err := inv.duties.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list duties: %w", err)
	}
	var duty *domain.Duty
	for _, d := range allDuties {
		if d.ID == assignment.DutyID {
			duty = d
			break
		}
	}
	if duty == nil {
		return nil, fmt.Errorf("duty %s not found", assignment.DutyID)
	}

	// Resolve the named backend from config (nil when this assignment has no
	// config counterpart, e.g. DB-only seeds).
	var resolved *config.Backend
	for _, ac := range inv.cfg.Assignments {
		if ac.Agent == agent.Name && ac.Duty == duty.Name {
			if b, _, berr := config.ResolveBackend(inv.cfg, ac); berr == nil {
				resolved = b
			}
			break
		}
	}
	exec, err := inv.buildExecutor(inv.cfg, resolved)
	if err != nil {
		return nil, fmt.Errorf("build executor: %w", err)
	}

	return inv.pipeline.Execute(ctx, ExecuteRequest{
		Assignment:  assignment,
		Agent:       agent,
		Duty:        duty,
		TriggerKind: triggerKind,
		EventID:     eventID,
		EventParams: params,
		Executor:    exec,
	})
}
```

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/run/ -v`
Expected: PASS (all, including the three new Invoker tests).

- [x] **Step 5: Commit**

```bash
git add internal/run/invoker.go internal/run/invoker_test.go
git commit -m "feat(sp3): shared assignment Invoker for cron and event dispatch"
```

---

### Task 6: Ingestor, Dispatcher, MemStore

**Files:**
- Create: `internal/events/mem.go`
- Create: `internal/events/ingest.go`
- Create: `internal/events/dispatcher.go`
- Test: `internal/events/dispatcher_test.go`, `internal/events/ingest_test.go`

- [x] **Step 1: Create `internal/events/mem.go`** (exported in-memory store, mirroring the `state.NewMemStore` precedent — used by unit tests here and the Task 12 integration test):

```go
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
```

- [x] **Step 2: Write the failing ingest test** — `internal/events/ingest_test.go`:

```go
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
```

- [x] **Step 3: Write the failing dispatcher tests** — `internal/events/dispatcher_test.go`:

```go
package events

import (
	"context"
	"fmt"
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
```

- [x] **Step 4: Run to verify failure**

Run: `go test ./internal/events/ -v`
Expected: FAIL (`NewIngestor`, `NewDispatcher`, `busCapacity` undefined; match tests still pass).

- [x] **Step 5: Implement `internal/events/ingest.go`**

```go
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
```

- [x] **Step 6: Implement `internal/events/dispatcher.go`**

```go
package events

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

const (
	busCapacity       = 256
	defaultWorkers    = 4
	defaultRescan     = 30 * time.Second
	rescanBatchLimit  = 100
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
```

- [x] **Step 7: Run to verify pass**

Run: `go test ./internal/events/ -race -v`
Expected: PASS (match + ingest + dispatcher tests; ~1s with the deliberate sleeps).

- [x] **Step 8: Commit**

```bash
git add internal/events/mem.go internal/events/ingest.go internal/events/dispatcher.go internal/events/ingest_test.go internal/events/dispatcher_test.go
git commit -m "feat(sp3): ingestor and at-least-once event dispatcher"
```

---

### Task 7: Poller

**Files:**
- Create: `internal/events/poller.go`
- Test: `internal/events/poller_test.go`

- [x] **Step 1: Write the failing tests** — `internal/events/poller_test.go`:

```go
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
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/events/ -run TestRunPoller -v`
Expected: FAIL (`RunPoller` undefined).

- [x] **Step 3: Implement** — `internal/events/poller.go`:

```go
package events

import (
	"context"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
)

// CursorStore persists poll cursors per plugin. *repo.CursorRepo and MemStore
// satisfy it.
type CursorStore interface {
	Get(ctx context.Context, plugin string) (string, error)
	Set(ctx context.Context, plugin, cursor string) error
}

// IngestFunc matches Ingestor.Ingest.
type IngestFunc func(ctx context.Context, evs []domain.Event) (int, error)

// RunPoller polls src at interval until ctx is done, persisting the cursor
// only after a successful poll AND ingest (so nothing is skipped on failure;
// re-polling is harmless thanks to event-level dedup). The first poll runs
// immediately.
func RunPoller(ctx context.Context, pluginName string, src plugin.PollSource, interval time.Duration,
	cursors CursorStore, ingest IngestFunc, logf func(format string, args ...any)) {

	tick := func() {
		cursor, err := cursors.Get(ctx, pluginName)
		if err != nil {
			logf("poller %s: load cursor: %v", pluginName, err)
			return
		}
		evs, newCursor, err := src.Poll(ctx, cursor)
		if err != nil {
			logf("poller %s: poll: %v", pluginName, err)
			return
		}
		if _, err := ingest(ctx, evs); err != nil {
			logf("poller %s: ingest: %v", pluginName, err)
			return // cursor not advanced; next tick re-polls
		}
		if newCursor != cursor {
			if err := cursors.Set(ctx, pluginName, newCursor); err != nil {
				logf("poller %s: save cursor: %v", pluginName, err)
			}
		}
	}

	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
```

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/events/ -race -v`
Expected: PASS (all events-package tests).

- [x] **Step 5: Commit**

```bash
git add internal/events/poller.go internal/events/poller_test.go
git commit -m "feat(sp3): plugin poll loop with durable cursors"
```

---

### Task 8: GitLab events source (webhook + poll)

**Files:**
- Create: `internal/plugins/gitlab/events.go`
- Modify: `internal/plugins/gitlab/gitlab.go` (Init gains webhook secret + poll config; ConfigSchema documents them; EventSources description loses "wired in SP3")
- Test: `internal/plugins/gitlab/events_test.go`

- [x] **Step 1: Write the failing tests** — `internal/plugins/gitlab/events_test.go`:

```go
package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

const mrWebhookFixture = `{
  "object_kind": "merge_request",
  "user": {"username": "alice"},
  "project": {"path_with_namespace": "org/repo"},
  "object_attributes": {
    "iid": 42,
    "title": "Add feature",
    "action": "open",
    "source_branch": "feat/x",
    "target_branch": "main",
    "url": "https://gitlab.example.com/org/repo/-/merge_requests/42",
    "last_commit": {"id": "abc123def"}
  }
}`

func webhookPlugin(t *testing.T, secret string) *GitLabPlugin {
	t.Helper()
	g := &GitLabPlugin{}
	secrets := func(name string) (string, error) {
		switch name {
		case "gitlab_token":
			return "tok", nil
		case "gitlab_webhook_secret":
			return secret, nil
		}
		return "", nil
	}
	if err := g.Init(context.Background(), map[string]any{}, secrets); err != nil {
		t.Fatal(err)
	}
	return g
}

func webhookReq(body, token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	if token != "" {
		r.Header.Set("X-Gitlab-Token", token)
	}
	return r
}

func TestHandleWebhook_ValidMR(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	evs, err := g.HandleWebhook(context.Background(), webhookReq(mrWebhookFixture, "s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.SourcePlugin != "gitlab" || ev.EventType != "mr_opened" {
		t.Errorf("envelope = %s/%s", ev.SourcePlugin, ev.EventType)
	}
	if ev.Identity != "alice" {
		t.Errorf("identity = %q", ev.Identity)
	}
	if ev.DedupKey != "mr:org/repo:42:abc123def" {
		t.Errorf("dedup_key = %q", ev.DedupKey)
	}
	norm := ev.PayloadNorm
	for k, want := range map[string]any{
		"project": "org/repo", "mr_iid": 42, "title": "Add feature", "action": "open",
		"source_branch": "feat/x", "target_branch": "main",
		"last_commit_sha": "abc123def", "author": "alice",
		"url": "https://gitlab.example.com/org/repo/-/merge_requests/42",
	} {
		if norm[k] != want {
			t.Errorf("payload_norm[%q] = %v (%T), want %v", k, norm[k], norm[k], want)
		}
	}
	if len(ev.PayloadRaw) == 0 {
		t.Error("payload_raw empty")
	}
}

func TestHandleWebhook_ActionMapping(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	for action, wantType := range map[string]string{
		"open": "mr_opened", "update": "mr_updated", "merge": "mr_merged", "close": "mr_closed",
	} {
		body := strings.Replace(mrWebhookFixture, `"action": "open"`, `"action": "`+action+`"`, 1)
		evs, err := g.HandleWebhook(context.Background(), webhookReq(body, "s3cret"))
		if err != nil || len(evs) != 1 {
			t.Fatalf("action %s: evs=%d err=%v", action, len(evs), err)
		}
		if evs[0].EventType != wantType {
			t.Errorf("action %s -> %s, want %s", action, evs[0].EventType, wantType)
		}
	}
	// Unrecognized action: ignored.
	body := strings.Replace(mrWebhookFixture, `"action": "open"`, `"action": "approved"`, 1)
	evs, err := g.HandleWebhook(context.Background(), webhookReq(body, "s3cret"))
	if err != nil || len(evs) != 0 {
		t.Errorf("approved action: evs=%d err=%v, want 0/nil", len(evs), err)
	}
}

func TestHandleWebhook_AuthFailures(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	for name, token := range map[string]string{"wrong": "nope", "missing": ""} {
		_, err := g.HandleWebhook(context.Background(), webhookReq(mrWebhookFixture, token))
		var ae *plugin.AuthError
		if err == nil || !asAuthError(err, &ae) {
			t.Errorf("%s token: err = %v, want *plugin.AuthError", name, err)
		}
	}
	// No secret configured -> reject everything.
	g2 := webhookPlugin(t, "")
	_, err := g2.HandleWebhook(context.Background(), webhookReq(mrWebhookFixture, "anything"))
	var ae *plugin.AuthError
	if err == nil || !asAuthError(err, &ae) {
		t.Errorf("unconfigured secret: err = %v, want *plugin.AuthError", err)
	}
}

func TestHandleWebhook_IgnoredKindAndBadJSON(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	evs, err := g.HandleWebhook(context.Background(),
		webhookReq(`{"object_kind": "push"}`, "s3cret"))
	if err != nil || len(evs) != 0 {
		t.Errorf("push kind: evs=%d err=%v, want 0/nil", len(evs), err)
	}
	_, err = g.HandleWebhook(context.Background(), webhookReq(`{not json`, "s3cret"))
	if err == nil {
		t.Error("bad JSON: expected parse error")
	}
}

const pollMRFixture = `[
  {"iid": 42, "title": "Add feature", "sha": "abc123def",
   "source_branch": "feat/x", "target_branch": "main",
   "web_url": "https://gitlab.example.com/org/repo/-/merge_requests/42",
   "updated_at": "2026-06-10T12:00:00Z",
   "author": {"username": "alice"}}
]`

func TestPoll_NormalizationParityAndCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// NOTE: r.URL.Path is DECODED by net/http; the %2F survives only in
		// EscapedPath(). The GitLab API requires the encoded form on the wire.
		if !strings.Contains(r.URL.EscapedPath(), "/api/v4/projects/org%2Frepo/merge_requests") {
			t.Errorf("escaped path = %q", r.URL.EscapedPath())
		}
		q := r.URL.Query()
		if q.Get("state") != "opened" || q.Get("order_by") != "updated_at" || q.Get("sort") != "asc" {
			t.Errorf("query = %v", q)
		}
		if q.Get("updated_after") == "" {
			t.Error("updated_after missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pollMRFixture))
	}))
	defer srv.Close()

	g := webhookPlugin(t, "s3cret")
	g.baseURL = srv.URL
	g.pollProjects = []string{"org/repo"}

	evs, newCursor, err := g.Poll(context.Background(), "2026-06-10T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d", len(evs))
	}
	ev := evs[0]
	// Parity with the webhook envelope for the same MR state:
	if ev.DedupKey != "mr:org/repo:42:abc123def" {
		t.Errorf("dedup_key = %q (must equal webhook's for the same MR+SHA)", ev.DedupKey)
	}
	if ev.EventType != "mr_updated" {
		t.Errorf("event type = %q, want mr_updated (poll cannot distinguish opened)", ev.EventType)
	}
	if ev.PayloadNorm["author"] != "alice" || ev.PayloadNorm["project"] != "org/repo" {
		t.Errorf("norm = %v", ev.PayloadNorm)
	}
	if newCursor != "2026-06-10T12:00:00Z" {
		t.Errorf("cursor = %q, want max updated_at", newCursor)
	}
}

func TestPoll_PartialFailureKeepsCursor(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.Contains(r.URL.Path, "bad%2Frepo") {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pollMRFixture))
	}))
	defer srv.Close()

	g := webhookPlugin(t, "s3cret")
	g.baseURL = srv.URL
	g.pollProjects = []string{"org/repo", "bad/repo"}

	evs, newCursor, err := g.Poll(context.Background(), "2026-06-10T00:00:00Z")
	if err != nil {
		t.Fatalf("partial failure must not error: %v", err)
	}
	if len(evs) != 1 {
		t.Errorf("events = %d, want 1 from the healthy project", len(evs))
	}
	if newCursor != "2026-06-10T00:00:00Z" {
		t.Errorf("cursor = %q, want UNCHANGED on partial failure", newCursor)
	}
	if calls != 2 {
		t.Errorf("API calls = %d", calls)
	}
}

func TestPoll_TotalFailureErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	g := webhookPlugin(t, "s3cret")
	g.baseURL = srv.URL
	g.pollProjects = []string{"org/repo"}
	_, _, err := g.Poll(context.Background(), "2026-06-10T00:00:00Z")
	if err == nil {
		t.Fatal("expected error when every project fails")
	}
}

func TestPoll_EmptyCursorUsesWindow(t *testing.T) {
	var gotAfter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAfter = r.URL.Query().Get("updated_after")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	g := webhookPlugin(t, "s3cret")
	g.baseURL = srv.URL
	g.pollProjects = []string{"org/repo"}
	g.pollInterval = time.Minute

	_, _, err := g.Poll(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	parsed, perr := time.Parse(time.RFC3339, gotAfter)
	if perr != nil {
		t.Fatalf("updated_after %q not RFC3339: %v", gotAfter, perr)
	}
	if since := time.Since(parsed); since < 30*time.Second || since > 5*time.Minute {
		t.Errorf("first-poll window = %v ago, want ~poll_interval", since)
	}
}

func TestPoll_NoProjectsNoop(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	evs, cursor, err := g.Poll(context.Background(), "c0")
	if err != nil || len(evs) != 0 || cursor != "c0" {
		t.Errorf("no-project poll: evs=%d cursor=%q err=%v", len(evs), cursor, err)
	}
}

// asAuthError mirrors errors.As without importing errors twice in assertions.
func asAuthError(err error, target **plugin.AuthError) bool {
	ae, ok := err.(*plugin.AuthError)
	if ok {
		*target = ae
	}
	return ok
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/plugins/gitlab/ -v`
Expected: FAIL (`HandleWebhook`, `Poll`, fields undefined).

- [x] **Step 3: Extend `gitlab.go`** — add struct fields and Init parsing. The `GitLabPlugin` struct becomes:

```go
// GitLabPlugin is the GitLab integration plugin: actions (post_mr_comment)
// plus the mr_events source (webhook push + poll).
type GitLabPlugin struct {
	token         string
	baseURL       string
	webhookSecret string
	pollProjects  []string
	pollInterval  time.Duration
}
```

In `Init`, after the existing base_url handling, add:

```go
	// Webhook secret: optional at init; the webhook handler rejects all
	// requests when it is unset (push ingestion requires it).
	ws, err := secrets("gitlab_webhook_secret")
	if err != nil {
		return fmt.Errorf("gitlab: resolve secret gitlab_webhook_secret: %w", err)
	}
	g.webhookSecret = ws

	g.pollInterval = time.Minute
	if v, ok := cfg["poll_interval"].(string); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("gitlab: invalid poll_interval %q: %w", v, err)
		}
		g.pollInterval = d
	}
	g.pollProjects = nil
	if list, ok := cfg["poll_projects"].([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok && s != "" {
				g.pollProjects = append(g.pollProjects, s)
			}
		}
	}
```

Add `"time"` to gitlab.go imports. Update `EventSources()`'s description to drop "(wired in SP3)":
```go
		{Name: "mr_events", Description: "Merge request opened/updated/merged/closed events (webhook + poll)"},
```
Extend `ConfigSchema()` properties with:
```go
			"poll_interval": map[string]any{"type": "string", "default": "60s"},
			"poll_projects": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
```

- [x] **Step 4: Implement `internal/plugins/gitlab/events.go`**

```go
package gitlab

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
)

const maxWebhookBody = 1 << 20 // 1 MiB

// actionToEventType maps GitLab MR webhook actions to envelope event types.
// Unlisted actions (approved, reopen, ...) are ignored in SP3.
func actionToEventType(action string) (string, bool) {
	switch action {
	case "open":
		return "mr_opened", true
	case "update":
		return "mr_updated", true
	case "merge":
		return "mr_merged", true
	case "close":
		return "mr_closed", true
	}
	return "", false
}

type webhookMRPayload struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Username string `json:"username"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		Action       string `json:"action"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		URL          string `json:"url"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
}

// HandleWebhook implements plugin.WebhookSource for GitLab webhooks.
func (g *GitLabPlugin) HandleWebhook(_ context.Context, r *http.Request) ([]domain.Event, error) {
	if g.webhookSecret == "" {
		return nil, &plugin.AuthError{Msg: "gitlab: webhook secret not configured (set secret gitlab_webhook_secret)"}
	}
	token := r.Header.Get("X-Gitlab-Token")
	if subtle.ConstantTimeCompare([]byte(token), []byte(g.webhookSecret)) != 1 {
		return nil, &plugin.AuthError{Msg: "gitlab: invalid webhook token"}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		return nil, fmt.Errorf("gitlab: read webhook body: %w", err)
	}
	var payload webhookMRPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("gitlab: parse webhook: %w", err)
	}
	if payload.ObjectKind != "merge_request" {
		return nil, nil // not an MR event; acknowledged and ignored
	}
	eventType, ok := actionToEventType(payload.ObjectAttributes.Action)
	if !ok {
		return nil, nil // unhandled action; acknowledged and ignored
	}

	a := payload.ObjectAttributes
	ev := normalizeMR(eventType, payload.Project.PathWithNamespace, a.IID, a.Title, a.Action,
		a.SourceBranch, a.TargetBranch, a.LastCommit.ID, payload.User.Username, a.URL, body)
	return []domain.Event{ev}, nil
}

// normalizeMR builds the shared envelope both ingestion surfaces emit.
// The dedup key changes only when the MR head SHA changes.
func normalizeMR(eventType, project string, iid int, title, action, sourceBranch, targetBranch, sha, author, url string, raw []byte) domain.Event {
	return domain.Event{
		SourcePlugin: "gitlab",
		EventType:    eventType,
		PayloadRaw:   json.RawMessage(raw),
		PayloadNorm: map[string]any{
			"project":         project,
			"mr_iid":          iid,
			"title":           title,
			"action":          action,
			"source_branch":   sourceBranch,
			"target_branch":   targetBranch,
			"last_commit_sha": sha,
			"author":          author,
			"url":             url,
		},
		Identity: author,
		DedupKey: fmt.Sprintf("mr:%s:%d:%s", project, iid, sha),
	}
}

type pollMR struct {
	IID          int       `json:"iid"`
	Title        string    `json:"title"`
	SHA          string    `json:"sha"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	WebURL       string    `json:"web_url"`
	UpdatedAt    time.Time `json:"updated_at"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
}

// Poll implements plugin.PollSource: it lists recently-updated open MRs per
// configured project. The cursor (RFC3339 of the max updated_at seen)
// advances only when EVERY project polled successfully — a partial failure
// returns the gathered events with the unchanged cursor (re-polling the
// healthy projects is harmless: event-level dedup collapses duplicates).
// Poll-discovered MRs emit mr_updated (poll cannot distinguish "opened").
func (g *GitLabPlugin) Poll(ctx context.Context, cursor string) ([]domain.Event, string, error) {
	if len(g.pollProjects) == 0 {
		return nil, cursor, nil
	}
	since := cursor
	if since == "" {
		since = time.Now().Add(-g.pollInterval).UTC().Format(time.RFC3339)
	}

	var events []domain.Event
	maxUpdated, _ := time.Parse(time.RFC3339, since)
	allOK := true
	failures := 0
	for _, project := range g.pollProjects {
		mrs, err := g.fetchUpdatedMRs(ctx, project, since)
		if err != nil {
			allOK = false
			failures++
			continue
		}
		for _, mr := range mrs {
			raw, _ := json.Marshal(mr)
			events = append(events, normalizeMR("mr_updated", project, mr.IID, mr.Title, "update",
				mr.SourceBranch, mr.TargetBranch, mr.SHA, mr.Author.Username, mr.WebURL, raw))
			if mr.UpdatedAt.After(maxUpdated) {
				maxUpdated = mr.UpdatedAt
			}
		}
	}
	if failures == len(g.pollProjects) {
		return nil, cursor, fmt.Errorf("gitlab poll: all %d projects failed", failures)
	}
	newCursor := cursor
	if allOK {
		newCursor = maxUpdated.UTC().Format(time.RFC3339)
	}
	return events, newCursor, nil
}

func (g *GitLabPlugin) fetchUpdatedMRs(ctx context.Context, project, since string) ([]pollMR, error) {
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?state=opened&order_by=updated_at&sort=asc&updated_after=%s",
		g.baseURL, url.PathEscape(project), url.QueryEscape(since))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("gitlab: build poll request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: poll %s: %w", project, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*maxWebhookBody))
	if err != nil {
		return nil, fmt.Errorf("gitlab: read poll response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab: poll %s returned %d: %s", project, resp.StatusCode, truncateForErr(body))
	}
	var mrs []pollMR
	if err := json.Unmarshal(body, &mrs); err != nil {
		return nil, fmt.Errorf("gitlab: parse poll response: %w", err)
	}
	return mrs, nil
}

func truncateForErr(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}
```

NOTE: `url.PathEscape("org/repo")` produces `org%2Frepo` — matching both the GitLab API requirement and the test's path assertion. The existing `postMRComment` uses a manual `strings.ReplaceAll(project, "/", "%2F")`; leave it as is (out of scope).

- [x] **Step 5: Run to verify pass**

Run: `go test ./internal/plugins/gitlab/ -v`
Expected: PASS (new event tests + the pre-existing action tests).

- [x] **Step 6: Commit**

```bash
git add internal/plugins/gitlab/
git commit -m "feat(sp3): gitlab mr_events source — authenticated webhook and cursor poll"
```

---

### Task 9: Webhook server

**Files:**
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

- [x] **Step 1: Write the failing tests** — `internal/server/server_test.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
)

// stubPlugin implements plugin.Plugin (+ optionally WebhookSource).
type stubPlugin struct {
	name    string
	events  []domain.Event
	err     error
	webhook bool
}

func (s *stubPlugin) Name() string                       { return s.name }
func (s *stubPlugin) EventSources() []plugin.EventSource { return nil }
func (s *stubPlugin) Actions() []plugin.Action           { return nil }
func (s *stubPlugin) ConfigSchema() plugin.Schema        { return nil }
func (s *stubPlugin) Init(_ context.Context, _ map[string]any, _ plugin.SecretLookup) error {
	return nil
}
func (s *stubPlugin) Do(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
	return nil, nil
}

type webhookStub struct{ stubPlugin }

func (w *webhookStub) HandleWebhook(_ context.Context, _ *http.Request) ([]domain.Event, error) {
	return w.events, w.err
}

type fakeIngestor struct {
	got []domain.Event
	n   int
	err error
}

func (f *fakeIngestor) Ingest(_ context.Context, evs []domain.Event) (int, error) {
	f.got = append(f.got, evs...)
	if f.err != nil {
		return 0, f.err
	}
	return f.n, nil
}

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(New(&fakeIngestor{}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestWebhook_Accepted(t *testing.T) {
	p := &webhookStub{stubPlugin: stubPlugin{name: "hooktest-ok"},
	}
	p.events = []domain.Event{{SourcePlugin: "hooktest-ok", EventType: "t", DedupKey: "k"}}
	plugin.Register(p)
	ing := &fakeIngestor{n: 1}
	srv := httptest.NewServer(New(ing).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/webhooks/hooktest-ok", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["accepted"] != float64(1) {
		t.Errorf("accepted = %v", body["accepted"])
	}
	if len(ing.got) != 1 {
		t.Errorf("ingested = %d", len(ing.got))
	}
}

func TestWebhook_StatusCodes(t *testing.T) {
	plugin.Register(&stubPlugin{name: "hooktest-nosrc"}) // no WebhookSource
	authFail := &webhookStub{stubPlugin: stubPlugin{name: "hooktest-auth"}}
	authFail.err = &plugin.AuthError{Msg: "bad token"}
	plugin.Register(authFail)
	parseFail := &webhookStub{stubPlugin: stubPlugin{name: "hooktest-parse"}}
	parseFail.err = fmt.Errorf("garbled payload")
	plugin.Register(parseFail)

	ingFail := &webhookStub{stubPlugin: stubPlugin{name: "hooktest-ingfail"}}
	ingFail.events = []domain.Event{{SourcePlugin: "hooktest-ingfail", EventType: "t", DedupKey: "k"}}
	plugin.Register(ingFail)

	srv := httptest.NewServer(New(&fakeIngestor{}).Handler())
	defer srv.Close()
	srvIngFail := httptest.NewServer(New(&fakeIngestor{err: fmt.Errorf("db down")}).Handler())
	defer srvIngFail.Close()

	cases := []struct {
		url  string
		want int
	}{
		{srv.URL + "/webhooks/no-such-plugin", 404},
		{srv.URL + "/webhooks/hooktest-nosrc", 404},
		{srv.URL + "/webhooks/hooktest-auth", 401},
		{srv.URL + "/webhooks/hooktest-parse", 400},
		{srvIngFail.URL + "/webhooks/hooktest-ingfail", 500},
	}
	for _, c := range cases {
		resp, err := http.Post(c.url, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("POST %s = %d, want %d", c.url, resp.StatusCode, c.want)
		}
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/server/ -v`
Expected: FAIL (package missing).

- [x] **Step 3: Implement** — `internal/server/server.go`:

```go
// Package server hosts OfficeFleet's HTTP ingestion surface: plugin webhooks
// and a health check. SP4 mounts the REST API/UI into this same server.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
)

// Ingestor persists inbound events; events.Ingestor satisfies it.
type Ingestor interface {
	Ingest(ctx context.Context, evs []domain.Event) (int, error)
}

type Server struct {
	ingestor Ingestor
	logf     func(format string, args ...any)
}

func New(ingestor Ingestor) *Server {
	return &Server{
		ingestor: ingestor,
		logf:     func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /webhooks/{plugin}", s.handleWebhook)
	return mux
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("plugin")
	p, ok := plugin.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown plugin"})
		return
	}
	ws, ok := p.(plugin.WebhookSource)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "plugin has no webhook source"})
		return
	}

	evs, err := ws.HandleWebhook(r.Context(), r)
	var authErr *plugin.AuthError
	if errors.As(err, &authErr) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": authErr.Msg})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	n, err := s.ingestor.Ingest(r.Context(), evs)
	if err != nil {
		s.logf("server: ingest webhook %s: %v", name, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "ingest failed"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": n})
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/server/ -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/server/
git commit -m "feat(sp3): webhook ingestion server"
```

---

### Task 10: Config — ServeConfig + event-subscription validation

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (append)

- [x] **Step 1: Write the failing tests** (append to `internal/config/config_test.go`; tests are `package config_test`, use `config.X` and the existing `errorsContain` helper):

```go
// --- SP3 validation tests ---

func eventSubConfig() *config.Config {
	return &config.Config{
		Backends: []config.Backend{{Name: "b", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:   []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "b"}}},
		Duties: []config.DutyConfig{{
			Name: "d1", TriggerKinds: []string{"manual", "event-subscription"},
		}},
		Assignments: []config.AssignmentConfig{{
			Agent: "a1", Duty: "d1",
			Trigger: domain.TriggerConfig{
				Kind:   "event-subscription",
				Filter: map[string]any{"source": "gitlab", "event_type": "mr_opened"},
			},
		}},
	}
}

func TestValidate_EventSubscriptionValid(t *testing.T) {
	if errs := config.Validate(eventSubConfig()); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_EventSubscriptionMissingSource(t *testing.T) {
	cfg := eventSubConfig()
	cfg.Assignments[0].Trigger.Filter = map[string]any{"event_type": "mr_opened"}
	errorsContain(t, config.Validate(cfg), "source")
}

func TestValidate_EventSubscriptionMissingEventType(t *testing.T) {
	cfg := eventSubConfig()
	cfg.Assignments[0].Trigger.Filter = map[string]any{"source": "gitlab"}
	errorsContain(t, config.Validate(cfg), "event_type")
}

func TestValidate_EventSubscriptionDutyKindMismatch(t *testing.T) {
	cfg := eventSubConfig()
	cfg.Duties[0].TriggerKinds = []string{"manual", "cron"}
	errorsContain(t, config.Validate(cfg), "trigger_kinds")
}

func TestValidate_ServeBlock(t *testing.T) {
	cfg := eventSubConfig()
	cfg.Serve = config.ServeConfig{Workers: -1}
	errorsContain(t, config.Validate(cfg), "workers")

	cfg = eventSubConfig()
	cfg.Serve = config.ServeConfig{RescanInterval: "soonish"}
	errorsContain(t, config.Validate(cfg), "rescan_interval")

	cfg = eventSubConfig()
	cfg.Serve = config.ServeConfig{Addr: ":9090", Workers: 8, RescanInterval: "45s"}
	if errs := config.Validate(cfg); len(errs) != 0 {
		t.Errorf("valid serve block rejected: %v", errs)
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run 'TestValidate_EventSubscription|TestValidate_ServeBlock' -v`
Expected: FAIL (`ServeConfig` undefined).

- [x] **Step 3: Implement** — in `internal/config/config.go`:

(a) Add the type and root field:

```go
// ServeConfig configures the fleet serve daemon.
type ServeConfig struct {
	Addr           string `yaml:"addr,omitempty"`            // webhook listener; default ":8080"
	Workers        int    `yaml:"workers,omitempty"`         // dispatcher pool; default 4
	RescanInterval string `yaml:"rescan_interval,omitempty"` // Go duration; default "30s"
}
```

and in `Config`: `Serve ServeConfig \`yaml:"serve,omitempty"\`` (after `Database`).

(b) In `Validate`, after the backends section, add:

```go
	if cfg.Serve.Workers < 0 {
		errs = append(errs, fmt.Errorf("serve: workers must be >= 0, got %d", cfg.Serve.Workers))
	}
	if cfg.Serve.RescanInterval != "" {
		if _, err := time.ParseDuration(cfg.Serve.RescanInterval); err != nil {
			errs = append(errs, fmt.Errorf("serve: invalid rescan_interval %q: %v", cfg.Serve.RescanInterval, err))
		}
	}
```

(c) In the assignments loop (after the existing trigger-agnostic checks), add:

```go
		if a.Trigger.Kind == "event-subscription" {
			src, _ := a.Trigger.Filter["source"].(string)
			typ, _ := a.Trigger.Filter["event_type"].(string)
			if src == "" {
				errs = append(errs, fmt.Errorf("assignment[%d]: event-subscription trigger requires a non-empty filter.source", i))
			}
			if typ == "" {
				errs = append(errs, fmt.Errorf("assignment[%d]: event-subscription trigger requires a non-empty filter.event_type", i))
			}
			if dutyOK {
				duty := dutyByName[a.Duty]
				if !slices.Contains(duty.TriggerKinds, "event-subscription") {
					errs = append(errs, fmt.Errorf("assignment[%d]: duty %q trigger_kinds does not include event-subscription", i, a.Duty))
				}
			}
		}
```

Add `"slices"` to config.go's imports. (`dutyOK` and `dutyByName` already exist in that loop — verify against the current code.)

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — all new tests AND every pre-existing config test.

- [x] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(sp3): serve block and event-subscription trigger validation"
```

---

### Task 11: CLI — serve, events, scheduler extraction, sample config

**Files:**
- Modify: `cmd/fleet/main.go`
- Modify: `configs/fleet.yaml`

No automated main.go tests (project precedent); verification is compile + vet + smoke. All dispatch/match/poll logic lives in already-tested packages.

- [x] **Step 1: Extract the scheduler loop.** In `cmd/fleet/main.go`, the body of `scheduleCmd`'s `sched.Run(...)` callback currently loads assignment/agent/duty, resolves the backend, builds the executor, and calls `pipeline.Execute`. Replace `scheduleCmd` with:

```go
// scheduleCmd returns the "schedule" daemon subcommand (cron only).
// Deprecated in favor of fleet serve, which also hosts webhooks, polling,
// and the event dispatcher.
func scheduleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schedule",
		Short: "Run the cron scheduler daemon (deprecated: use fleet serve)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, err := loadValidatedConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			initPlugins(ctx, cfg, pool)
			inv := buildInvoker(cfg, pool)
			return runSchedulerLoop(ctx, pool, inv)
		},
	}
}

// buildInvoker wires the shared assignment-execution path.
func buildInvoker(cfg *config.Config, pool *pgxpool.Pool) *run.Invoker {
	pipeline := run.NewPipeline(cfg, repo.NewRunRepo(pool), state.NewPostgresStore(pool), &dbSecretsProvider{pool: pool})
	return run.NewInvoker(cfg, pipeline,
		repo.NewAssignmentRepo(pool), repo.NewAgentRepo(pool), repo.NewDutyRepo(pool))
}

// runSchedulerLoop blocks running cron-triggered assignments until ctx is done.
func runSchedulerLoop(ctx context.Context, pool *pgxpool.Pool, inv *run.Invoker) error {
	assignments, err := repo.NewAssignmentRepo(pool).List(ctx)
	if err != nil {
		return fmt.Errorf("list assignments: %w", err)
	}
	sched := trigger.NewScheduler()
	for _, a := range assignments {
		if !a.Enabled || a.Trigger.Kind != "cron" {
			continue
		}
		t := trigger.NewCron(a.Trigger.Schedule)
		if err := sched.Add(a.ID.String(), t, time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping assignment %s: bad cron schedule: %v\n", a.ID, err)
			continue
		}
		fmt.Printf("scheduled assignment %s (schedule: %s)\n", a.ID, a.Trigger.Schedule)
	}
	fmt.Println("scheduler running...")
	sched.Run(ctx, func(runCtx context.Context, assignmentID string) {
		id, err := uuid.Parse(assignmentID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scheduler: invalid assignment id %s: %v\n", assignmentID, err)
			return
		}
		result, err := inv.Invoke(runCtx, id, "cron", nil, map[string]any{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "scheduler: execute assignment %s: %v\n", assignmentID, err)
			return
		}
		if result.Error != nil {
			fmt.Printf("scheduler: assignment %s completed with status %s (error: %s)\n", assignmentID, result.Status, *result.Error)
		} else {
			fmt.Printf("scheduler: assignment %s completed with status %s\n", assignmentID, result.Status)
		}
	})
	return nil
}
```

This DELETES the old inline agent/duty/backend resolution from scheduleCmd (the Invoker owns it now). Behavior note: the old code printed errors identically; the only intended change is the shared path.

- [x] **Step 2: Add `serveCmd`** (and register `root.AddCommand(serveCmd())` + `root.AddCommand(eventsCmd())` in `main()`):

```go
// serveCmd returns the "serve" daemon: webhooks, polling, dispatcher, cron.
func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the OfficeFleet daemon (webhooks, polling, event dispatch, cron)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			cfg, err := loadValidatedConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			initPlugins(ctx, cfg, pool)

			inv := buildInvoker(cfg, pool)
			eventRepo := repo.NewEventRepo(pool)
			cursorRepo := repo.NewCursorRepo(pool)

			addr := cfg.Serve.Addr
			if addr == "" {
				addr = ":8080"
			}
			rescan := 30 * time.Second
			if cfg.Serve.RescanInterval != "" {
				rescan, _ = time.ParseDuration(cfg.Serve.RescanInterval) // validated at load
			}

			dispatcher := events.NewDispatcher(eventRepo, repo.NewAssignmentRepo(pool), inv, cfg.Serve.Workers, rescan)
			ingestor := events.NewIngestor(eventRepo, dispatcher.Notify)
			go dispatcher.Run(ctx)

			// Poll loops: one per plugin that implements PollSource.
			for _, p := range plugin.All() {
				src, ok := p.(plugin.PollSource)
				if !ok {
					continue
				}
				interval := pollInterval(cfg, p.Name())
				fmt.Printf("polling %s every %s\n", p.Name(), interval)
				go events.RunPoller(ctx, p.Name(), src, interval, cursorRepo, ingestor.Ingest,
					func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
			}

			httpSrv := &http.Server{Addr: addr, Handler: server.New(ingestor).Handler()}
			go func() {
				fmt.Printf("webhook listener on %s\n", addr)
				if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					fmt.Fprintf(os.Stderr, "serve: http: %v\n", err)
				}
			}()

			go func() {
				if err := runSchedulerLoop(ctx, pool, inv); err != nil {
					fmt.Fprintf(os.Stderr, "serve: scheduler: %v\n", err)
				}
			}()

			<-ctx.Done()
			fmt.Println("shutting down...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutdownCtx)
			return nil
		},
	}
}

// pollInterval reads poll_interval from a plugin's config block (default 60s).
func pollInterval(cfg *config.Config, pluginName string) time.Duration {
	for _, pc := range cfg.Plugins {
		if pc.Name != pluginName {
			continue
		}
		if v, ok := pc.Config["poll_interval"].(string); ok && v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
	}
	return time.Minute
}
```

New imports for main.go: `"errors"` (already present from SP2's loadValidatedConfig — verify), `"net/http"`, `"os/signal"`, `"syscall"`, `"github.com/cbarraford/office-fleet/internal/events"`, `"github.com/cbarraford/office-fleet/internal/server"`, `"github.com/jackc/pgx/v5/pgxpool"` (verify — dbSecretsProvider already uses it).

- [x] **Step 3: Add `eventsCmd`:**

```go
// eventsCmd returns the "events" group of subcommands.
func eventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Event management commands",
	}
	cmd.AddCommand(eventsListCmd())
	cmd.AddCommand(eventsReplayCmd())
	return cmd
}

func eventsListCmd() *cobra.Command {
	var flagStatus string
	var flagLimit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent events",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig()
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()

			evs, err := repo.NewEventRepo(pool).ListRecent(ctx, flagStatus, flagLimit)
			if err != nil {
				return fmt.Errorf("list events: %w", err)
			}
			if len(evs) == 0 {
				fmt.Println("(no events)")
				return nil
			}
			fmt.Printf("%-36s %-10s %-14s %-11s %-25s %s\n", "ID", "SOURCE", "TYPE", "STATUS", "RECEIVED", "DEDUP_KEY")
			fmt.Println(strings.Repeat("-", 130))
			for _, ev := range evs {
				fmt.Printf("%-36s %-10s %-14s %-11s %-25s %s\n",
					ev.ID, ev.SourcePlugin, ev.EventType, ev.Status,
					ev.ReceivedAt.Format(time.RFC3339), ev.DedupKey)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&flagStatus, "status", "", "filter by status (pending|dispatched)")
	cmd.Flags().IntVar(&flagLimit, "limit", 50, "max events to show")
	return cmd
}

func eventsReplayCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "replay <event-id>",
		Short: "Re-queue a dispatched event (picked up by fleet serve's rescan)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid event id: %w", err)
			}
			cfg, _ := loadConfig()
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()

			eventRepo := repo.NewEventRepo(pool)
			ev, err := eventRepo.GetByID(ctx, id)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}
			if ev.Status == domain.EventStatusPending {
				fmt.Printf("event %s is already pending\n", id)
				return nil
			}
			if err := eventRepo.MarkPending(ctx, id); err != nil {
				return fmt.Errorf("mark pending: %w", err)
			}
			fmt.Printf("event %s re-queued; fleet serve's rescan will dispatch it within one interval\n", id)
			fmt.Println("note: assignments that already processed this event's dedup_key will record a skipped run")
			return nil
		},
	}
}
```

- [x] **Step 4: Update `configs/fleet.yaml`.** Add after the `database:` block:

```yaml
serve:
  addr: ":8080"          # webhook listener (POST /webhooks/gitlab)
  workers: 4             # dispatcher worker pool
  rescan_interval: 30s   # pending-event redispatch period
```

In the gitlab plugin config block, add poll settings (commented — polling is opt-in):

```yaml
plugins:
  - name: gitlab
    config:
      base_url: "https://gitlab.com"
#      poll_interval: 60s
#      poll_projects: ["myorg/myrepo"]
```

In the `mr-reviewer` duty, extend `trigger_kinds` with `- event-subscription`. Then add a third assignment after the cron one:

```yaml
  - agent: dev-1
    duty: mr-reviewer
    enabled: true
    trigger:
      kind: event-subscription
      filter:
        source: gitlab
        event_type: mr_opened
        project: "myorg/myrepo"
    config:
      project: "myorg/myrepo"
    extra_instructions: "A new MR just opened; review it promptly. Keep comments terse."
    outputs:
      - plugin: gitlab
        action: post_mr_comment
        params:
          project: "{{.Assignment.project}}"
          mr_iid: "{{.Event.mr_iid}}"
          body: "{{.Event.llm_summary}}"
```

- [x] **Step 5: Build, vet, smoke**

```bash
go build ./... && go vet ./...
go test ./...
FLEET_DATABASE_DSN=postgres://localhost/x go run ./cmd/fleet --config configs/fleet.yaml config validate
go run ./cmd/fleet --config configs/fleet.yaml events list --help
go run ./cmd/fleet --config configs/fleet.yaml serve --help
```
Expected: all clean; validate prints OK; help texts render.

- [x] **Step 6: Commit**

```bash
git add cmd/fleet/main.go configs/fleet.yaml
git commit -m "feat(sp3): fleet serve daemon, events CLI, shared scheduler loop"
```

---

### Task 12: Full-vertical integration test

**Files:**
- Create: `internal/run/event_integration_test.go`

In-package (`package run`) so the test can build `&Pipeline{...}`/`&Invoker{...}` with unexported fields, reusing `fakeRunRepo` and the Task 5 fakes. It composes the REAL gitlab plugin webhook parsing, REAL server, REAL ingestor/dispatcher (MemStore), and the REAL pipeline with a fake executor.

- [x] **Step 1: Write the test** — `internal/run/event_integration_test.go`:

```go
package run

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/events"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/server"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/google/uuid"

	// Registers the gitlab plugin.
	_ "github.com/cbarraford/office-fleet/internal/plugins/gitlab"
)

const integrationWebhookFixture = `{
  "object_kind": "merge_request",
  "user": {"username": "alice"},
  "project": {"path_with_namespace": "org/repo"},
  "object_attributes": {
    "iid": 7,
    "title": "Integration MR",
    "action": "open",
    "source_branch": "feat/y",
    "target_branch": "main",
    "url": "https://gitlab.example.com/org/repo/-/merge_requests/7",
    "last_commit": {"id": "feedface"}
  }
}`

func TestEventVertical_WebhookToRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- real gitlab plugin, initialized with a webhook secret ---
	gl, ok := plugin.Get("gitlab")
	if !ok {
		t.Fatal("gitlab plugin not registered")
	}
	secrets := func(name string) (string, error) {
		if name == "gitlab_webhook_secret" {
			return "integration-secret", nil
		}
		return "", nil
	}
	if err := gl.Init(ctx, map[string]any{}, secrets); err != nil {
		t.Fatal(err)
	}

	// --- output recorder plugin ---
	recorder := &deliveryRecorder{name: "sp3-recorder-plugin"}
	plugin.Register(recorder)

	// --- pipeline + invoker with fakes ---
	backendName := "sp3-backend"
	agentID, dutyID, assignmentID := uuid.New(), uuid.New(), uuid.New()
	cfg := &config.Config{Backends: []config.Backend{{
		Name: backendName, Kind: "claude", Model: "m",
		DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
	}}}
	rr := newFakeRunRepo()
	memState := state.NewMemStore()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: memState}
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "auto-reviewed"})

	assignment := &domain.Assignment{
		ID: assignmentID, AgentID: agentID, DutyID: dutyID, Enabled: true,
		Backend: &domain.BackendRef{Name: backendName},
		Config:  map[string]any{},
		Trigger: domain.TriggerConfig{Kind: "event-subscription", Filter: map[string]any{
			"source": "gitlab", "event_type": "mr_opened", "project": "org/repo",
		}},
		Outputs: []domain.OutputBinding{{
			Plugin: "sp3-recorder-plugin", Action: "post",
			Params: map[string]any{"body": "{{.Event.llm_summary}}", "mr": "{{.Event.mr_iid}}"},
		}},
	}
	inv := &Invoker{
		cfg: cfg, pipeline: pipeline,
		assignments: &fakeAssignmentGetter{byID: map[uuid.UUID]*domain.Assignment{assignmentID: assignment}},
		agents: &fakeAgentLister{agents: []*domain.Agent{{
			ID: agentID, Name: "sp3-agent", Role: "dev", SystemPrompt: "reviewer",
			DefaultBackend: domain.BackendRef{Name: backendName}, Enabled: true,
		}}},
		duties: &fakeDutyLister{duties: []*domain.Duty{{
			ID: dutyID, Name: "sp3-duty", Role: "dev", Description: "d",
			Prompt: "Review MR !{{.Event.mr_iid}} by {{.Event.author}}",
		}}},
		buildExecutor: func(_ *config.Config, _ *config.Backend) (executor.Executor, error) {
			return fakeExec, nil
		},
	}

	// --- real eventing core over MemStore ---
	store := events.NewMemStore()
	dispatcher := events.NewDispatcher(store, &staticAssignmentLister{list: []*domain.Assignment{assignment}}, inv, 2, 50*time.Millisecond)
	ingestor := events.NewIngestor(store, dispatcher.Notify)
	go dispatcher.Run(ctx)

	// --- real webhook server ---
	httpSrv := httptest.NewServer(server.New(ingestor).Handler())
	defer httpSrv.Close()

	req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/webhooks/gitlab", strings.NewReader(integrationWebhookFixture))
	req.Header.Set("X-Gitlab-Token", "integration-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook status = %d", resp.StatusCode)
	}

	// --- wait for the run ---
	waitForCondition(t, 3*time.Second, func() bool {
		return len(rr.runs) >= 1
	}, "no run recorded")

	var run *domain.Run
	for _, r := range rr.runs {
		run = r
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("run status = %q", run.Status)
	}
	if run.TriggerKind != "event-subscription" {
		t.Errorf("trigger kind = %q", run.TriggerKind)
	}
	if run.EventID == nil {
		t.Fatal("run.EventID not stamped")
	}
	if !strings.Contains(run.RenderedPrompt, "!7") || !strings.Contains(run.RenderedPrompt, "alice") {
		t.Errorf("rendered prompt = %q, want event fields", run.RenderedPrompt)
	}
	if recorder.params["body"] != "auto-reviewed" || recorder.params["mr"] != "7" {
		t.Errorf("delivered params = %v", recorder.params)
	}

	// --- event marked dispatched ---
	evID := uuid.MustParse(*run.EventID)
	waitForCondition(t, 2*time.Second, func() bool {
		ev, err := store.GetByID(ctx, evID)
		return err == nil && ev.Status == domain.EventStatusDispatched
	}, "event not marked dispatched")

	// --- duplicate webhook: same MR+SHA -> zero new events ---
	req2, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/webhooks/gitlab", strings.NewReader(integrationWebhookFixture))
	req2.Header.Set("X-Gitlab-Token", "integration-secret")
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("duplicate webhook status = %d", resp2.StatusCode)
	}

	// --- replay: re-queue and expect a dedup-SKIPPED second run ---
	if err := store.MarkPending(ctx, evID); err != nil {
		t.Fatal(err)
	}
	dispatcher.Notify(evID)
	waitForCondition(t, 3*time.Second, func() bool {
		return len(rr.runs) >= 2
	}, "replay produced no second run")
	skipped := 0
	for _, r := range rr.runs {
		if r.Status == domain.RunStatusSkipped {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("skipped runs = %d, want exactly 1 (replay dedup)", skipped)
	}
}

type staticAssignmentLister struct{ list []*domain.Assignment }

func (s *staticAssignmentLister) List(_ context.Context) ([]*domain.Assignment, error) {
	return s.list, nil
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(timeout)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal(msg)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
```

NOTE 1: `deliveryRecorder` already exists in `internal/run/pipeline_endpoint_test.go` (same package) — do NOT redefine it; reuse.
NOTE 2: `fakeRunRepo.runs` is accessed from the test goroutine while the dispatcher's worker writes runs — `fakeRunRepo` methods have no mutex. Check whether `newFakeRunRepo` is already mutex-guarded; if not, add a `sync.Mutex` to `fakeRunRepo` (lock in Insert/UpdateStatus/UpdateResult and add a `snapshot()` helper returning a copy for assertions) as part of this task — run the whole package with `-race` to prove it.
NOTE 3: the duty prompt renders `{{.Event.author}}` — that exists because the dispatcher merges `payload_norm` (which has `author`) into EventParams.

- [x] **Step 2: Run it (with race detector)**

Run: `go test ./internal/run/ -run TestEventVertical -race -v -count=1`
Expected: PASS. If the race detector fires on fakeRunRepo, apply NOTE 2's mutex and re-run.

Then: `go test ./internal/run/ -race -count=1` (whole package, race-clean).

- [x] **Step 3: Commit**

```bash
git add internal/run/event_integration_test.go internal/run/pipeline_test.go
git commit -m "test(sp3): full webhook-to-run vertical integration test"
```

(Include pipeline_test.go only if NOTE 2 required the mutex change.)

---

### Task 13: Final verification & wrap-up

**Files:** none new.

- [x] **Step 1: Full suite**

```bash
gofmt -l . && go vet ./... && go test ./... -count=1 && go test ./internal/... -race -count=1
```
Expected: gofmt prints nothing; vet clean; ALL packages pass, race-clean.

- [x] **Step 2: SP1/SP2 stability check**

```bash
git diff cafd2b3 --stat -- internal/executor/ internal/agentloop/ internal/outputs/ internal/prompt/ internal/state/ internal/trigger/
```
Expected: NO changes to SP1/SP2 packages other than those this plan specifies (none of these dirs should appear at all).

- [x] **Step 3: CLI smoke**

```bash
FLEET_DATABASE_DSN=postgres://localhost/x go run ./cmd/fleet --config configs/fleet.yaml config validate
go run ./cmd/fleet --config configs/fleet.yaml backends list
```
Expected: OK; backends list renders.

If a local Postgres is available (optional but recommended):
```bash
createdb fleet_sp3_smoke 2>/dev/null || true
FLEET_DATABASE_DSN=postgres://localhost/fleet_sp3_smoke go run ./cmd/fleet --config configs/fleet.yaml migrate
FLEET_DATABASE_DSN=postgres://localhost/fleet_sp3_smoke go run ./cmd/fleet --config configs/fleet.yaml events list
# expect "(no events)"; proves migration 004 applied and the events CLI works
```

- [x] **Step 4: Push**

```bash
git status --short   # expect clean
git push origin master
```

---

## Acceptance criteria traceability (spec §10)

| Spec AC | Covered by |
|---|---|
| 1. Migration + event-level dedup | Task 1 (schema), Task 2 (ON CONFLICT), Task 6 (MemStore mirror + ingest dedup test), Task 13 optional migrate smoke |
| 2. Webhook → normalized → persisted → dispatched → Run with EventID/prompts/outputs; bad token 401 | Task 8 (handler tests), Task 9 (status codes), Task 12 (vertical) |
| 3. Poll parity + cursor + webhook/poll overlap one row | Task 8 (parity + cursor tests), Task 6 (ingest dedup), Task 12 (duplicate webhook) |
| 4. Filter semantics; zero-match → dispatched | Task 3 (matcher table), Task 6 (zero-match test) |
| 5. At-least-once: startup pending dispatched; redelivery → skipped run | Task 6 (rescan test), Task 12 (replay → skip) |
| 6. serve runs all four concerns; events list/replay; schedule unchanged | Task 11 (wiring + smoke), Task 13 |
| 7. Config validation (serve block, filter requirements, sample validates) | Task 10 (tests), Task 11 (sample + validate smoke) |
| 8. SP1/SP2 tests pass; fmt/vet clean | Task 13 |
