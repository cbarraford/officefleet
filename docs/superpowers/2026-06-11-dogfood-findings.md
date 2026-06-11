# Dogfood pass — 2026-06-11 (Phase 1: local stack)

Live validation of the full stack against a real Postgres, the real claude CLI, and a
local fake GitLab API (recorder on :9477). Everything below ran for real — no fakes
inside the binary.

## Setup

- Postgres 16 in Docker (`officefleet-dogfood`, port 5499), fresh database.
- `./fleet` built via `make build` (embedded SPA).
- Dogfood config: `/tmp/officefleet-dogfood/fleet.yaml` — one `hello-report` smoke duty
  (no tools, fenced-JSON result contract) assigned to `dev-1` on
  `event-subscription: mr_opened`, outputs = fan-out `create_issue` over `findings` +
  `post_mr_comment` summary; gitlab plugin pointed at the fake recorder.
- Secrets stored encrypted (FSEC1/`FLEET_MASTER_KEY`); admin user via
  `fleet users create --password-stdin`.

## Bugs found and fixed (each committed individually)

1. **Claude executor used a nonexistent `--system` flag** (`internal/executor/claude.go`).
   The real CLI flag is `--system-prompt`; every run carrying a persona died on an
   unknown option, and the system prompt was *also* duplicated into stdin. Fix: drop the
   flag — the stdin `<system>` envelope (already pinned by tests) carries the persona
   across CLI versions.
2. **Fresh-install `fleet migrate` was broken** (`002_unique_constraints.sql`). 001 now
   declares the unique constraints inline, so 002's `ADD CONSTRAINT` collides — and its
   `EXCEPTION WHEN duplicate_object` guard misses Postgres's actual error for an existing
   constraint-backed index (`duplicate_table`, 42P07). Migrations 003–005 never applied
   on a fresh DB. Fix: guard catches `duplicate_object OR duplicate_table`. (Safe to
   amend: 002 could never have applied successfully anywhere.)
3. **Dedup-skipped runs recorded no reason** (`internal/run/pipeline.go`). Pause-gate
   skips record `agent_paused`/`assignment_paused`, but redeliveries collapsed by
   per-assignment dedup wrote a NULL reason — unreadable in the SPA run history. Fix:
   new `duplicate_event` skip reason.

## Validated live (no issues)

- **Migrations 001–005** on fresh PG (after fix #2): all tables incl. events,
  poll_cursors, users, sessions; migrate also seeds.
- **Secrets**: encrypted at rest (FSEC1), decrypted for plugin init.
- **Serve daemon**: healthz; embedded SPA at `/`; half-configured email plugin warns and
  continues (SP3b guard working).
- **Auth**: login → HttpOnly cookie session → `/me` (username+role); users API.
- **Avatars**: API-created agent → async initials SVG (~2s) → `avatar_url` cache-busted →
  `GET /avatars/<id>.svg` 200 `image/svg+xml` publicly.
- **The whole event pipeline, end-to-end with a real LLM**: webhook (token-checked) →
  event row (`mr:dogfood/sandbox:7:abc123`, pending) → dispatcher → **claude CLI run
  succeeded** (28,971 tokens) → fenced-JSON result extracted → **fan-out filed 2 issues**
  (model-authored titles) **+ 1 summary note**, all three `delivered` in
  `outputs_delivered` and received by the fake GitLab with correct `%2F`-encoded paths.
- **Live stats SQL** (the SP4a planner-rewrite that had never touched real PG):
  1 run / 100% success / 28,971 tokens / 3 outputs — all correct.
- **Event-level dedup**: identical webhook re-POST → `{"accepted":0}`.
- **Replay / at-least-once**: `POST /events/{id}/replay` → redispatch → per-assignment
  dedup recorded a `skipped` run (now with `duplicate_event` reason) — no duplicate LLM
  call, no duplicate deliveries.

## Not covered (Phase 2 — needs real credentials/services)

- Real GitLab: live webhooks/polls, mr-review against an actual MR (inline-comment
  positions on a real diff), code-audit `glab issue list` dedup, mr-feedback replies,
  clone-with-token inside run workspaces.
- Avatar generation against a real image backend (OpenAI key).
- GitHub/Slack/Discord/email plugins against live services.
- Browser walk of the SPA (the stack is left running at http://localhost:8090 —
  admin / dogfood-admin-pw — for a manual click-through).

## Teardown (when done poking)

```bash
docker rm -f officefleet-dogfood
pkill -f fake_gitlab.py
pkill -f "fleet serve"   # the dogfood daemon on :8090
rm -rf /tmp/officefleet-dogfood
```
