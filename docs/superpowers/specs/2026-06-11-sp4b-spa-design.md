# SP4b — Operator SPA (React/TS) — Design Spec

**Status:** Draft for review
**Date:** 2026-06-11
**Author:** brainstormed with Claude Code
**Parent:** `spec.md` §12 (the five surfaces); SP4a design spec (the API this consumes,
docs/superpowers/specs/2026-06-11-sp4a-api-foundation-design.md). SP4c (avatars) follows as
its own spec; this SPA renders initials bubbles until SP4c fills `avatar_url`.

---

## 1. Summary

SP4b ships the operator web UI: a React/TypeScript SPA built with Vite, embedded into the
`fleet` binary via `go:embed`, served by `fleet serve` alongside the API and webhooks. It
implements all five §12 surfaces against the SP4a `/api/v1` contract (session cookie auth,
snake_case JSON, SSE run feed). One small backend addition closes the §12 gap found during
brainstorming: a **users management API** (SP4a shipped only the CLI).

### Decisions locked during brainstorming

1. **React + TS + Vite, embedded.** `web/` holds the frontend project; `make web` builds into
   `internal/web/dist`, which `go:embed` serves with SPA-fallback routing. Only
   `dist/.gitkeep` is committed (see §3): when the embedded FS has no `index.html`, the
   handler serves an inline "UI not built — run `make web`" page. Dev loop: Vite dev server proxying `/api` and `/avatars` to `:8080` — no
   CORS configuration anywhere (same-origin in prod, proxy in dev).
2. **Users API added in SP4b** — GET/POST/DELETE under `/api/v1/users`, admin-only mutations,
   self-delete blocked.

---

## 2. Goals & non-goals

### Goals
- The five §12 surfaces: live dashboard (SSE), agents employee-directory, duty library,
  per-agent detail, integrations & settings.
- `internal/web`: embedded static serving with SPA fallback, mounted via the existing
  `server.Handler(mounts...)`.
- Users management API + the middleware change it needs (session userID in request context).
- Login/logout UX over the SP4a session cookie; viewer role renders read-only (mutating
  controls hidden/disabled; the server remains the enforcement).
- Build tooling: `web/` Vite project, `Makefile` (`make web`, `make build`), placeholder
  pattern, `.gitignore` entries (node_modules, dist contents except placeholder).
- Frontend test gate: `tsc --noEmit` + `vite build` + a small vitest suite for the API client.

### Non-goals (SP4b)
- Avatar **generation** (SP4c) — the UI ships the initials bubble and an `avatar_url`
  renderer; regenerate/upload controls arrive with SP4c's endpoints.
- Playwright/browser e2e (deferred; recorded).
- Mobile layout polish (usable, not pixel-perfect, below ~900px).
- Live template preview (spec.md §9's "previewable" — needs a render-preview endpoint;
  deferred to a later slice with the audit-log work).
- i18n, theming beyond the single dark theme, websockets (SSE suffices).
- Component libraries / Tailwind — hand-rolled components + one custom CSS theme.

---

## 3. Repository layout & build pipeline

```
web/
  package.json            react, react-dom, react-router-dom; dev: typescript, vite,
                          @vitejs/plugin-react, vitest
  vite.config.ts          build.outDir = ../internal/web/dist (emptyOutDir),
                          server.proxy: /api, /avatars, /healthz -> http://localhost:8080
  tsconfig.json           strict
  index.html
  src/                    (see §5)
internal/web/
  web.go                  //go:embed all:dist + Mount(mux) SPA handler
  web_test.go
  dist/.gitkeep           the only committed dist file (build output is untracked)
Makefile                  web: npm ci+build · build: web + go build · test: go test + web typecheck
```

- `make web` runs `npm ci && npm run build` in `web/` (build = `tsc --noEmit && vite build`).
- **Dirty-worktree avoidance (lesson from the committed `fleet` binary):** nothing the build
  writes is ever tracked. `.gitignore` adds `web/node_modules/` and `internal/web/dist/*`
  with `!internal/web/dist/.gitkeep`; Vite is configured `emptyOutDir: false` so `.gitkeep`
  survives builds (`make web-clean` wipes dist back to `.gitkeep` when hashed assets
  accumulate). There is NO committed index.html: when the embedded FS lacks one (fresh clone,
  no Node build), the SPA handler serves a small inline "UI not built — run `make web`" page.
