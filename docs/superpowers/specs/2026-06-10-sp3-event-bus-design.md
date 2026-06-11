# SP3 — Event Bus & Dispatcher — Design Spec

**Status:** Draft for review
**Date:** 2026-06-10
**Author:** brainstormed with Claude Code
**Parent:** `spec.md` §7 (Plugin framework), §8 (Triggers), §10 (Event flow & the bus), §13 (SP3
entry). SP1 (core engine) and SP2 (endpoint backends + agent loop) are complete; this spec makes
OfficeFleet **promptless**: events arrive from integrations and fire runs without a human in the
loop.

---

## 1. Summary

SP3 implements the eventing core: a normalized **event envelope** persisted to a Postgres
`events` table, **ingestion** via plugin webhooks (push) and plugin polling, an in-process
**bus**, and a **dispatcher** that matches events against `event-subscription` assignments and
executes them through the existing run pipeline with a bounded worker pool. Delivery is
**at-least-once** over a durable status column; the platform's two-level dedup (event-level
unique index + the per-assignment dedup already in the pipeline) makes redelivery safe and
auditable. One new daemon, **`fleet serve`**, hosts everything long-lived: webhook listener,
poll loops, dispatcher, and the existing cron scheduler.

The vertical is proven end-to-end by wiring the **existing GitLab plugin's `mr_events` source**
with BOTH ingestion surfaces (webhook + poll). Plugin breadth (Slack, Discord, GitHub, Email)
is deliberately split out to a follow-up **SP3b** spec against a then-stable framework.

### Decisions locked during brainstorming

1. **Scope: core + GitLab events.** The full eventing vertical, proven by the GitLab plugin's
   `mr_events` (webhook + poll). Slack/Discord/GitHub/Email plugins move to SP3b.
2. **One `fleet serve` daemon.** Cron scheduler + webhook HTTP server + poll loops + bus +
   dispatcher in one process. `fleet schedule` remains as a cron-only alias, marked deprecated
   in its help text. SP4 later mounts the REST API/UI into the same process (single-binary
   architecture, spec.md §5).
3. **At-least-once delivery via Postgres status.** Events carry `status: pending | dispatched`.
   The in-process channel is a low-latency wakeup over the durable table; a rescan loop redrives
   `pending` events on startup and on an interval. Replay = flip status back to `pending`.
4. **`continuous` trigger: OUT.** This resolves spec.md's internal conflict (§13 lists it in
   SP3; §8 says "Later"; §15 defers its supervision design post-SP3). SP3 ships
   `event-subscription` only; `continuous` is designed when something needs it.
5. **GitLab implements both webhook and poll**, normalizing identically — one plugin proves both
   framework surfaces, and poll makes dogfooding work from behind NAT against gitlab.com.

---

## 2. Goals & non-goals

### Goals
- The §10 event envelope, durable `events` table, and event repo.
- Additive plugin capability interfaces for ingestion: `WebhookSource` (push) and `PollSource`
  (poll), with poll cursors persisted per plugin.
- An in-process bus + dispatcher: filter matching, per-assignment fan-out through the existing
  pipeline, bounded concurrency, mark-after-attempt status transitions, startup/interval rescan.
- The `event-subscription` trigger kind, configured entirely through the existing
  `TriggerConfig.Filter` map (no domain-type changes).
- GitLab `mr_events`: authenticated webhook handler + cursor-based poll, shared normalization.
- `fleet serve` daemon; `fleet events list` / `fleet events replay` CLI.
- `Run.EventID` finally populated (the SP1 field exists but was never set), linking every
  event-triggered Run to its raw event for replay/observability (§10).

