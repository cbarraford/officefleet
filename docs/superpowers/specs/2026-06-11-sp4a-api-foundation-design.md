# SP4a — API Foundation (Auth, Secrets, REST, SSE) — Design Spec

**Status:** Draft for review
**Date:** 2026-06-11
**Author:** brainstormed with Claude Code
**Parent:** `spec.md` §12 (Web UI, auth & secrets) and §13 (SP4 entry). SP4 is decomposed into
three slices, each its own spec → plan → implementation cycle:
- **SP4a (this spec):** the backend foundation — data-ownership fix, users/sessions/auth,
  real secrets encryption, the REST API + SSE, mounted into `fleet serve`.
- **SP4b:** the React/TS SPA consuming this API (all five §12 surfaces, embedded in the binary).
- **SP4c:** personas polish — avatar generation via a configured image backend (§6.1). The
  schema columns (`avatar_url`, `hired_at`) land in SP4a so 4b/4c need no migrations.

---

## 1. Summary

SP4a turns OfficeFleet's engine into an operable service: a session-authenticated JSON API
over every entity and record, a real encryption layer for secrets (the SP1 column is *named*
`encrypted_value` but stores plaintext today), built-in users with Admin/Viewer roles, an SSE
live-run feed, and the structural prerequisite for any UI at all — **the DB becomes the source
of truth for entity data**, with fleet.yaml demoted to first-boot bootstrap (today every
`fleet migrate` re-seeds from YAML, which would clobber UI edits).

### Decisions locked during brainstorming

1. **SP4 split into 4a/4b/4c**; this spec is 4a only. No SPA code in this cycle.
2. **DB owns entity data.** `fleet migrate` seeds from fleet.yaml only when the entity tables
   are all empty; `fleet seed --force` re-seeds explicitly (documented as overwriting UI
   edits). Backends / serve / plugin blocks stay YAML-owned (operational config).
3. **stdlib PBKDF2** (crypto/pbkdf2, Go ≥1.24) for password hashing — the zero-new-dependency
   policy holds for all of SP4a.

---

## 2. Goals & non-goals

### Goals
- Seed-policy change + `fleet seed --force`.
- `users` + `sessions` tables, PBKDF2 hashing, session cookies, Admin/Viewer middleware,
  `fleet users` CLI, login/logout/me endpoints.
- Real AES-256-GCM secrets encryption with legacy-plaintext compatibility, `fleet secrets`
  CLI, secrets API (names/status only — never values).
- REST API under `/api/v1`: entity CRUD (agents/duties/assignments), runs/events read +
  replay, run-now, backends read, per-agent stats, mounted into `fleet serve`.
- SSE run-lifecycle feed via an in-process broadcaster and one additive pipeline hook.
- Schema: `agents.avatar_url`, `agents.hired_at` (consumed by 4b/4c).

### Non-goals (SP4a)
- Any frontend code (SP4b).
- Avatar generation / image backends (SP4c).
- SSO/OIDC (the role check sits behind one middleware function — the later seam).
- Managed-backends table or backend CRUD (backends remain fleet.yaml config; the API exposes
  them read-only).
- TLS termination (deploy behind a reverse proxy; `serve.secure_cookies: true` config flips
  the cookie Secure flag).
- Rate limiting, audit log, password reset flows.
- New module dependencies.

---

## 3. Data ownership: DB owns, YAML bootstraps

- `seed.FromConfig` gains a guard: it counts agents, duties, and assignments; if **any** rows
  exist, it logs `DB already populated; skipping config seed (use 'fleet seed --force')` and
  returns without writing. First boot (all three empty) seeds exactly as today.
- New command `fleet seed --force`: runs the unconditional upsert path (the current behavior),
  printing a warning that UI edits to identically-named entities are overwritten.
- `fleet migrate` keeps calling the (now-guarded) seed, so first-boot UX is unchanged:
  `migrate` + a populated fleet.yaml still yields a working fleet.
- The known seed-collapse wart (multiple same-(agent,duty) YAML assignments collapsing onto
  one row) is unchanged in mechanics but loses its sharpest edge: it can now only happen at
  first boot or explicit `--force`, never silently on routine migrates.

---

## 4. Users, sessions, roles (`internal/auth`, migration 005)

### 4.1 Schema (migration `005_users_sessions.sql`)

- `users` — id (uuid pk), username (unique), password_hash (text), role (text, CHECK in
  `('admin','viewer')`), created_at, updated_at.
