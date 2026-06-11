# SP5 — Sample Duties / huginn parity — Design Spec

**Status:** Draft for review
**Date:** 2026-06-11
**Author:** brainstormed with Claude Code
**Parent:** `spec.md` §12 (SP5: port code-review, code-audit, MR-feedback as Duties), §11/§15
(the inline-comment seam — **resolved here**); lineage: the `huginn-agent` codebase
(`~/workshop/huginn-agent`), whose behaviors these duties port at the prompt level.

---

## 1. Summary

SP5 makes OfficeFleet do huginn's job: three sample duties — **mr-review**, **code-audit**,
**mr-feedback** — assigned to a "developer" agent in the sample config. The duties are prompts +
trigger configs + output bindings; the platform machinery they need is small and generic:

1. **Output fan-out** (`for_each` on an output binding) — resolves the §11 inline-comment seam
   with the structured-output path: the LLM returns a list in its structured result; the platform
   delivers a plugin action once per element, recording one `OutputDelivery` per item.
2. **Three real GitLab actions** — `post_inline_comment`, `create_issue` (currently a stub),
   `reply_to_discussion`.
3. **One new event type** — `mr_note` (GitLab Note Hook on merge requests), with bot-loop
   protection at ingestion.

### Decisions locked during brainstorming

1. **Scope: all three duties** (full huginn parity per spec.md §12).
2. **Inline-comment seam → structured-output fan-out.** The platform posts each comment/issue
   from the LLM's structured result. Declarative, governed, per-item audit trail; identical for
   CLI and endpoint backends. (Rejected: sanctioned tool / direct `glab` posting — bypasses
   output governance and puts write credentials inside the run.)
3. **Code-audit is a single-pass duty.** Huginn's verifier/false-positive/scan-ledger machinery
   is ported as *prompt discipline*, not platform machinery. Repeat-finding dedup is prompt-driven:
   the run lists existing open issues (via `glab` in the workspace) before reporting findings.
4. **Fan-out lives in the delivery layer** (`outputs.Deliver` + `OutputBinding.for_each`), not in
   per-plugin batch actions — one mechanism, every plugin benefits, `outputs_delivered` stays
   per-item truthful.
5. **Repo access stays prompt-level**: duty prompts clone over HTTPS with
   `{{secret "gitlab_token"}}` rendered into the URL — the existing SP1 mechanism. (The
   viewer-readable-secrets caveat is already recorded for the audit-log slice.)

---

## 2. Goals & non-goals

### Goals
- `for_each` fan-out in `internal/outputs` + `Item` on `prompt.Context` + config validation.
- GitLab: `post_inline_comment` (with plain-note fallback), `create_issue`, `reply_to_discussion`;
  `mr_note` webhook events with `bot_username` loop protection.
- Three sample duties + a `developer` agent + three assignments in `configs/fleet.yaml`, with
  prompts that port huginn's review/audit/feedback guidance.
- Tests for all of the above (fakes/httptest, no Postgres, no live GitLab).

### Non-goals (SP5)
- Dogfood validation against a live GitLab (explicitly deferred by the user; the deferred
  dogfood pass now covers it).
- Note **polling** (webhook-only for `mr_note`; poll parity recorded as deferred).
- Run-chaining / two-stage verifier machinery; platform-side finding-fingerprint state.
- Resolving discussions (`resolve_discussion` stays a stub; replies are the parity behavior).
- GitHub/Slack variants of these duties.
- Conditional outputs (deliver-only-if); fan-out covers SP5's needs.

---

## 3. Output fan-out (`for_each`)

`domain.OutputBinding` gains one optional field:

```go
type OutputBinding struct {
	Plugin  string         `json:"plugin"`
	Action  string         `json:"action"`
	Params  map[string]any `json:"params"`
	ForEach string         `json:"for_each,omitempty"` // key into LLMResult.Output; value must be a JSON array
}
```

`outputs.Deliver` semantics when `for_each` is set:

- `result.Output[ForEach]` missing or `nil` or `[]` → **zero deliveries** (a healthy
  "no findings" run; nothing recorded for that binding).
- Value is not a JSON array → **one failed delivery** (`for_each key %q is not an array`).
- Array → for each element: render params with the element exposed as `{{.Item.*}}` (elements
  must be JSON objects; a non-object element yields one failed delivery for that item, the rest
  continue) and call `plugin.Do` once. Each item yields its own `OutputDelivery`; item failures
  don't abort the remaining items (same never-abort contract as today).
- **Safety cap:** at most **50 items** per fan-out binding. Items beyond the cap are recorded as
  one failed delivery (`for_each list truncated: 73 items exceeds the cap of 50`) so a
  hallucinating model cannot file thousands of issues, and the truncation is visible in the run
  record rather than silent.