### Non-goals (SP3)
- Slack / Discord / GitHub / Email plugins (SP3b).
- The `continuous` trigger (post-SP3; decision #4).
- Rich filter expressions (globs, regex, comparisons) — exact match only; deferred.
- Event retention/pruning policy (an SP4 settings concern).
- Cross-process replay signaling (the CLI flips status; the daemon's rescan picks it up within
  one interval — documented behavior, fine at SP3 scale).
- The REST API / UI surfaces over events (SP4); webhooks + `/healthz` are the only HTTP routes.
- Multi-instance `fleet serve` coordination (single daemon assumed, per spec.md §2 non-goals).

---

## 3. Event domain & schema

### 3.1 Envelope (`internal/domain`)

```go
// Event is the normalized envelope for one inbound occurrence from a plugin
// event source (spec.md §10).
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

type EventStatus string

const (
	EventStatusPending    EventStatus = "pending"
	EventStatusDispatched EventStatus = "dispatched"
)
```

### 3.2 Migration `004_events.sql`

- `events` — id (uuid pk), source_plugin, event_type, payload_raw (jsonb), payload_norm (jsonb),
  identity, dedup_key, status (default `'pending'`), received_at (default now()),
  dispatched_at (nullable).
  - **Unique index on `(source_plugin, dedup_key)`** — event-level dedup. The same MR at the
    same SHA arriving via both webhook and poll inserts once (`ON CONFLICT DO NOTHING`).
  - Index on `(status)` (rescan scans pending); index on `(source_plugin, event_type)`.
- `poll_cursors` — plugin (pk), cursor (text), updated_at. Cursors are **plugin-level**, not
  assignment-level, so `assignment_state` is deliberately not reused.

### 3.3 Two-level dedup (the at-least-once safety argument)

1. **Event level:** the unique index collapses duplicate *arrivals* (webhook + poll overlap,
   webhook retries) into one row.
2. **Assignment level:** the pipeline's existing dedup (`deriveDedupKey` reads the `dedup_key`
   event param; `state.HasProcessed` per assignment) turns duplicate *dispatches* (crash
   redelivery, operator replay) into recorded **skipped** runs — never duplicate outputs.

Both layers exist independently; at-least-once delivery is safe because layer 2 is idempotent.

---

## 4. Plugin capability interfaces (additive)

The 5-method `plugin.Plugin` interface is **unchanged**. Plugins opt into ingestion by
implementing optional interfaces (Go interface-upgrade idiom), discovered with type assertions:

```go
// package plugin (imports domain — domain stays pure)

// WebhookSource is implemented by plugins that accept push ingestion.
// HandleWebhook authenticates and parses one inbound HTTP request and returns
// normalized events. The PLATFORM persists them and writes the HTTP response:
// a returned error of type *AuthError -> 401; any other error -> 400; panics
// and storage failures -> 500.
type WebhookSource interface {
	HandleWebhook(ctx context.Context, r *http.Request) ([]domain.Event, error)
}

// AuthError marks a webhook authentication failure (-> 401).
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return e.Msg }

// PollSource is implemented by plugins that support interval polling.
// Poll returns events newer than cursor plus the new cursor. An empty cursor
// means "first poll"; the plugin decides its own cursor encoding.
type PollSource interface {
	Poll(ctx context.Context, cursor string) ([]domain.Event, string, error)
}
```

This deviates from §7's "plugin provides an `http.Handler`" sketch deliberately: the platform
owning persistence and response codes keeps plugins pure parsers/validators, testable with
`httptest.NewRequest` and no server. Plugins fill `SourcePlugin`, `EventType`, `PayloadRaw`,
`PayloadNorm`, `Identity`, `DedupKey`; the platform assigns `ID`, `Status`, `ReceivedAt`.

---

## 5. GitLab `mr_events` source

### 5.1 Webhook (push)

- Validates `X-Gitlab-Token` against the secret `gitlab_webhook_secret` (constant-time compare);
  mismatch/missing → `*plugin.AuthError` → 401.
- Accepts `object_kind: "merge_request"` payloads; other kinds return zero events (200, ignored).
- `object_attributes.action` maps to the event type:
  `open` → `mr_opened`, `update` → `mr_updated`, `merge` → `mr_merged`, `close` → `mr_closed`.
  Unrecognized actions are ignored (zero events).

### 5.2 Poll

- Plugin config gains `poll_projects: []string` (e.g. `["myorg/myrepo"]`) and
  `poll_interval` (Go duration string, default `60s`; the serve daemon reads it).