- `sessions` — token_hash (text pk), user_id (uuid fk → users ON DELETE CASCADE),
  expires_at, created_at; index on expires_at.
- Same migration adds `agents.avatar_url TEXT` and `agents.hired_at DATE` (both nullable).

### 4.2 Password hashing

PBKDF2-HMAC-SHA256 (stdlib `crypto/pbkdf2`), 600,000 iterations (OWASP 2023+), 16-byte
per-user random salt, 32-byte derived key. Stored as the versioned string
`pbkdf2-sha256$600000$<b64 salt>$<b64 hash>` — the prefix lets a future KDF (argon2id)
coexist and migrate on next login. Constant-time comparison on verify.

### 4.3 Sessions

- Login issues a 32-byte crypto/rand token, returned in cookie `fleet_session`
  (`HttpOnly`, `SameSite=Lax`, `Path=/`; `Secure` when `serve.secure_cookies: true`).
- The DB stores **SHA-256(token)**, never the token — a leaked DB cannot replay sessions.
- Fixed 7-day expiry; expired rows are lazily deleted on validation and bulk-deleted
  opportunistically at login. Logout deletes the row and clears the cookie.

### 4.4 Roles & middleware

- **Viewer:** GET requests and the SSE stream only. **Admin:** everything.
- One middleware wraps the API mux: unauthenticated → 401; authenticated viewer on a
  non-GET → 403. `POST /api/v1/login` is the only unauthenticated API route. `/healthz`
  and `/webhooks/*` sit outside the API mux entirely (webhooks authenticate per-plugin).
- Auth endpoints: `POST /api/v1/login {username,password}` → cookie + `{user, role}`;
  `POST /api/v1/logout`; `GET /api/v1/me` → `{username, role}`.

### 4.5 CLI

`fleet users create <username> --role admin|viewer` (password via interactive prompt or
`--password-stdin`), `fleet users list` (no hashes shown), `fleet users delete <username>`.
First boot: the operator creates the first admin via CLI. There are no default credentials.

---

## 5. Secrets encryption — made real (`internal/secrets`)

Today the `secrets.encrypted_value` BYTEA column stores plaintext and is read raw by
`buildSecretLookup` / `dbSecretsProvider`; there is not even a CLI to set one. SP4a fixes
both.

### 5.1 Crypto

- AES-256-GCM. Master key from env `FLEET_MASTER_KEY` (base64-encoded 32 bytes); a
  `${env:…}`-style indirection is unnecessary — it is already an env var.
- Stored value format: ASCII magic `FSEC1` + 12-byte random nonce + ciphertext(+tag), all in
  the existing BYTEA column. **Rows lacking the magic are legacy plaintext** — detectable
  without schema changes (the magic is improbable as a secret prefix; `encrypt-existing`
  makes the window moot).
- `internal/secrets`: `NewCipher(keyB64) (*Cipher, error)`, `Encrypt(plain []byte) ([]byte, error)`,
  `Decrypt(stored []byte) ([]byte, error)`, `IsEncrypted(stored []byte) bool`.

### 5.2 Read path (compatibility)

The existing lookup/provider paths route through one helper: if `IsEncrypted` → decrypt
(error if no/wrong key — loud, naming the secret); else return raw bytes (legacy
compatibility). `fleet serve` logs one startup warning listing any unencrypted secret names
and whether `FLEET_MASTER_KEY` is set.

### 5.3 CLI & API

- `fleet secrets set <name>` (value read from stdin — never argv), `fleet secrets list`
  (names + `encrypted: yes/no` only), `fleet secrets delete <name>`,
  `fleet secrets encrypt-existing` (idempotently encrypts all legacy rows; requires the key).
- Writes always encrypt; `set`/`encrypt-existing` without `FLEET_MASTER_KEY` fail with a
  clear error.
- API: `GET /api/v1/secrets` → `[{name, encrypted}]`; `PUT /api/v1/secrets/{name}` (body
  `{value}`; Admin); `DELETE /api/v1/secrets/{name}` (Admin). **No endpoint ever returns a
  secret value.**

---

## 6. REST API (`internal/api`)

JSON under `/api/v1`, served only by `fleet serve`. Consistent errors
`{"error": "..."}` with 400 (validation), 401, 403, 404, 409 (name conflicts), 500
(logged server-side, generic body). Handlers depend on small in-package interfaces
(the established `Invoker`-seam pattern) so everything tests with fakes + httptest.