`prompt.Context` gains `Item map[string]any`. Non-fan-out bindings leave it nil; param templates
in fan-out bindings reference `{{.Item.path}}`, `{{.Item.line}}`, etc. (rendered with the same
engine and `{{secret ...}}` helpers as today).

Config validation (`internal/config`): `for_each`, when set, must be a non-empty string; sample
validation parity with the API's duty/assignment validation (the field flows through YAML seed,
API CRUD, and the SPA's assignment JSON unchanged — it is part of `OutputBinding`, which all
three already serialize).

## 4. GitLab plugin additions

### Actions

| Action | Params | Behavior |
|---|---|---|
| `post_inline_comment` | `project`, `mr_iid`, `path`, `line`, `body` | GET `merge_requests/:iid/versions` (latest `head/base/start` SHAs) → POST `discussions` with a text position on `new_path`/`new_line`. **Fallback:** if GitLab rejects the position (400/422 — stale line numbers are routine), post a plain MR note prefixed `**{path}:{line}** — ` instead; the delivery result records `{"fallback": "note"}`. Only an error when both attempts fail. |
| `create_issue` | `project`, `title`, `description`, `labels` (optional comma-separated string) | POST `projects/:id/issues`. Replaces the SP3 stub. |
| `reply_to_discussion` | `project`, `mr_iid`, `discussion_id`, `body` | POST `merge_requests/:iid/discussions/:discussion_id/notes`. |

All share the existing client conventions: `PRIVATE-TOKEN` auth, literal-`/`→`%2F` project
encoding, non-2xx → error with status + body, `paramToString` coercion (line numbers arrive as
strings or floats from JSON templates).

### `mr_note` events

The webhook handler (existing `X-Gitlab-Token` check) accepts Note Hook payloads
(`object_kind: "note"`, `object_attributes.noteable_type: "MergeRequest"`); other noteable types
are ignored. Normalized envelope:

- `event_type`: `mr_note`
- `payload_norm`: `project` (path with namespace), `mr_iid`, `mr_title`, `note_id`,
  `discussion_id`, `note_body`, `author` (username), `mr_author`, `mr_source_branch`, `url`
- `identity`: note author username
- `dedup_key`: `note:{project}:{note_id}`

**Loop protection (ingestion-level):** plugin config gains `bot_username` (string, optional).
Notes authored by `bot_username` are dropped in `HandleWebhook` before ingestion — the
mr-feedback duty can never be triggered by its own replies. (Defense in depth: the sample
prompt also instructs the agent never to reply to its own comments.) The `mr_events` poll
source is unchanged; `mr_note` is webhook-only.

`EventSources()` gains `{Name: "mr_notes", Description: "Merge request comment (note) events (webhook only)"}`.

## 5. The three sample duties (`configs/fleet.yaml`)

A new `developer` agent (claude backend, persona: senior engineer who reviews like huginn:
specific, severity-tagged, no nitpicks) plus three duties and three assignments. Prompts are Go
templates; what follows fixes their *contract* — full text lands in the plan.

### 5.1 `mr-review`

- **Trigger:** `event-subscription`, filter `{source: gitlab, event_type: mr_opened}` (one
  assignment; a second assignment may subscribe to `mr_updated` — the sample ships `mr_opened`
  only to keep noise down, with `mr_updated` shown commented).
- **Prompt contract:** clone the repo (token-rendered URL), check out the MR source branch, diff
  against the target branch; review with huginn's discipline (real bugs/security/correctness over
  style; cite file+line; severity high/medium/low; ≤ 10 comments; self-check each finding against
  the actual code before reporting). Structured result:
  `output.comments: [{path, line, severity, body}]` + `output.verdict` (one-paragraph summary).
- **Outputs:**
  1. fan-out `post_inline_comment` over `comments`
  2. `post_mr_comment` with the verdict summary (always posted — a review with zero inline
     comments still reports "reviewed, looks good").
- **Dedup:** existing event-level + per-assignment dedup (`mr:{project}:{iid}:{sha}` from SP3)
  already prevents re-review of the same head SHA.

### 5.2 `code-audit`

- **Trigger:** `cron` (sample: weekly, `0 6 * * 1`).
- **Assignment config:** `{project: <path>, category: <security|correctness|performance>}` —
  the duty's `config_schema` declares both.
- **Prompt contract:** clone default branch; `glab issue list --label code-audit` (or API via
  `curl` — prompt prefers `glab`) to load existing open findings; audit the configured category
  with huginn's verifier discipline as instructions (verify each candidate against the code,
  drop speculative findings, no duplicates of existing issues); structured result:
  `output.issues: [{title, description, labels}]` where `labels` always includes `code-audit`,
  and `description` carries file refs + evidence + suggested fix.