- Each cycle, per project: `GET /api/v4/projects/{id}/merge_requests?state=opened&updated_after={cursor}&order_by=updated_at&sort=asc`.
- Cursor = the max `updated_at` seen across the cycle, RFC3339, single cursor for all
  configured projects. The cursor advances **only when every project in the cycle polled
  successfully** — a partial failure leaves it unchanged so the failed project's events aren't
  skipped (re-polling the successful projects is harmless: the event-level dedup index collapses
  the duplicates). First poll (empty cursor) uses `now - poll_interval` to avoid flooding
  history.
- Poll-discovered MRs emit `mr_updated` (poll cannot reliably distinguish "opened" — recorded
  as a known, accepted simplification; the dedup key makes webhook/poll overlap harmless).

### 5.3 Shared normalization

Both paths produce identical envelopes:

```
payload_norm = {
  project: "myorg/myrepo", mr_iid: 42, title: "...", action: "open",
  source_branch: "...", target_branch: "...", last_commit_sha: "abc123",
  author: "username", url: "https://gitlab.com/.../merge_requests/42",
}
identity  = author username
dedup_key = "mr:{project}:{mr_iid}:{last_commit_sha}"
```

The dedup key changes only when the MR head SHA changes — matching the SP1 mr-reviewer
convention, so a re-pushed MR re-fires review and an untouched MR never does. `mr_iid` sits in
`payload_norm` exactly where the existing duty prompt template (`{{.Event.mr_iid}}`) expects it.

---

## 6. Bus, dispatcher & matching

### 6.1 Trigger matching (`event-subscription`)

No domain-type changes — `TriggerConfig.Filter map[string]any` carries everything:

```yaml
trigger:
  kind: event-subscription
  filter:
    source: gitlab          # REQUIRED — matches Event.SourcePlugin
    event_type: mr_opened   # REQUIRED — matches Event.EventType
    project: myorg/myrepo   # any further keys: exact match vs payload_norm
```

- Config validation (extending `config.Validate`): an `event-subscription` trigger must have
  non-empty `source` and `event_type` filter values, and the duty's `trigger_kinds` must include
  `event-subscription`.
- Extra filter keys compare as strings (`fmt.Sprint(filterVal) == fmt.Sprint(normVal)`) against
  **top-level** `payload_norm` fields. A filter key absent from `payload_norm` is a non-match.
- No globs/regex/comparisons in SP3 (deferred, §11).

### 6.2 Dispatcher (`internal/events`)

- **Bus:** a buffered channel of event IDs (capacity 256) with **non-blocking send**. The table
  is the source of truth; a full channel just means the rescan loop delivers instead.
- **Dispatching one event:**
  1. Load the event; skip unless `status = pending`.
  2. Load enabled assignments whose trigger kind is `event-subscription` (with their agent and
     duty), evaluate filters per §6.1.
  3. For each match, submit to a **bounded worker pool** (`serve.workers`, default 4) that calls
     the existing `run.Pipeline.Execute` with:
     - `TriggerKind: "event-subscription"`,
     - `EventID` (new, see §6.3),
     - `EventParams` = `payload_norm` **merged with reserved meta keys** `source`, `event_type`,
       `identity`, `dedup_key`, `event_id` (meta keys win on collision). The pipeline's existing
       `deriveDedupKey` picks up `dedup_key`; the pause gate and per-assignment dedup apply
       unchanged.
  4. After **all matched runs have been attempted** (success, failure, or skip), set
     `status = dispatched`, `dispatched_at = now()`. Zero matches → still `dispatched`
     (auditable as "nothing cared"). A crash before the mark leaves `pending` → redelivered on
     restart → layer-2 dedup records skips.
- **Rescan loop:** on startup and every `serve.rescan_interval` (default 30s), scan `pending`
  events (oldest first) and dispatch them. This is simultaneously crash recovery, channel-
  overflow catch-up, and the replay pickup mechanism.
- Run executors are built per assignment via the existing `executor.FromBackend` resolution
  (same as the cron scheduler path).