### 6.1 Entities

| Route | Methods | Notes |
|---|---|---|
| `/api/v1/agents` | GET, POST | list / create |
| `/api/v1/agents/{id}` | GET, PATCH, DELETE | GET embeds `stats`; PATCH: name, role, system_prompt, default_backend, enabled, avatar_url, hired_at |
| `/api/v1/duties` (+`/{id}`) | GET, POST, PATCH, DELETE | full Duty fields |
| `/api/v1/assignments` (+`/{id}`) | GET, POST, PATCH, DELETE | trigger, outputs, config, backend, overrides, enabled |

- PATCH is a partial update: only fields present in the body change; absent fields are
  untouched (JSON-pointer-free, flat field set).
- Pause/resume is `PATCH {"enabled": false/true}` — the pipeline's existing pause gate
  provides the semantics (skipped runs recorded).
- Creation/patch validation mirrors config validation where overlapping (event-subscription
  triggers require non-empty filter `source`/`event_type`; duty trigger_kinds must include
  the assignment's kind; referenced backend names must exist in config) so the API cannot
  create what the dispatcher would reject.
- DELETE cascades per the existing FK design (assignments cascade from agents/duties; runs
  cascade from assignments).
- Repo additions: `Update`/`Delete` for the three entity repos; existing Upsert/Get/List
  unchanged.

### 6.2 Operations & records

| Route | Methods | Notes |
|---|---|---|
| `/api/v1/assignments/{id}/run` | POST | body `{params: {...}}` → `run.Invoker.Invoke(..., "manual", nil, params)`; returns the recorded Run summary |
| `/api/v1/runs` | GET | filters: `status`, `agent_id`, `limit` (default 50, newest first); summaries (no transcript) |
| `/api/v1/runs/{id}` | GET | full record incl. `llm_result` (transcript) and `event_id` |
| `/api/v1/events` | GET | filters: `status`, `limit`; reuses `ListRecent` |
| `/api/v1/events/{id}/replay` | POST | `MarkPending` + an in-process `dispatcher.Notify` nudge (the API lives inside serve, so unlike the CLI it redispatches immediately rather than waiting a rescan interval) |
| `/api/v1/backends` | GET | from config: name, kind, auth mode, model, default_effort — never key material |
| `/api/v1/agents/{id}/stats` | GET | the §6 `AgentStats` aggregation |

- Stats SQL (one query over `runs` per agent): total runs; runs/tokens/cost last 30d;
  success rate = succeeded/(succeeded+failed) and skip rate last 30d; cumulative tokens/cost;
  outputs delivered = count of `outputs_delivered` entries with status `delivered`
  (jsonb expansion), total + 30d; avg wall-clock duration; last_run_at.
- New repo: `RunRepo.ListFiltered(status, agentID, limit)`; `StatsRepo` (or a method on
  RunRepo) for the aggregation.

### 6.3 SSE live feed

- `GET /api/v1/stream` (auth: any role) — `text/event-stream`; events `run_started` and
  `run_finished`, JSON payload = run summary (id, assignment/agent/duty ids, trigger kind,
  status, tokens, cost).
- `internal/api.Broadcaster`: subscriber channels (buffer 16), non-blocking publish that
  **drops messages to slow consumers** (the feed is advisory; the runs table is truth — a
  client that misses messages refetches `/api/v1/runs`). Subscribers removed on disconnect
  via request context.
- The pipeline gains one additive, nil-safe hook: `Pipeline.onRunUpdate func(*domain.Run)`,
  invoked after the initial Insert (started) and after each terminal record (succeeded /
  failed / skipped). Set only by serve wiring via a `SetRunUpdateHook(fn)` method (`NewPipeline` keeps its
  signature). Same additive precedent as `EventID`.

### 6.4 Mounting

`server.Handler()` becomes `Handler(mounts ...func(*http.ServeMux))`: the webhook + healthz
routes register as today, then each mount function adds its routes. `fleet serve` constructs
the API (`api.New(deps...)`) and passes its mount. CLI commands other than `serve` never
construct the API.

---

## 7. Error handling summary

| Failure | Behavior |
|---|---|
| Unauthenticated API request | 401 `{"error":"authentication required"}` |
| Viewer attempts mutation | 403 |
| Login with bad credentials | 401 (uniform; no user-exists oracle, constant-time verify) |
| Entity validation failure | 400 with the specific reason |
| Name conflict on create | 409 |
| Secrets write without master key | CLI: clear error; API: 500 with `{"error":"master key not configured"}` |
| Encrypted secret read without/with wrong key | Error naming the secret (plugin Init/run fails loudly) |
| Legacy plaintext secret read | Works; startup warning lists names |
| SSE slow consumer | Messages dropped for that subscriber only |
| Run-now on disabled assignment/agent | Run recorded as skipped (existing pause gate) — 200 with the skip |

---

## 8. Testing strategy

- **Unit — auth:** PBKDF2 round-trip, versioned-format parse/reject, constant-time verify;
  session create/validate/expiry/destroy against a fake store; middleware matrix
  (anon/viewer/admin × GET/POST/PATCH/DELETE/stream).
- **Unit — secrets:** encrypt/decrypt round-trip, tamper detection (GCM auth failure),
  `IsEncrypted` on legacy bytes, missing/wrong/malformed key errors, idempotent
  encrypt-existing logic.
- **Unit — seed guard:** empty DB seeds; any-populated skips; `--force` overwrites (fake
  repos).
- **Unit — broadcaster:** fan-out, slow-consumer drop, unsubscribe-on-context-cancel.
- **API integration (httptest + fake repos/invoker):** login → cookie → `/me`; CRUD
  round-trips incl. validation 400s and conflict 409; viewer 403 matrix; run-now → fake
  invoker called with params, run summary returned; stats endpoint shape; events replay;
  backends endpoint redaction; secrets endpoints never echo values; SSE client receives
  started/finished for a pipeline-hook firing.
- **CLI:** users/secrets command validation paths (DB-less where possible).
- **Pipeline hook:** unit test that the hook fires on start + each terminal path and that a
  nil hook is safe (all existing pipeline tests run with nil).

---

## 9. Acceptance criteria

1. With a populated DB, `fleet migrate` performs no entity writes (a UI-edited agent
   survives); an empty DB still bootstraps from fleet.yaml; `fleet seed --force` overwrites
   with a printed warning.
2. `fleet users create/list/delete` work; `POST /login` sets an HttpOnly cookie backed by a
   hashed session row; `/me` returns the role; logout invalidates; anonymous API calls → 401;
   viewer mutations → 403; bad logins are uniform 401s.
3. Secrets written via CLI or API are AES-256-GCM at rest (verified by raw column
   inspection in tests); `encrypt-existing` converts legacy rows idempotently; plugin
   secret lookups decrypt transparently and legacy plaintext still resolves with a startup
   warning; absent `FLEET_MASTER_KEY` fails writes loudly and never silently stores
   plaintext; no API/CLI surface ever returns a secret value.
4. Entity CRUD persists through the API with validation parity (an API-created
   event-subscription assignment without filter.source is rejected 400); `PATCH
   {"enabled":false}` produces recorded skipped runs via the existing gate.
5. `POST /assignments/{id}/run` executes through the Invoker and returns the recorded Run;
   `/runs`, `/runs/{id}` (with event_id), `/events`, `/events/{id}/replay`, `/backends`
   (redacted), and `/agents/{id}/stats` return per §6.
6. An SSE client receives `run_started`/`run_finished` for runs executed while connected;
   a slow client drops messages without affecting other subscribers or the pipeline.
7. `fleet serve` hosts the API alongside webhooks/pollers/dispatcher/cron; all SP1–SP3b
   tests pass unchanged; gofmt/vet clean; zero new module dependencies.

---

## 10. Open questions / deferred

- **SSO/OIDC** — behind the single middleware seam (spec.md §12); later.
- **Argon2id migration** — the versioned hash format supports rehash-on-login when/if
  x/crypto is adopted.
- **Backend CRUD / managed-backends table** — backends stay YAML; revisit with SP4b's
  settings surface if operators need UI-managed backends.
- **Audit log** (who changed what) — natural SP4b/c follow-up; the API layer is where it
  would hook.
- **Dispatcher worker recover()** — known framework follow-up from SP3b (a plugin panic
  kills serve); not SP4a scope but adjacent to this slice's serve wiring; candidate for the
  SP4a plan's final task if cheap, else its own polish item.
- **Session sliding expiry / refresh** — fixed 7d window for now.
- **Avatar generation** — SP4c (columns ship now).