- Node is required only to build the UI; `go build` always works (inline fallback page or
  built dist — whatever `internal/web/dist` holds at compile time).

---

## 4. Serving (`internal/web`)

```go
//go:embed all:dist
var distFS embed.FS

// Mount registers the SPA: static assets from dist, with index.html fallback
// for client-routed paths. Registered LAST on the mux — the "/" pattern is
// ServeMux's lowest-precedence route, so /api/v1, /webhooks, /healthz (and
// later /avatars) always win.
func Mount(mux *http.ServeMux)
```

- `GET /` + any non-asset path → `index.html` (client routing); real asset paths (`/assets/*`,
  hashed Vite output) → served with correct content types; unknown `/api/*` never reaches the
  fallback (those patterns are registered explicitly and 404 inside their own handlers). When
  the embedded FS has no `index.html` (UI not built), every SPA path serves the inline
  "UI not built" page instead.
- Static files are **public** — the login page must load before any session exists; all data
  access stays behind the session API. No cache headers beyond Vite's content-hashing needs
  (`index.html: no-cache`, hashed assets: long max-age).
- `fleet serve` passes `web.Mount` as a second mount alongside `api.Mount`.

---

## 5. SPA architecture

```
src/
  main.tsx               router bootstrap
  App.tsx                layout shell: sidebar nav, session guard, login redirect
  api/client.ts          typed fetch wrapper: credentials, JSON error envelope,
                         401 -> redirect /login (except on /login itself)
  api/types.ts           TS mirrors of the snake_case API entities (Agent, Duty,
                         Assignment, Run, Event, AgentStats, BackendView, SecretInfo, User)
  api/sse.ts             EventSource wrapper: auto-reconnect w/ backoff, onEvent callback,
                         reconnect triggers a refetch signal (advisory-feed contract)
  pages/Login.tsx
  pages/Dashboard.tsx
  pages/Agents.tsx
  pages/AgentDetail.tsx
  pages/Duties.tsx
  pages/Settings.tsx
  components/            Card, Table, Modal, Badge, StatusPill, AvatarBubble,
                         ConfirmButton, JsonView (collapsible for payloads/transcripts)
  styles.css             dark ops theme, CSS custom properties
```

### Surface specs (per §12)

1. **Dashboard** — live activity feed (SSE events prepend, capped ~50), recent-runs table
   (status pills, agent/duty names resolved from a cached agents/duties fetch), counters:
   active (enabled) agents, runs today, failures today. SSE disconnect shows a "reconnecting"
   indicator; reconnect refetches the runs table.
2. **Agents** — responsive card grid: `AvatarBubble` (avatar_url image when set, else initials
   on a name-hash color), name, role, hire date, Paused badge when disabled, stats strip
   (runs 30d · success% · outputs 30d) from `GET /agents` + embedded stats on detail (list
   view fetches `/agents/{id}/stats` lazily per card, cached). Inline pause/resume toggle
   (admin), "New agent" modal (name, role, system prompt, default backend dropdown from
   `/backends`, hire date).
3. **Agent detail** — header (avatar, name, role, hired, enabled toggle), §6 stats panel,
   assignments table (duty name, trigger kind/summary, enabled toggle, Run now button with
   params JSON input), run history (filterable by status) with a drawer: rendered system +
   task prompts, LLM summary/transcript (collapsible), outputs delivered, link to the raw
   event (`event_id` → `/events` view).
4. **Duty library** — table + create/edit modal: name, role category, description, trigger
   kinds (checkboxes incl. event-subscription), required tools (tag input), prompt
   (textarea, monospace), output action types (plugin/action rows), config schema (JSON
   textarea, validated client-side as JSON).
5. **Settings** — tabs:
   - *Backends*: read-only table from `/backends` (name, kind, auth mode, model, effort).
   - *Secrets*: list (name + encrypted badge), set (name + value form → PUT), delete with
     confirm. Values never displayed — the form is write-only.
   - *Users*: list (username, role, created), create (username/password/role), delete with
     confirm; self-delete button disabled.
   - *Events*: recent events table (source/type/status/received/dedup key), replay button.