### 6.3 Pipeline touch (additive)

`run.ExecuteRequest` gains `EventID *string`; `Execute` stamps it onto the Run (including the
pause-gate and dedup-skip record paths). `domain.Run.EventID` has existed since SP1 but was
never populated — SP3 closes that loop, enabling §10's "show the raw event behind any Run".
No other pipeline behavior changes.

---

## 7. The serve daemon & HTTP ingestion

### 7.1 `internal/server` (webhook listener)

- `POST /webhooks/{plugin}` — look up the registered plugin; it must implement `WebhookSource`
  (else 404). Call `HandleWebhook`; persist returned events (`ON CONFLICT DO NOTHING`); nudge
  the bus for each newly inserted event; respond `202 {"accepted": <newly inserted>}`.
  Auth errors → 401; parse errors → 400; storage errors → 500.
- `GET /healthz` → `200 ok`.
- Nothing else — SP4 mounts the REST API/UI into this same server later.

### 7.2 `fleet serve`

Startup order: load **validated** config → DB pool → init plugins → start in this order:
1. dispatcher (workers + rescan; rescan runs once immediately — crash recovery before new
   ingestion),
2. poll loops (one goroutine per `PollSource` plugin, at its `poll_interval`; cursor loaded
   from / saved to `poll_cursors`; poll errors are logged and retried next interval — they
   never crash the daemon),
3. webhook HTTP server on `serve.addr`,
4. cron scheduler — the existing logic **extracted from `scheduleCmd` into a shared function**
   so `fleet schedule` (now deprecated-in-help, cron-only) and `fleet serve` share one
   implementation.