- **Outputs:** fan-out `create_issue` over `issues`.

### 5.3 `mr-feedback`

- **Trigger:** `event-subscription`, filter `{source: gitlab, event_type: mr_note}`.
- **Prompt contract:** read `{{.Event.note_body}}` in the context of the MR (clone + checkout
  when the question needs code context; skip the clone for purely conversational replies);
  reply helpfully and concretely; if the comment asks for a change, describe the change (SP5
  does not push commits — recorded as deferred). Structured result:
  `output.replies: [{body}]` — a list of **zero or one** element. An empty list means
  "no reply warranted" (bot chatter, thanks, etc.) with status 0.
- **Outputs:** one fan-out binding, `reply_to_discussion` over `replies`
  (`discussion_id: {{.Event.discussion_id}}`, `body: {{.Item.body}}`). The empty list yields
  zero deliveries — fan-out's empty-list semantics double as the "reply only when warranted"
  conditional, so no conditional-output mechanism is needed.
- **Loop protection:** ingestion-level `bot_username` drop (§4) plus prompt instruction.

## 6. Error handling

| Failure | Behavior |
|---|---|
| `for_each` key missing/empty list | zero deliveries for that binding (healthy) |
| `for_each` value not an array | one failed delivery, other bindings unaffected |
| fan-out item not an object / render error | failed delivery for that item; remaining items continue |
| > 50 fan-out items | first 50 delivered, one failed delivery records the truncation |
| inline position rejected (400/422) | fallback to plain MR note with `path:line` prefix; result notes the fallback |
| both inline + fallback fail | failed delivery (recorded; run still completes) |
| note webhook from `bot_username` | dropped at ingestion (no event row) |
| non-MR note webhook | ignored (200, zero events) |
| clone/auth failure inside the run | LLM reports failure → `submit_result` status ≠ 0 → run failed, outputs skipped (existing SP2 contract) |

## 7. Testing strategy

- **outputs (fan-out):** table tests — empty/missing key, non-array, happy N-item delivery with
  `{{.Item.*}}` rendering, per-item failure isolation, non-object item, 50-item cap + truncation
  record, `Item` absent for normal bindings.
- **gitlab actions:** httptest — `post_inline_comment` happy path (versions fetch + discussion
  POST shape), fallback on 400 (records fallback), both-fail error; `create_issue` shape +
  labels; `reply_to_discussion` shape; param coercion (float64 line).
- **gitlab mr_note:** webhook fixture tests — note on MR → normalized envelope + dedup key;
  note by `bot_username` → dropped; note on Issue/Commit/Snippet → ignored; missing token → 401
  (existing).
- **config:** `for_each` validation; the sample `configs/fleet.yaml` parses + validates + seeds
  (extend the existing sample-config test).
- **prompt contract sanity:** the three duty prompts in the sample config render against a
  synthetic `prompt.Context` without template errors (catches `{{.Event.x}}` typos at test time).
- **No live GitLab / no Postgres** (project test conventions). Live behavior belongs to the
  deferred dogfood pass.

## 8. Acceptance criteria

1. A fan-out binding delivers one recorded `OutputDelivery` per list item, renders `{{.Item.*}}`
   params, caps at 50 with a visible truncation record, and treats an empty/missing list as zero
   deliveries (all proven by `internal/outputs` tests).
2. `post_inline_comment` posts a positioned diff discussion and falls back to a prefixed plain
   note when GitLab rejects the position; `create_issue` (no longer a stub) and
   `reply_to_discussion` work; all proven against httptest fakes.
3. An `mr_note` webhook produces a normalized event with dedup key `note:{project}:{note_id}`;
   the bot's own notes never become events (`bot_username` config).
4. `configs/fleet.yaml` defines the `developer` agent, three duties, and three assignments;
   it parses, validates, and seeds; the three prompts render cleanly against a synthetic context.
5. The mr-feedback contract: an empty `replies` list yields zero deliveries (no reply posted);
   a one-element list posts exactly one discussion reply.
6. Full suite green; gofmt/vet clean; zero new Go deps; `make test` passes end-to-end.

## 9. Open questions / deferred

- **Dogfood validation** (live GitLab, real reviews/audits/replies) — deferred dogfood pass.
- **`mr_note` polling parity** — webhook-only in SP5.
- **`resolve_discussion`** stays a stub; resolution remains manual.
- **MR-feedback pushing commits** ("fix it for me") — needs branch-push credentials policy;
  deferred.
- **Platform-side finding fingerprints** for audit dedup — prompt-driven via `glab issue list`
  in SP5; revisit if prompt-driven dedup proves unreliable in dogfood.
- **Conditional outputs** — fan-out's empty-list semantics covered SP5's only need.