### Auth & roles in the client
- `GET /me` on boot: 401 → `/login`; role stored in app context.
- Viewer: all mutating controls hidden or disabled with a tooltip ("viewer role is
  read-only"). The server's 403 remains the actual enforcement; the UI is courtesy.
- Logout button posts `/logout` and redirects.

---

## 6. Users management API (backend addition)

| Route | Method | Behavior |
|---|---|---|
| `/api/v1/users` | GET | list users (`password_hash` already `json:"-"`) |
| `/api/v1/users` | POST | `{username, password, role}` → HashPassword + Create; 400 invalid role/empty fields; 409 duplicate |
| `/api/v1/users/{username}` | DELETE | 404 unknown; **400 when deleting your own account** |

- Mutations are admin-only via the existing middleware. Listing follows the uniform
  middleware semantics — viewers can GET (usernames + roles only; harmless and consistent).
- Self-delete protection requires knowing *who* the caller is: the middleware additionally
  stores the session's `userID` in the request context (`ctxKeyUserID`), and `/me` gains
  `username` in its response (one `Users.GetByID`-style lookup — add `GetByID` to the
  UserStore interface and repo).
- `api.Deps.Users` widens from the login-only interface to
  `{GetByUsername, GetByID, Create, List, Delete}` — repo methods exist except `GetByID`
  (trivial addition).

---

## 7. Error handling & UX conventions

| Failure | UX |
|---|---|
| API 401 (expired session) | redirect to /login, return-to preserved |
| API 403 (viewer mutation) | toast "viewer role is read-only" (shouldn't occur — controls hidden) |
| API 400 validation | inline form error from the JSON `error` field |
| API 409 conflict | inline form error |
| API 5xx | toast with the generic message |
| SSE drop | "reconnecting…" indicator + refetch on reconnect |
| fetch network failure | toast + retry button on page-level loads |

---

## 8. Testing strategy

- **Go:** `internal/web` — placeholder serving, SPA fallback (`/agents/123` → index.html),
  asset content types, `/api/v1/*` and `/healthz` never intercepted (register a probe mount).
  Users API — handler tests with fakes (list redaction, create/dup/role validation,
  self-delete 400, viewer GET allowed / viewer POST 403 via the existing matrix pattern).
- **Frontend:** `tsc --noEmit` + `vite build` are the hard CI gate (`make test` runs both
  after `go test`). Vitest: `api/client.ts` (401 redirect behavior, error envelope parsing,
  credentials include) and `api/sse.ts` (reconnect/backoff logic with a fake EventSource).
  Component tests deferred (recorded) — surfaces are exercised manually this slice.
- **Manual smoke (documented in the plan):** `make web && go run ./cmd/fleet serve` →
  login → click through all five surfaces against a seeded local DB.

---

## 9. Acceptance criteria

1. `make web && go build ./...` produces a binary whose `fleet serve` serves the SPA at `/`,
   the API at `/api/v1`, webhooks, and healthz from one listener; plain `go build` without
   Node serves the inline "UI not built" page; neither path ever dirties the git worktree.
2. Login works end-to-end (cookie session); an expired/absent session lands on `/login`;
   logout invalidates; viewer sees read-only surfaces.
3. All five surfaces function against the SP4a API: dashboard reflects SSE run events live;
   agents grid + pause toggle + create; agent detail shows stats/assignments/run history with
   prompts+transcript+event link; duty CRUD; settings tabs (backends ro, secrets write-only,
   users CRUD, events + replay).
4. Users API: list never contains hashes; create validates role and rejects duplicates (409);
   self-delete returns 400; viewer mutations 403; `/me` returns username + role.
5. SPA fallback never shadows `/api/v1/*`, `/webhooks/*`, `/healthz` (Go tests prove it).
6. `tsc --noEmit`, `vite build`, vitest, and the full Go suite pass; gofmt/vet clean; the Go
   module gains zero new dependencies (Node deps live in `web/package.json` only).

---

## 10. Open questions / deferred

- **Playwright e2e** — revisit once the surfaces stabilize.
- **Template render-preview endpoint** (spec.md §9 "previewable") — pairs with the audit-log
  work; deferred.
- **Component test depth** — vitest covers the client/sse libs only this slice.
- **Viewer-visible secrets in run records** — carried consideration from SP4a (rendered
  prompts may embed `{{secret}}` output); the run drawer displays prompts as the spec
  mandates; the audit-log slice owns redaction policy.
- **Login rate limiting** — carried from SP4a.