Graceful shutdown: signal-cancelled context stops pollers/scheduler/workers and
`http.Server.Shutdown` drains the listener. In-flight runs complete (matching the pause
semantics' "in-flight runs complete normally" philosophy); an event whose runs were cut off
mid-flight remains `pending` and redelivers safely on restart.

### 7.3 Config (`fleet.yaml`)

```yaml
serve:
  addr: ":8080"           # webhook listener bind address (default ":8080")
  workers: 4              # dispatcher worker pool size (default 4)
  rescan_interval: 30s    # pending-event rescan period (default 30s)

plugins:
  - name: gitlab
    config:
      base_url: https://gitlab.com
      poll_interval: 60s
      poll_projects: ["myorg/myrepo"]
```

Validation: `workers > 0` if set; `rescan_interval`/`poll_interval` parse as Go durations.
The sample `fleet.yaml` gains an event-subscription assignment (`dev-1 × mr-reviewer`, filter
on `source: gitlab, event_type: mr_opened`, project-scoped) and `mr-reviewer.trigger_kinds`
gains `event-subscription`.

### 7.4 CLI

```
fleet serve                                   # the daemon (webhooks+poll+dispatch+cron)
fleet events list [--status s] [--limit n]    # newest first; default limit 50
fleet events replay <event-id>                # status -> pending; daemon rescan picks it up
fleet schedule                                # unchanged behavior; help text marks it deprecated
```

`events replay` on an already-pending event is a no-op (prints a notice). Replay produces
dedup-skip runs unless per-assignment state was cleared — that is the *correct* audit-visible
behavior; clearing state for a forced re-run is an operator action out of SP3 scope.

---

## 8. Error handling summary

| Failure | Handling |
|---|---|
| Webhook bad token | 401, nothing persisted |
| Webhook unparseable/wrong kind | 400 (parse error) / 202 with zero events (ignored kind) |
| Event insert conflict (duplicate dedup_key) | Silently collapsed; not re-dispatched; 202 counts only new rows |
| Poll error (network, 5xx, bad cursor) | Logged, cursor unchanged, retry next interval |
| Crash between persist and dispatch | Event stays `pending`; startup rescan redelivers |
| Crash mid-dispatch (some runs done) | Stays `pending`; redelivery → done assignments record dedup-skips, missed ones run |
| Run failure for one matched assignment | Recorded failed (existing pipeline semantics); does NOT block other matches or the dispatched mark |
| Channel full | Non-blocking send drops the nudge; rescan delivers |
| Dispatcher DB errors | Logged; event left `pending`; rescan retries |

---

## 9. Testing strategy

- **Unit — GitLab webhook:** auth reject (401-class error), wrong-kind ignored, action → type
  mapping for all four actions, normalization golden (payload_norm/identity/dedup_key) from a
  real GitLab webhook fixture, via `httptest.NewRequest` (no server).
- **Unit — GitLab poll:** stubbed GitLab API (`httptest.Server`): normalization parity with the
  webhook golden, cursor advance, empty-cursor first poll, API-error leaves cursor unchanged.
- **Unit — matching:** table tests for §6.1 (required keys, extras exact-match, absent-key
  non-match, string coercion of numbers).
- **Unit — dispatcher:** fake pipeline + in-memory repos: match → run with correct
  EventParams/EventID, zero-match → dispatched, mark-after-attempt ordering, one failing run
  doesn't block siblings or the mark, worker-pool bounding, non-blocking bus send, rescan
  delivers pending + replayed events.
- **Unit — event repo:** insert, conflict-dedup returns "not new", status transitions, pending
  scan ordering. Against the fake-repo pattern; SQL exercised by migration smoke.
- **Unit — config:** new validation rules (filter requirements, serve block, durations).
- **Integration:** full vertical with a real `internal/server` + real dispatcher + fake
  executor + mem store: POST GitLab fixture → 202 → event row → matched run recorded
  (EventID, rendered prompts, outputs delivered via recorder plugin) → event `dispatched`;
  then replay → dedup-skip run recorded.
- **Manual dogfood:** `fleet serve` against a real GitLab project (webhook where reachable,
  poll otherwise) — the long-deferred live validation pass.

---

## 10. Acceptance criteria

1. `fleet migrate` creates `events` and `poll_cursors`; inserting two events with the same
   `(source_plugin, dedup_key)` stores one row.
2. A GitLab MR webhook POST with a valid token is normalized, persisted, and dispatched to a
   matching `event-subscription` assignment; the recorded Run carries `EventID`, both rendered
   prompts, and delivered outputs. An invalid token gets 401 and persists nothing.
3. Poll against a stubbed GitLab API produces envelopes identical to the webhook's for the same
   MR state, advances its cursor, and overlapping webhook+poll arrivals yield one event row.
4. Filter matching enforces `source` + `event_type` and exact-matches extra keys; a non-matching
   event ends `dispatched` with zero runs.
5. At-least-once holds: a `pending` event present at daemon startup is dispatched; redelivering
   an event whose assignment already processed its dedup_key records a **skipped** run and
   delivers no outputs.
6. `fleet serve` runs webhook listener, poll loops, dispatcher, and cron scheduler concurrently
   and shuts down gracefully on SIGINT; `fleet events list`/`replay` behave per §7.4;
   `fleet schedule` behavior is unchanged.
7. Config validation accepts the new `serve` block and event-subscription triggers (rejecting
   missing source/event_type) and the sample fleet.yaml validates.
8. All SP1/SP2 tests pass unchanged; `gofmt`/`go vet` clean.

---

## 11. Open questions / deferred

- **Plugin breadth (SP3b):** Slack, Discord, GitHub, Email — each its own event sources +
  actions against this framework.
- **Richer filters** — globs/regex/numeric comparisons on `payload_norm`; exact-match only for
  now.
- **Event retention/pruning** — unbounded table growth is acceptable for SP3; an SP4 settings
  concern.
- **Cross-process replay nudge** — `events replay` relies on the rescan interval; a
  LISTEN/NOTIFY wakeup is a later nicety.
- **`continuous` trigger** — deferred post-SP3 (decision #4; resolves spec.md §13 vs §8).
- **Poll `mr_opened` fidelity** — poll emits `mr_updated` only; if "opened-only" duties prove
  common, the poller could diff `created_at` against the cursor window.
- **Multi-instance serve** — single daemon assumed; leader election / shared-queue semantics
  are far-future.
