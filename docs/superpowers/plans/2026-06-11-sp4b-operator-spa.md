# SP4b — Operator SPA (React/TS) + Users API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the operator web UI — a React/TypeScript SPA embedded in the `fleet` binary — covering all five spec.md §12 surfaces against the SP4a `/api/v1` API, plus the users-management API the Settings page needs.

**Architecture:** `web/` holds a Vite + React + TS project that builds into `internal/web/dist`; `internal/web` embeds that directory (`//go:embed all:dist`) and mounts a SPA handler with index.html fallback as the lowest-precedence route on the existing server mux. The backend gains three `/api/v1/users` routes and a middleware change (session userID into request context) for self-delete protection and `/me` username. Nothing the frontend build writes is ever tracked by git: only `internal/web/dist/.gitkeep` is committed, and when the embedded FS has no `index.html` the handler serves an inline "UI not built" page.

**Tech Stack:** Go stdlib (`embed`, `net/http`) — zero new Go deps. Frontend: react ^19, react-dom ^19, react-router-dom ^7; dev: typescript ^5.8, vite ^6, @vitejs/plugin-react ^4, vitest ^3, jsdom. Hand-rolled CSS (one dark theme file), no component library.

**Spec:** `docs/superpowers/specs/2026-06-11-sp4b-spa-design.md`

---

## Environment notes (read before starting)

1. **`NODE_OPTIONS` quirk in this session:** the harness injects a `--require` of a file that may not exist, which breaks every `node`/`npm` invocation with `Cannot find module ... restore-node-options.cjs`. If you see that error, prefix the command with `NODE_OPTIONS=` (empty), e.g. `cd web && NODE_OPTIONS= npm ci`. Do NOT bake this prefix into the Makefile or package.json — it is a session quirk, not a project property. Node v25 / npm 11 are installed.
2. **Conventions (established in SP1–SP4a):** commit directly to `master`; small commits per task; `gofmt` and `go vet ./...` must stay clean; the Go module gains **zero** new dependencies (AC6); tests use fakes, never Postgres.
3. **LSP diagnostics are often stale in this repo** — trust `go test ./...` and `go build ./...` output, not stale editor diagnostics.
4. Run all Go commands from the repo root: `/Users/cbarraford/workshop/office-fleet`.

## File map (what this plan touches)

| Path | Action | Responsibility |
|---|---|---|
| `internal/api/middleware.go` | modify | store session userID in request context (`ctxKeyUserID`) |
| `internal/api/auth_handlers.go` | modify | `/me` returns `username` (via `Users.GetByID`) |
| `internal/api/api.go` | modify | widen `UserStore` interface; register `/api/v1/users` routes |
| `internal/api/users_handlers.go` | create | list/create/delete users handlers |
| `internal/api/users_handlers_test.go` | create | users API tests (redaction, validation, 409, self-delete, role matrix) |
| `internal/api/api_test.go` | modify | widen `fakeUserStore` |
| `internal/api/middleware_test.go` | modify | `authedAPI` seeds a user for `/me` |
| `internal/repo/users.go` | modify | add `GetByID` |
| `internal/web/web.go` | create | `//go:embed all:dist`, `Mount`, SPA fallback, not-built page, cache headers |
| `internal/web/web_test.go` | create | fallback/asset/precedence tests via `fstest.MapFS` |
| `internal/web/dist/.gitkeep` | create | the ONLY committed dist file |
| `cmd/fleet/main.go` | modify | mount `web.Mount` after `apiSrv.Mount` |
| `.gitignore` | modify | `web/node_modules/`, `internal/web/dist/*`, `!.gitkeep` |
| `Makefile` | create | `web`, `web-clean`, `build`, `test` |
| `web/package.json`, `web/tsconfig.json`, `web/vite.config.ts`, `web/index.html` | create | Vite project |
| `web/src/main.tsx`, `web/src/App.tsx` | create | router bootstrap; layout shell + session guard |
| `web/src/styles.css` | create | dark theme |
| `web/src/api/types.ts` | create | TS mirrors of the snake_case API |
| `web/src/api/client.ts` (+`client.test.ts`) | create | typed fetch wrapper, 401 handling |
| `web/src/api/sse.ts` (+`sse.test.ts`) | create | EventSource wrapper w/ backoff + refetch signal |
| `web/src/lib/format.ts`, `web/src/lib/toast.ts` | create | date/cost formatting; toast bus |
| `web/src/components/*.tsx` | create | Card, Table, Modal, Badge, StatusPill, AvatarBubble, ConfirmButton, JsonView, Toasts |
| `web/src/pages/{Login,Dashboard,Agents,AgentDetail,Duties,Settings}.tsx` | create | the five surfaces + login |

## API contract facts the frontend MUST match (verified against SP4a code)

- All entity JSON is **snake_case** (`system_prompt`, `default_backend`, `avatar_url`, `hired_at`, …). `User.password_hash` is never serialized (`json:"-"`).
- `GET /api/v1/agents/{id}` returns an **envelope**: `{"agent": Agent, "stats": AgentStats | null}`. All other gets/lists return the bare entity/array.
- `POST /api/v1/login` body `{username, password}` → `{username, role}` + session cookie. 401 body: `{"error":"invalid credentials"}`.
- Errors are always `{"error": "message"}` with 400/401/403/404/409/500.
- Viewer role: any non-GET → 403 `{"error":"viewer role is read-only"}` (middleware-enforced).
- `GET /api/v1/stream` is SSE; messages are unnamed `data:` lines whose JSON payload is `{"event":"run_started"|"run_finished","id","assignment_id","agent_id","duty_id","trigger_kind","status","tokens","cost"}` (no SSE `event:` field — use `onmessage`).
- `POST /api/v1/assignments/{id}/run` body `{"params": {...}}` → the created Run.
- `POST /api/v1/events/{id}/replay` → `{"status":"requeued"}`.
- `GET /api/v1/backends` → `[{name, kind, auth_mode, model?, default_effort?}]`.
- `GET /api/v1/secrets` → `[{name, encrypted}]`; `PUT /api/v1/secrets/{name}` body `{"value":"..."}`.
- Valid duty trigger kinds: `manual`, `cron`, `event-subscription`, `continuous`.
- `hired_at` is sent to the API as a `"YYYY-MM-DD"` string on create/patch but comes back RFC3339.

---

### Task 1: Users plumbing — userID in context, widened UserStore, repo GetByID, /me username

**Files:**
- Modify: `internal/api/middleware.go`
- Modify: `internal/api/api.go:63-65` (UserStore interface)
- Modify: `internal/api/auth_handlers.go:56-59` (handleMe)
- Modify: `internal/api/api_test.go:157-175` (fakeUserStore)
- Modify: `internal/api/middleware_test.go` (authedAPI helper)
- Modify: `internal/repo/users.go` (add GetByID)

- [x] **Step 1: Widen the fake user store in `internal/api/api_test.go`**

Replace the existing `fakeUserStore` block (lines 157–175: the struct, `newFakeUserStore`, and `GetByUsername`) with:

```go
type fakeUserStore struct {
	mu    sync.Mutex
	users map[string]*domain.User // keyed by username
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{users: map[string]*domain.User{}}
}

// add seeds a user, assigning an ID when unset.
func (f *fakeUserStore) add(u *domain.User) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	cp := *u
	f.users[u.Username] = &cp
}

func (f *fakeUserStore) GetByUsername(_ context.Context, username string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[username]
	if !ok {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}

func (f *fakeUserStore) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.ID == id {
			cp := *u
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeUserStore) Create(_ context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[u.Username]; ok {
		// mirror the Postgres error text isUniqueViolation matches on
		return fmt.Errorf("duplicate key value violates unique constraint (SQLSTATE 23505)")
	}
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	cp := *u
	f.users[u.Username] = &cp
	return nil
}

func (f *fakeUserStore) List(_ context.Context) ([]*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.users))
	for n := range f.users {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*domain.User, 0, len(names))
	for _, n := range names {
		cp := *f.users[n]
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeUserStore) Delete(_ context.Context, username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[username]; !ok {
		return fmt.Errorf("user %q not found", username)
	}
	delete(f.users, username)
	return nil
}
```

Add `"sort"` to the `api_test.go` import block if not already present.

- [x] **Step 2: Write the failing test for `/me` returning the username**

Append to `internal/api/middleware_test.go`:

```go
func TestMeReturnsUsernameAndRole(t *testing.T) {
	a, token := authedAPI(t, domain.RoleAdmin)
	mux := http.NewServeMux()
	a.Mount(mux)
	resp := doReq(t, mux, http.MethodGet, "/api/v1/me", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["username"] != "tester-admin" {
		t.Errorf("username = %q, want tester-admin", body["username"])
	}
	if body["role"] != domain.RoleAdmin {
		t.Errorf("role = %q, want admin", body["role"])
	}
}
```

Add `"encoding/json"` to the `middleware_test.go` imports if missing. Note: `doReq` already exists in `middleware_test.go`; check its signature before use (`doReq(t, handler, method, path, token)`).

- [x] **Step 3: Update `authedAPI` in `middleware_test.go` to seed the user**

Replace the body of `authedAPI` with:

```go
// authedAPI returns a minimal API plus a logged-in client cookie for the given role.
func authedAPI(t *testing.T, role string) (*API, string) {
	t.Helper()
	sessions := auth.NewSessions(newMemSessionStore(role))
	users := newFakeUserStore()
	me := &domain.User{ID: uuid.New(), Username: "tester-" + role, Role: role}
	users.add(me)
	a := New(Deps{Sessions: sessions, Users: users})
	token, err := sessions.Start(context.Background(), me.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a, token
}
```

- [x] **Step 4: Run the new test to verify it fails**

Run: `go test ./internal/api/ -run TestMeReturnsUsernameAndRole -v`
Expected: FAIL — `username = "", want tester-admin` (current handleMe returns only role). Compile errors about the widened fake are fine to fix as they surface; the *behavioral* failure is the username assertion.

- [x] **Step 5: Widen the `UserStore` interface in `internal/api/api.go`**

Replace:

```go
type UserStore interface {
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
}
```

with:

```go
type UserStore interface {
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	Create(ctx context.Context, u *domain.User) error
	List(ctx context.Context) ([]*domain.User, error)
	Delete(ctx context.Context, username string) error
}
```

- [x] **Step 6: Store the userID in the request context (`internal/api/middleware.go`)**

The const block currently declares `ctxKeyRole` and an **unused** `ctxKeyUsername` (verify with `grep -rn ctxKeyUsername internal/` — expect only the declaration). Replace the consts with:

```go
const (
	ctxKeyRole ctxKey = iota
	ctxKeyUserID
)
```

In `requireAuth`, replace the `_ = userID` line and the context wiring (currently lines 39–45) with:

```go
		if role != domain.RoleAdmin && r.Method != http.MethodGet {
			writeError(w, http.StatusForbidden, "viewer role is read-only")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyRole, role)
		ctx = context.WithValue(ctx, ctxKeyUserID, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
```

- [x] **Step 7: `/me` returns the username (`internal/api/auth_handlers.go`)**

Replace `handleMe` with:

```go
func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	role, _ := r.Context().Value(ctxKeyRole).(string)
	userID, _ := r.Context().Value(ctxKeyUserID).(uuid.UUID)
	user, err := a.users.GetByID(r.Context(), userID)
	if err != nil {
		a.logf("api: me lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		// The session outlived the account (user deleted): treat as unauthenticated.
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": user.Username, "role": role})
}
```

Add `"github.com/google/uuid"` to the `auth_handlers.go` imports.

- [x] **Step 8: Add `GetByID` to `internal/repo/users.go`**

Insert after `GetByUsername`:

```go
// GetByID returns (nil, nil) when no user has the id.
func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	var u domain.User
	err := r.db.QueryRow(ctx,
		"SELECT id, username, password_hash, role, created_at, updated_at FROM users WHERE id=$1",
		id).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}
```

- [x] **Step 9: Build + full test run**

Run: `go build ./... && go test ./internal/api/ ./internal/repo/ -count=1`
Expected: PASS, including `TestMeReturnsUsernameAndRole`. If any other test constructed an API with a `fakeUserStore` and now fails to compile, fix the construction (the fake's method set only grew — most failures will be missing `users.add(...)` seeding for `/me`-touching tests).

Then run the whole suite: `go test ./... -count=1` — expected PASS.

- [x] **Step 10: gofmt + vet + commit**

```bash
gofmt -l . && go vet ./...
git add internal/api internal/repo
git commit -m "feat(sp4b): session userID in request context; /me returns username; widen UserStore"
```

(`gofmt -l .` must print nothing.)

---

### Task 2: Users management API — GET/POST/DELETE /api/v1/users

**Files:**
- Create: `internal/api/users_handlers.go`
- Create: `internal/api/users_handlers_test.go`
- Modify: `internal/api/api.go` (`authedMux` route table)

- [x] **Step 1: Write the failing tests**

Create `internal/api/users_handlers_test.go`:

```go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// usersTestAPI builds an API whose caller ("caller") is logged in with role.
func usersTestAPI(t *testing.T, role string) (*API, *fakeUserStore, string) {
	t.Helper()
	sessions := auth.NewSessions(newMemSessionStore(role))
	users := newFakeUserStore()
	me := &domain.User{ID: uuid.New(), Username: "caller", Role: role, PasswordHash: "x"}
	users.add(me)
	a := New(Deps{Sessions: sessions, Users: users})
	token, err := sessions.Start(context.Background(), me.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a, users, token
}

func usersReq(t *testing.T, a *API, method, path, token string, body any) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	a.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestListUsersOmitsPasswordHash(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	users.add(&domain.User{Username: "bob", Role: domain.RoleViewer, PasswordHash: "super-secret-hash"})

	resp := usersReq(t, a, http.MethodGet, "/api/v1/users", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if strings.Contains(body, "password_hash") || strings.Contains(body, "super-secret-hash") {
		t.Errorf("response leaks password hash: %s", body)
	}
	if !strings.Contains(body, `"bob"`) || !strings.Contains(body, `"caller"`) {
		t.Errorf("response missing expected usernames: %s", body)
	}
}

func TestCreateUser(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	resp := usersReq(t, a, http.MethodPost, "/api/v1/users", token,
		map[string]string{"username": "newbie", "password": "hunter22", "role": "viewer"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	u, err := users.GetByUsername(context.Background(), "newbie")
	if err != nil || u == nil {
		t.Fatalf("user not stored: %v", err)
	}
	if u.Role != domain.RoleViewer {
		t.Errorf("role = %q, want viewer", u.Role)
	}
	if !auth.VerifyPassword(u.PasswordHash, "hunter22") {
		t.Error("stored hash does not verify against the password")
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "password") {
		t.Errorf("create response leaks password material: %s", raw)
	}
}

func TestCreateUserValidation(t *testing.T) {
	cases := []struct {
		name string
		body map[string]string
	}{
		{"empty username", map[string]string{"username": "  ", "password": "pw", "role": "viewer"}},
		{"empty password", map[string]string{"username": "x", "password": "", "role": "viewer"}},
		{"bad role", map[string]string{"username": "x", "password": "pw", "role": "root"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, token := usersTestAPI(t, domain.RoleAdmin)
			resp := usersReq(t, a, http.MethodPost, "/api/v1/users", token, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestCreateUserDuplicate(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	users.add(&domain.User{Username: "bob", Role: domain.RoleViewer, PasswordHash: "x"})
	resp := usersReq(t, a, http.MethodPost, "/api/v1/users", token,
		map[string]string{"username": "bob", "password": "pw", "role": "viewer"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestDeleteUser(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	users.add(&domain.User{Username: "bob", Role: domain.RoleViewer, PasswordHash: "x"})
	resp := usersReq(t, a, http.MethodDelete, "/api/v1/users/bob", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if u, _ := users.GetByUsername(context.Background(), "bob"); u != nil {
		t.Error("user still present after delete")
	}
}

func TestDeleteUserUnknown(t *testing.T) {
	a, _, token := usersTestAPI(t, domain.RoleAdmin)
	resp := usersReq(t, a, http.MethodDelete, "/api/v1/users/ghost", token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDeleteUserSelf(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	resp := usersReq(t, a, http.MethodDelete, "/api/v1/users/caller", token, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if u, _ := users.GetByUsername(context.Background(), "caller"); u == nil {
		t.Error("self-delete went through")
	}
}

func TestUsersViewerRoleMatrix(t *testing.T) {
	a, _, token := usersTestAPI(t, domain.RoleViewer)
	if resp := usersReq(t, a, http.MethodGet, "/api/v1/users", token, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("viewer GET = %d, want 200", resp.StatusCode)
	}
	if resp := usersReq(t, a, http.MethodPost, "/api/v1/users", token,
		map[string]string{"username": "x", "password": "pw", "role": "viewer"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer POST = %d, want 403", resp.StatusCode)
	}
	if resp := usersReq(t, a, http.MethodDelete, "/api/v1/users/caller", token, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer DELETE = %d, want 403", resp.StatusCode)
	}
}
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/api/ -run 'TestListUsers|TestCreateUser|TestDeleteUser|TestUsersViewer' -v`
Expected: FAIL — the GET/POST/DELETE users routes 404 (no handlers registered yet).

- [x] **Step 3: Implement the handlers**

Create `internal/api/users_handlers.go`:

```go
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.users.List(r.Context())
	if err != nil {
		a.logf("api: list users: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if users == nil {
		users = []*domain.User{}
	}
	writeJSON(w, http.StatusOK, users) // PasswordHash is json:"-"
}

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Username) == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if body.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}
	if body.Role != domain.RoleAdmin && body.Role != domain.RoleViewer {
		writeError(w, http.StatusBadRequest, "role must be admin or viewer")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		a.logf("api: hash password: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	user := &domain.User{Username: body.Username, PasswordHash: hash, Role: body.Role}
	if err := a.users.Create(r.Context(), user); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a user with that username already exists")
			return
		}
		a.logf("api: create user: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	target, err := a.users.GetByUsername(r.Context(), username)
	if err != nil {
		a.logf("api: delete user lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	callerID, _ := r.Context().Value(ctxKeyUserID).(uuid.UUID)
	if target.ID == callerID {
		writeError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	if err := a.users.Delete(r.Context(), username); err != nil {
		a.logf("api: delete user: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
```

- [x] **Step 4: Register the routes**

In `internal/api/api.go`, inside `authedMux()` after the secrets routes, add:

```go
	m.HandleFunc("GET /api/v1/users", a.handleListUsers)
	m.HandleFunc("POST /api/v1/users", a.handleCreateUser)
	m.HandleFunc("DELETE /api/v1/users/{username}", a.handleDeleteUser)
```

- [x] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/api/ -count=1`
Expected: PASS (all of the new tests plus the existing suite).

- [x] **Step 6: gofmt + vet + commit**

```bash
gofmt -l . && go vet ./...
git add internal/api
git commit -m "feat(sp4b): users management API (list/create/delete, self-delete guard)"
```

---

### Task 3: internal/web — embedded SPA serving with fallback + serve wiring

**Files:**
- Create: `internal/web/dist/.gitkeep` (empty file)
- Create: `internal/web/web.go`
- Create: `internal/web/web_test.go`
- Modify: `.gitignore`
- Modify: `cmd/fleet/main.go:918` (mount `web.Mount`)

- [x] **Step 1: Create the placeholder and .gitignore entries**

```bash
mkdir -p internal/web/dist
touch internal/web/dist/.gitkeep
```

Replace `.gitignore` (currently just `/fleet`) with:

```gitignore
/fleet
web/node_modules/
internal/web/dist/*
!internal/web/dist/.gitkeep
```

- [x] **Step 2: Write the failing tests**

Create `internal/web/web_test.go`:

```go
package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func builtFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        &fstest.MapFile{Data: []byte("<html><body>spa-index</body></html>")},
		"assets/app-abc.js": &fstest.MapFile{Data: []byte("console.log('app')")},
	}
}

// serveFS mounts dist alongside probe routes that must keep precedence.
func serveFS(t *testing.T, dist fs.FS, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("probe-ok"))
	})
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "api-404", http.StatusNotFound)
	})
	mux.HandleFunc("POST /webhooks/{plugin}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("webhook-ok"))
	})
	mountFS(mux, dist)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func body(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	b, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNotBuiltServesFallbackPage(t *testing.T) {
	// The real embedded FS holds only .gitkeep in a fresh clone — Mount must
	// serve the inline "UI not built" page, never error.
	mux := http.NewServeMux()
	Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := body(t, rec); !strings.Contains(got, "UI not built") {
		t.Errorf("body = %q, want the not-built page", got)
	}
}

func TestRootServesIndex(t *testing.T) {
	rec := serveFS(t, builtFS(), http.MethodGet, "/")
	if rec.Code != http.StatusOK || !strings.Contains(body(t, rec), "spa-index") {
		t.Fatalf("GET / = %d %q, want 200 index", rec.Code, body(t, rec))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", cc)
	}
}

func TestSPAFallbackServesIndex(t *testing.T) {
	for _, path := range []string{"/agents/123", "/duties", "/login", "/settings"} {
		rec := serveFS(t, builtFS(), http.MethodGet, path)
		if rec.Code != http.StatusOK || !strings.Contains(body(t, rec), "spa-index") {
			t.Errorf("GET %s = %d, want index fallback", path, rec.Code)
		}
	}
}

func TestAssetContentTypeAndCache(t *testing.T) {
	rec := serveFS(t, builtFS(), http.MethodGet, "/assets/app-abc.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", cc)
	}
}

func TestExplicitMountsKeepPrecedence(t *testing.T) {
	if got := body(t, serveFS(t, builtFS(), http.MethodGet, "/healthz")); got != "probe-ok" {
		t.Errorf("/healthz = %q, want probe-ok (SPA must not shadow it)", got)
	}
	rec := serveFS(t, builtFS(), http.MethodGet, "/api/v1/nonexistent")
	if rec.Code != http.StatusNotFound || !strings.Contains(body(t, rec), "api-404") {
		t.Errorf("/api/v1/* reached the SPA fallback: %d %q", rec.Code, body(t, rec))
	}
	if got := body(t, serveFS(t, builtFS(), http.MethodPost, "/webhooks/gitlab")); got != "webhook-ok" {
		t.Errorf("POST /webhooks/gitlab = %q, want webhook-ok (SPA 405 must not shadow it)", got)
	}
}

func TestDirectoryNotListed(t *testing.T) {
	rec := serveFS(t, builtFS(), http.MethodGet, "/assets/")
	if got := body(t, rec); strings.Contains(got, "app-abc.js") {
		t.Errorf("directory listing leaked: %q", got)
	}
}

func TestNonGetRejected(t *testing.T) {
	rec := serveFS(t, builtFS(), http.MethodPost, "/")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}
```

- [x] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/web/ -v`
Expected: FAIL to build — `Mount`/`mountFS` undefined.

- [x] **Step 4: Implement `internal/web/web.go`**

```go
// Package web serves the embedded operator SPA. The Vite build output lives
// in dist/ (untracked except .gitkeep); when the UI was not built, every SPA
// path serves an inline "UI not built" page so plain `go build` always works.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

const notBuiltPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>OfficeFleet</title></head>
<body style="font-family:sans-serif;background:#15171c;color:#e8eaed;display:grid;place-items:center;min-height:100vh;margin:0">
<div style="text-align:center"><h1>UI not built</h1>
<p>Run <code>make web</code> and rebuild the binary,<br>or use the dev server: <code>cd web &amp;&amp; npm run dev</code>.</p></div>
</body></html>`

// Mount registers the SPA on mux. The "/" pattern is ServeMux's
// lowest-precedence route, so explicit mounts (/api/v1, /webhooks, /healthz,
// and later /avatars) always win regardless of registration order.
func Mount(mux *http.ServeMux) {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // unreachable: embed guarantees dist exists
	}
	mountFS(mux, dist)
}

// mountFS is Mount with an injectable filesystem so tests can exercise both
// the built (fstest.MapFS) and not-built (real embed) modes.
func mountFS(mux *http.ServeMux, dist fs.FS) {
	index, err := fs.ReadFile(dist, "index.html")
	built := err == nil
	fileServer := http.FileServerFS(dist)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !built {
			serveHTML(w, []byte(notBuiltPage))
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" && name != "index.html" {
			if info, serr := fs.Stat(dist, name); serr == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite content-hashes asset filenames — safe to cache forever.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: client-routed paths (and the root) get index.html.
		serveHTML(w, index)
	})
}

func serveHTML(w http.ResponseWriter, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(page)
}
```

- [x] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/web/ -count=1 -v`
Expected: PASS (all 8 tests). `TestNotBuiltServesFallbackPage` passes because dist currently holds only `.gitkeep`.

- [x] **Step 6: Wire into `fleet serve`**

In `cmd/fleet/main.go`, add the import `"github.com/cbarraford/office-fleet/internal/web"` and change line 918 from:

```go
			httpSrv := &http.Server{Addr: addr, Handler: server.New(ingestor).Handler(apiSrv.Mount)}
```

to:

```go
			httpSrv := &http.Server{Addr: addr, Handler: server.New(ingestor).Handler(apiSrv.Mount, web.Mount)}
```

- [x] **Step 7: Full build + suite, then commit**

```bash
go build ./... && go test ./... -count=1
gofmt -l . && go vet ./...
git add internal/web .gitignore cmd/fleet/main.go
git commit -m "feat(sp4b): embedded SPA serving with not-built fallback; mount in serve"
```

Note: `git add internal/web` picks up `dist/.gitkeep` because of the `!internal/web/dist/.gitkeep` negation. Verify with `git status --short` that `.gitkeep` is staged.

---

### Task 4: web/ Vite scaffold + Makefile + theme CSS

**Files:**
- Create: `web/package.json`, `web/tsconfig.json`, `web/vite.config.ts`, `web/index.html`
- Create: `web/src/main.tsx` (placeholder bootstrap — replaced in Task 8)
- Create: `web/src/styles.css` (the complete theme; later tasks only consume classes)
- Create: `web/package-lock.json` (generated by `npm install`)
- Create: `Makefile`

There are no unit tests in this task; the verification steps are the build gates themselves (this is infrastructure — the rest of the plan depends on these gates passing).

- [x] **Step 1: Create `web/package.json`**

```json
{
  "name": "officefleet-web",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc --noEmit && vite build",
    "check": "tsc --noEmit && vitest run",
    "test": "vitest run"
  },
  "dependencies": {
    "react": "^19.1.0",
    "react-dom": "^19.1.0",
    "react-router-dom": "^7.6.0"
  },
  "devDependencies": {
    "@types/react": "^19.1.0",
    "@types/react-dom": "^19.1.0",
    "@vitejs/plugin-react": "^4.5.0",
    "jsdom": "^26.1.0",
    "typescript": "^5.8.0",
    "vite": "^6.3.0",
    "vitest": "^3.2.0"
  }
}
```

- [x] **Step 2: Create `web/tsconfig.json`**

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "noEmit": true,
    "skipLibCheck": true,
    "isolatedModules": true,
    "types": ["vite/client"]
  },
  "include": ["src"]
}
```

- [x] **Step 3: Create `web/vite.config.ts`**

```ts
/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/web/dist',
    // .gitkeep must survive builds — the worktree stays clean (see SP4b spec §3).
    emptyOutDir: false,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/avatars': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
    },
  },
  test: {
    environment: 'jsdom',
  },
})
```

- [x] **Step 4: Create `web/index.html`**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>OfficeFleet</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

- [x] **Step 5: Create the placeholder `web/src/main.tsx`**

(Task 8 replaces this with the real router; this keeps `vite build` green from day one.)

```tsx
import { createRoot } from 'react-dom/client'
import './styles.css'

createRoot(document.getElementById('root')!).render(<h1>OfficeFleet</h1>)
```

- [x] **Step 6: Create `web/src/styles.css` (the complete theme)**

```css
/* OfficeFleet operator UI — single dark theme. */
:root {
  --bg: #15171c;
  --bg-panel: #1d2026;
  --bg-raised: #252932;
  --border: #32363f;
  --text: #e8eaed;
  --text-dim: #9aa0a6;
  --accent: #61afef;
  --green: #98c379;
  --red: #e06c75;
  --yellow: #e5c07b;
  --purple: #c678dd;
  --radius: 8px;
  --sidebar-w: 200px;
}

* { box-sizing: border-box; }
body {
  margin: 0;
  background: var(--bg);
  color: var(--text);
  font: 14px/1.5 -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
}
a { color: var(--accent); text-decoration: none; }
code, pre, textarea.mono, .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }

/* Layout shell */
.shell { display: flex; min-height: 100vh; }
.sidebar {
  width: var(--sidebar-w); flex-shrink: 0;
  background: var(--bg-panel); border-right: 1px solid var(--border);
  padding: 16px 0; display: flex; flex-direction: column; gap: 2px;
  position: sticky; top: 0; height: 100vh;
}
.sidebar .brand { font-weight: 700; font-size: 16px; padding: 0 20px 16px; }
.sidebar nav { display: flex; flex-direction: column; flex: 1; }
.sidebar nav a {
  color: var(--text-dim); padding: 9px 20px; border-left: 2px solid transparent;
}
.sidebar nav a:hover { color: var(--text); }
.sidebar nav a.active { color: var(--text); border-left-color: var(--accent); background: var(--bg-raised); }
.sidebar .session { padding: 12px 20px; border-top: 1px solid var(--border); font-size: 12px; color: var(--text-dim); }
.main { flex: 1; padding: 24px 28px; min-width: 0; }
.main h1 { font-size: 20px; margin: 0 0 18px; }

/* Cards */
.card {
  background: var(--bg-panel); border: 1px solid var(--border);
  border-radius: var(--radius); padding: 16px;
}
.card h2 { font-size: 14px; margin: 0 0 12px; color: var(--text-dim); font-weight: 600; }
.grid { display: grid; gap: 14px; }
.grid.cols-3 { grid-template-columns: repeat(3, 1fr); }
.grid.cards { grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); }
@media (max-width: 900px) { .grid.cols-3 { grid-template-columns: 1fr; } }

/* Stat blocks */
.stat { text-align: center; }
.stat .num { font-size: 26px; font-weight: 700; }
.stat .label { color: var(--text-dim); font-size: 12px; }

/* Tables */
table.tbl { width: 100%; border-collapse: collapse; }
.tbl th {
  text-align: left; color: var(--text-dim); font-weight: 600; font-size: 12px;
  padding: 8px 10px; border-bottom: 1px solid var(--border);
}
.tbl td { padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: top; }
.tbl tr.clickable:hover { background: var(--bg-raised); cursor: pointer; }
.tbl tr.highlight { outline: 1px solid var(--accent); }

/* Badges & pills */
.badge {
  display: inline-block; padding: 1px 8px; border-radius: 10px;
  font-size: 11px; font-weight: 600; border: 1px solid var(--border);
  color: var(--text-dim); background: var(--bg-raised);
}
.badge.warn { color: var(--yellow); border-color: var(--yellow); }
.badge.ok { color: var(--green); border-color: var(--green); }
.pill {
  display: inline-block; padding: 1px 10px; border-radius: 10px;
  font-size: 11px; font-weight: 700; text-transform: uppercase;
}
.pill.succeeded { background: rgba(152, 195, 121, 0.15); color: var(--green); }
.pill.failed { background: rgba(224, 108, 117, 0.15); color: var(--red); }
.pill.running { background: rgba(97, 175, 239, 0.15); color: var(--accent); }
.pill.queued { background: rgba(229, 192, 123, 0.15); color: var(--yellow); }
.pill.skipped { background: rgba(154, 160, 166, 0.15); color: var(--text-dim); }
.pill.pending { background: rgba(229, 192, 123, 0.15); color: var(--yellow); }
.pill.dispatched { background: rgba(152, 195, 121, 0.15); color: var(--green); }

/* Avatar */
.avatar { border-radius: 50%; object-fit: cover; flex-shrink: 0; }
.avatar-initials {
  display: flex; align-items: center; justify-content: center;
  color: #fff; font-weight: 700; border-radius: 50%;
}

/* Buttons & forms */
button, .btn {
  background: var(--bg-raised); color: var(--text); border: 1px solid var(--border);
  border-radius: 6px; padding: 6px 14px; font-size: 13px; cursor: pointer;
}
button:hover:not(:disabled) { border-color: var(--accent); }
button:disabled { opacity: 0.45; cursor: not-allowed; }
button.primary { background: var(--accent); border-color: var(--accent); color: #10131a; font-weight: 600; }
button.danger { color: var(--red); }
button.danger.confirming { background: var(--red); color: #10131a; border-color: var(--red); }
button.small { padding: 3px 10px; font-size: 12px; }
input, select, textarea {
  background: var(--bg); color: var(--text); border: 1px solid var(--border);
  border-radius: 6px; padding: 7px 10px; font-size: 13px; width: 100%;
}
input:focus, select:focus, textarea:focus { outline: none; border-color: var(--accent); }
label.field { display: block; margin-bottom: 12px; }
label.field span { display: block; font-size: 12px; color: var(--text-dim); margin-bottom: 4px; }
.form-error { color: var(--red); font-size: 13px; margin: 8px 0; }
.row { display: flex; gap: 10px; align-items: center; }
.row.between { justify-content: space-between; }
.row.wrap { flex-wrap: wrap; }
.spacer { flex: 1; }

/* Modal */
.modal-overlay {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.55);
  display: grid; place-items: center; z-index: 100;
}
.modal {
  background: var(--bg-panel); border: 1px solid var(--border); border-radius: var(--radius);
  padding: 20px; width: min(560px, 92vw); max-height: 86vh; overflow-y: auto;
}
.modal h2 { margin: 0 0 16px; font-size: 16px; }

/* Drawer (run detail) */
.drawer {
  position: fixed; top: 0; right: 0; bottom: 0; width: min(640px, 92vw);
  background: var(--bg-panel); border-left: 1px solid var(--border);
  padding: 20px; overflow-y: auto; z-index: 90;
}
.drawer h2 { margin: 0 0 14px; font-size: 16px; }
.kv { display: grid; grid-template-columns: 140px 1fr; gap: 4px 12px; font-size: 13px; }
.kv dt { color: var(--text-dim); }
.kv dd { margin: 0; word-break: break-word; }

/* Collapsible (JsonView / prompts / transcript) */
details.fold { border: 1px solid var(--border); border-radius: 6px; margin: 8px 0; }
details.fold summary { padding: 7px 10px; cursor: pointer; color: var(--text-dim); font-size: 12px; font-weight: 600; }
details.fold pre {
  margin: 0; padding: 10px; border-top: 1px solid var(--border);
  font-size: 12px; overflow-x: auto; white-space: pre-wrap; word-break: break-word;
  max-height: 360px; overflow-y: auto;
}

/* Tabs */
.tabs { display: flex; gap: 4px; border-bottom: 1px solid var(--border); margin-bottom: 16px; }
.tabs button {
  background: none; border: none; border-bottom: 2px solid transparent; border-radius: 0;
  color: var(--text-dim); padding: 8px 14px;
}
.tabs button.active { color: var(--text); border-bottom-color: var(--accent); }

/* Toasts */
.toasts { position: fixed; bottom: 16px; right: 16px; display: flex; flex-direction: column; gap: 8px; z-index: 200; }
.toast {
  background: var(--bg-raised); border: 1px solid var(--border); border-left: 3px solid var(--accent);
  border-radius: 6px; padding: 10px 14px; max-width: 360px; font-size: 13px;
}
.toast.error { border-left-color: var(--red); }

/* Feed */
.feed { display: flex; flex-direction: column; gap: 6px; max-height: 420px; overflow-y: auto; }
.feed .item { font-size: 13px; padding: 6px 8px; border-radius: 6px; background: var(--bg-raised); }
.feed .item .when { color: var(--text-dim); font-size: 11px; }
.reconnecting { color: var(--yellow); font-size: 12px; }

/* Login */
.login-wrap { min-height: 100vh; display: grid; place-items: center; }
.login-card { width: min(360px, 92vw); }

/* Misc */
.dim { color: var(--text-dim); }
.empty { color: var(--text-dim); text-align: center; padding: 28px 0; }
.mb { margin-bottom: 14px; }
.mt { margin-top: 14px; }
```

- [x] **Step 7: Install dependencies (generates `web/package-lock.json`)**

```bash
cd web && NODE_OPTIONS= npm install
```

Expected: succeeds, `web/package-lock.json` created, `web/node_modules/` populated (and ignored by git — verify `git status --short` shows no `node_modules` entries).

- [x] **Step 8: Run the build gate**

```bash
cd web && NODE_OPTIONS= npm run build
```

Expected: `tsc --noEmit` passes, `vite build` writes `index.html` + `assets/*` into `../internal/web/dist`. Then verify the worktree invariant:

```bash
git status --short
```

Expected: only intended new files (`web/*`, `Makefile`); **no `internal/web/dist/*` entries** (ignored) and `.gitkeep` still present (`ls internal/web/dist/.gitkeep`).

- [x] **Step 9: Verify the embed picks up the build**

```bash
go test ./internal/web/ -count=1
```

Expected: **`TestNotBuiltServesFallbackPage` now FAILS** (dist genuinely contains an index.html, so the real embed serves the SPA, not the fallback page). This proves the embed works — but the test must pass in both states. Fix the test to assert mode-appropriately — replace `TestNotBuiltServesFallbackPage` in `internal/web/web_test.go` with:

```go
func TestEmbeddedMountServes(t *testing.T) {
	// Works in BOTH repo states: fresh clone (dist has only .gitkeep — the
	// inline not-built page) and after `make web` (the real index.html).
	mux := http.NewServeMux()
	Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := body(t, rec)
	if !strings.Contains(got, "UI not built") && !strings.Contains(got, "<div id=\"root\">") {
		t.Errorf("body = %q, want either the not-built page or the built index", got)
	}
}
```

(The not-built *behavior* stays covered: `mountFS` with an empty `fstest.MapFS` — add this companion test in the same file:)

```go
func TestNotBuiltFallbackPage(t *testing.T) {
	rec := serveFS(t, fstest.MapFS{}, http.MethodGet, "/agents/123")
	if rec.Code != http.StatusOK || !strings.Contains(body(t, rec), "UI not built") {
		t.Errorf("not-built mode = %d %q, want the inline page", rec.Code, body(t, rec))
	}
}
```

Re-run: `go test ./internal/web/ -count=1` — expected PASS.

- [x] **Step 10: Create the `Makefile`** (repo root)

Recipe lines MUST be indented with real TAB characters (make rejects spaces).

```make
.PHONY: web web-clean build test

# Build the SPA into internal/web/dist (only .gitkeep is tracked there).
web: web/node_modules
	cd web && npm run build

web/node_modules: web/package-lock.json
	cd web && npm ci
	@touch web/node_modules

# Wipe build output back to the committed .gitkeep (hashed assets accumulate
# because vite runs with emptyOutDir: false).
web-clean:
	find internal/web/dist -mindepth 1 ! -name .gitkeep -delete

build: web
	go build -o fleet ./cmd/fleet

test: web/node_modules
	go test ./...
	cd web && npm run check
```

Verify (with the NODE_OPTIONS caveat — run `NODE_OPTIONS= make web-clean && NODE_OPTIONS= make build`):
- `make web-clean` leaves exactly `.gitkeep` in `internal/web/dist/` (`ls -A internal/web/dist`).
- `make build` rebuilds the UI and produces `./fleet` (gitignored).
- `git status --short` — still no dist or binary entries.

- [x] **Step 11: Commit**

```bash
git add web/package.json web/package-lock.json web/tsconfig.json web/vite.config.ts web/index.html web/src Makefile
git commit -m "feat(sp4b): Vite scaffold, theme CSS, Makefile build pipeline"
```

(`git status` must show no unintended files; `web/node_modules` and `internal/web/dist/*` stay untracked.)

---

### Task 5: API types + typed fetch client (vitest TDD)

**Files:**
- Create: `web/src/api/types.ts`
- Create: `web/src/api/client.ts`
- Test: `web/src/api/client.test.ts`

- [x] **Step 1: Create `web/src/api/types.ts`** (TS mirrors of the snake_case wire format — see "API contract facts" above)

```ts
// TypeScript mirrors of the /api/v1 JSON wire format (snake_case, see
// internal/domain/types.go). Optional pointer fields are `| null`.

export interface BackendRef {
  name: string
  model?: string
  effort?: string
}

export interface Agent {
  id: string
  name: string
  role: string
  system_prompt: string
  default_backend: BackendRef
  enabled: boolean
  avatar_url: string | null
  hired_at: string | null
  created_at: string
  updated_at: string
}

export interface OutputActionType {
  plugin: string
  action: string
}

export interface Duty {
  id: string
  name: string
  role: string
  description: string
  trigger_kinds: string[] | null
  prompt: string
  required_tools: string[] | null
  output_actions: OutputActionType[] | null
  config_schema: Record<string, unknown> | null
  backend: BackendRef | null
  created_at: string
  updated_at: string
}

export interface TriggerConfig {
  kind: string
  schedule?: string
  filter?: Record<string, unknown>
}

export interface OutputBinding {
  plugin: string
  action: string
  params: Record<string, unknown> | null
}

export interface Assignment {
  id: string
  agent_id: string
  duty_id: string
  enabled: boolean
  trigger: TriggerConfig
  outputs: OutputBinding[] | null
  config: Record<string, unknown> | null
  backend: BackendRef | null
  task_prompt_override: string | null
  extra_instructions: string | null
  created_at: string
  updated_at: string
}

export interface LLMResult {
  status: number
  summary: string
  output: Record<string, unknown> | null
  transcript: string
  tokens: number
  cost: number
}

export interface OutputDelivery {
  plugin: string
  action: string
  params: Record<string, unknown> | null
  status: string
  error?: string
}

export type RunStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'skipped'

export interface Run {
  id: string
  assignment_id: string
  agent_id: string
  duty_id: string
  trigger_kind: string
  event_id: string | null
  rendered_system_prompt: string
  rendered_prompt: string
  llm_result: LLMResult | null
  outputs_delivered: OutputDelivery[] | null
  status: RunStatus
  tokens: number
  cost: number
  started_at: string
  finished_at: string | null
  error: string | null
}

export interface FleetEvent {
  id: string
  source_plugin: string
  event_type: string
  payload_raw: unknown
  payload_norm: Record<string, unknown> | null
  identity: string
  dedup_key: string
  status: 'pending' | 'dispatched'
  received_at: string
  dispatched_at: string | null
}

export interface AgentStats {
  agent_id: string
  total_runs: number
  runs_last_30d: number
  success_rate: number
  skip_rate: number
  total_tokens: number
  total_cost_usd: number
  tokens_last_30d: number
  cost_last_30d_usd: number
  outputs_delivered: number
  outputs_last_30d: number
  avg_run_duration_s: number
  last_run_at: string | null
}

// GET /api/v1/agents/{id} returns this envelope (stats is null when the
// stats query failed — detail still loads).
export interface AgentDetailResponse {
  agent: Agent
  stats: AgentStats | null
}

export interface BackendView {
  name: string
  kind: string
  auth_mode: string
  model?: string
  default_effort?: string
}

export interface SecretInfo {
  name: string
  encrypted: boolean
}

export interface User {
  id: string
  username: string
  role: 'admin' | 'viewer'
  created_at: string
  updated_at: string
}

export interface Me {
  username: string
  role: 'admin' | 'viewer'
}

// SSE payload from GET /api/v1/stream (unnamed `data:` messages).
export interface StreamMsg {
  event: 'run_started' | 'run_finished'
  id: string
  assignment_id: string
  agent_id: string
  duty_id: string
  trigger_kind: string
  status: RunStatus
  tokens: number
  cost: number
}
```

- [x] **Step 2: Write the failing client tests**

Create `web/src/api/client.test.ts`:

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, configureClient } from './client'

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('api client', () => {
  const onUnauthorized = vi.fn()

  beforeEach(() => {
    configureClient({ onUnauthorized })
  })

  afterEach(() => {
    vi.unstubAllGlobals() // restoreAllMocks does NOT undo stubGlobal
    vi.restoreAllMocks()
    onUnauthorized.mockReset()
  })

  it('returns parsed JSON on success', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { role: 'admin' }))
    vi.stubGlobal('fetch', fetchMock)

    const out = await api.get<{ role: string }>('/api/v1/me')
    expect(out.role).toBe('admin')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/me',
      expect.objectContaining({ method: 'GET', credentials: 'same-origin' }),
    )
  })

  it('sends JSON bodies with content-type', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, { ok: true }))
    vi.stubGlobal('fetch', fetchMock)

    await api.post('/api/v1/users', { username: 'x' })
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(init.method).toBe('POST')
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect(init.body).toBe(JSON.stringify({ username: 'x' }))
  })

  it('throws ApiError with the server error envelope message', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(409, { error: 'duplicate' })))

    const err = await api.post('/api/v1/duties', {}).then(
      () => null,
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).message).toBe('duplicate')
  })

  it('falls back to a generic message when the error body is not JSON', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('boom', { status: 500 })),
    )

    const err = await api.get('/api/v1/agents').then(
      () => null,
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(500)
    expect((err as ApiError).message).toBe('request failed (500)')
  })

  it('invokes onUnauthorized and throws on 401', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(401, { error: 'authentication required' })),
    )

    await expect(api.get('/api/v1/me')).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).toHaveBeenCalledOnce()
  })
})
```

- [x] **Step 3: Run tests to verify they fail**

```bash
cd web && NODE_OPTIONS= npx vitest run src/api/client.test.ts
```

Expected: FAIL — `./client` module not found.

- [x] **Step 4: Implement `web/src/api/client.ts`**

```ts
export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

interface ClientConfig {
  onUnauthorized: () => void
}

// Default 401 behavior: bounce to /login preserving the current location —
// except on /login itself, where the error must surface in the form.
let cfg: ClientConfig = {
  onUnauthorized: () => {
    if (!window.location.pathname.startsWith('/login')) {
      const next = encodeURIComponent(window.location.pathname + window.location.search)
      window.location.assign(`/login?next=${next}`)
    }
  },
}

export function configureClient(overrides: Partial<ClientConfig>): void {
  cfg = { ...cfg, ...overrides }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin' }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  const res = await fetch(path, init)
  if (res.status === 401) {
    cfg.onUnauthorized()
    throw new ApiError(401, 'authentication required')
  }
  if (!res.ok) {
    let msg = `request failed (${res.status})`
    try {
      const data: unknown = await res.json()
      if (data && typeof data === 'object' && typeof (data as { error?: unknown }).error === 'string') {
        msg = (data as { error: string }).error
      }
    } catch {
      // non-JSON error body: keep the generic message
    }
    throw new ApiError(res.status, msg)
  }
  return res.json() as Promise<T>
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  patch: <T>(path: string, body: unknown) => request<T>('PATCH', path, body),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}
```

- [x] **Step 5: Run tests to verify they pass**

```bash
cd web && NODE_OPTIONS= npx vitest run src/api/client.test.ts
```

Expected: PASS (5 tests). Also run the typecheck: `cd web && NODE_OPTIONS= npx tsc --noEmit` — clean.

- [x] **Step 6: Commit**

```bash
git add web/src/api/types.ts web/src/api/client.ts web/src/api/client.test.ts
git commit -m "feat(sp4b): typed API client and wire-format mirrors with vitest coverage"
```

---

### Task 6: SSE wrapper with reconnect + refetch signal (vitest TDD)

**Files:**
- Create: `web/src/api/sse.ts`
- Test: `web/src/api/sse.test.ts`

- [x] **Step 1: Write the failing tests**

Create `web/src/api/sse.test.ts`:

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { connectStream, type EventSourceLike } from './sse'

class FakeEventSource implements EventSourceLike {
  static instances: FakeEventSource[] = []
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  closed = false

  constructor(public url: string) {
    FakeEventSource.instances.push(this)
  }

  close(): void {
    this.closed = true
  }
}

const factory = (url: string) => new FakeEventSource(url)
const latest = () => FakeEventSource.instances[FakeEventSource.instances.length - 1]

describe('connectStream', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    FakeEventSource.instances = []
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('connects to /api/v1/stream and reports status on open', () => {
    const onStatus = vi.fn()
    connectStream({ onStatus }, factory)
    expect(latest().url).toBe('/api/v1/stream')
    latest().onopen?.()
    expect(onStatus).toHaveBeenCalledWith(true)
  })

  it('parses messages and dispatches them', () => {
    const onMessage = vi.fn()
    connectStream({ onMessage }, factory)
    latest().onopen?.()
    latest().onmessage?.({ data: '{"event":"run_started","id":"r1","status":"running"}' })
    expect(onMessage).toHaveBeenCalledWith(expect.objectContaining({ event: 'run_started', id: 'r1' }))
  })

  it('ignores malformed payloads without dying', () => {
    const onMessage = vi.fn()
    connectStream({ onMessage }, factory)
    latest().onopen?.()
    latest().onmessage?.({ data: 'not-json{' })
    expect(onMessage).not.toHaveBeenCalled()
  })

  it('reconnects with doubling backoff and fires onReconnect on re-open', () => {
    const onStatus = vi.fn()
    const onReconnect = vi.fn()
    connectStream({ onStatus, onReconnect }, factory)

    latest().onopen?.() // first open: no onReconnect
    expect(onReconnect).not.toHaveBeenCalled()

    latest().onerror?.() // drop
    expect(onStatus).toHaveBeenLastCalledWith(false)
    expect(FakeEventSource.instances).toHaveLength(1)

    vi.advanceTimersByTime(1000) // first retry after 1s
    expect(FakeEventSource.instances).toHaveLength(2)

    latest().onerror?.() // still down: next delay doubles to 2s
    vi.advanceTimersByTime(1999)
    expect(FakeEventSource.instances).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(FakeEventSource.instances).toHaveLength(3)

    latest().onopen?.() // back: refetch signal fires, delay resets
    expect(onReconnect).toHaveBeenCalledOnce()
    expect(onStatus).toHaveBeenLastCalledWith(true)

    latest().onerror?.()
    vi.advanceTimersByTime(1000) // reset delay: 1s again
    expect(FakeEventSource.instances).toHaveLength(4)
  })

  it('caps the backoff at 30s', () => {
    connectStream({}, factory)
    latest().onopen?.()
    // 1, 2, 4, 8, 16, 32→30 …
    for (let i = 0; i < 6; i++) {
      latest().onerror?.()
      vi.advanceTimersByTime(30000)
    }
    const count = FakeEventSource.instances.length
    latest().onerror?.()
    vi.advanceTimersByTime(29999)
    expect(FakeEventSource.instances).toHaveLength(count)
    vi.advanceTimersByTime(1)
    expect(FakeEventSource.instances).toHaveLength(count + 1)
  })

  it('stop() closes the source and cancels pending retries', () => {
    const stop = connectStream({}, factory)
    latest().onopen?.()
    latest().onerror?.()
    stop()
    vi.advanceTimersByTime(60000)
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(latest().closed).toBe(true)
  })
})
```

- [x] **Step 2: Run tests to verify they fail**

```bash
cd web && NODE_OPTIONS= npx vitest run src/api/sse.test.ts
```

Expected: FAIL — `./sse` module not found.

- [x] **Step 3: Implement `web/src/api/sse.ts`**

```ts
import type { StreamMsg } from './types'

// The SSE feed is advisory (the server drops messages for slow consumers and
// the run table is truth), so every reconnect fires onReconnect as a
// "refetch your data" signal.

export interface StreamHandlers {
  onMessage?: (msg: StreamMsg) => void
  onStatus?: (connected: boolean) => void
  onReconnect?: () => void
}

export interface EventSourceLike {
  onopen: (() => void) | null
  onmessage: ((ev: { data: string }) => void) | null
  onerror: (() => void) | null
  close(): void
}

export type EventSourceFactory = (url: string) => EventSourceLike

const INITIAL_DELAY_MS = 1000
const MAX_DELAY_MS = 30000

export function connectStream(
  handlers: StreamHandlers,
  // EventSource's native handler signatures are wider (MessageEvent, this-
  // typed) than the minimal shape we drive, so a direct assignment fails
  // under strictFunctionTypes — the double assertion is deliberate.
  makeSource: EventSourceFactory = (url) => new EventSource(url) as unknown as EventSourceLike,
): () => void {
  let source: EventSourceLike | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let delay = INITIAL_DELAY_MS
  let everOpened = false
  let stopped = false

  const open = () => {
    source = makeSource('/api/v1/stream')
    source.onopen = () => {
      delay = INITIAL_DELAY_MS
      handlers.onStatus?.(true)
      if (everOpened) handlers.onReconnect?.()
      everOpened = true
    }
    source.onmessage = (ev) => {
      try {
        handlers.onMessage?.(JSON.parse(ev.data) as StreamMsg)
      } catch {
        // malformed payload: ignore (feed is advisory)
      }
    }
    source.onerror = () => {
      handlers.onStatus?.(false)
      source?.close()
      if (stopped) return
      timer = setTimeout(open, delay)
      delay = Math.min(delay * 2, MAX_DELAY_MS)
    }
  }

  open()
  return () => {
    stopped = true
    if (timer) clearTimeout(timer)
    source?.close()
  }
}
```

- [x] **Step 4: Run tests to verify they pass**

```bash
cd web && NODE_OPTIONS= npx vitest run src/api/
```

Expected: PASS (client + sse suites). Typecheck: `cd web && NODE_OPTIONS= npx tsc --noEmit` — clean.

- [x] **Step 5: Commit**

```bash
git add web/src/api/sse.ts web/src/api/sse.test.ts
git commit -m "feat(sp4b): SSE wrapper with backoff reconnect and refetch signal"
```

---

### Task 7: Shared components, formatting helpers, toast bus

**Files:**
- Create: `web/src/lib/format.ts`, `web/src/lib/toast.ts`
- Create: `web/src/components/Card.tsx`, `Table.tsx`, `Modal.tsx`, `Badge.tsx`, `StatusPill.tsx`, `AvatarBubble.tsx`, `ConfirmButton.tsx`, `JsonView.tsx`, `Toasts.tsx`

No vitest here (component tests are explicitly deferred in the spec §8); the gate is `tsc --noEmit && vite build`. The placeholder `main.tsx` doesn't import these yet — `noUnusedLocals` only applies within modules, unimported modules still typecheck.

- [x] **Step 1: Create `web/src/lib/format.ts`**

```ts
export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

export function fmtDateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

export function fmtCost(usd: number): string {
  if (usd === 0) return '$0'
  return `$${usd.toFixed(4)}`
}

export function fmtPercent(ratio: number): string {
  return `${Math.round(ratio * 100)}%`
}

// True when the ISO timestamp falls on the local calendar day of `now`.
export function isToday(iso: string, now: Date = new Date()): boolean {
  const d = new Date(iso)
  return (
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate()
  )
}
```

- [x] **Step 2: Create `web/src/lib/toast.ts`**

```ts
// Minimal toast bus: pages call toast(); the <Toasts/> host renders them.
export interface ToastItem {
  id: number
  kind: 'error' | 'info'
  text: string
}

let nextID = 1
let items: ToastItem[] = []
let listener: ((items: ToastItem[]) => void) | null = null

export function toast(kind: ToastItem['kind'], text: string): void {
  const item = { id: nextID++, kind, text }
  items = [...items, item]
  listener?.(items)
  setTimeout(() => dismiss(item.id), 6000)
}

export function dismiss(id: number): void {
  items = items.filter((t) => t.id !== id)
  listener?.(items)
}

export function subscribe(fn: (items: ToastItem[]) => void): () => void {
  listener = fn
  fn(items)
  return () => {
    if (listener === fn) listener = null
  }
}
```

- [x] **Step 3: Create the components**

`web/src/components/Toasts.tsx`:

```tsx
import { useEffect, useState } from 'react'
import { dismiss, subscribe, type ToastItem } from '../lib/toast'

export default function Toasts() {
  const [items, setItems] = useState<ToastItem[]>([])
  useEffect(() => subscribe(setItems), [])
  if (items.length === 0) return null
  return (
    <div className="toasts">
      {items.map((t) => (
        <div key={t.id} className={`toast ${t.kind}`} onClick={() => dismiss(t.id)}>
          {t.text}
        </div>
      ))}
    </div>
  )
}
```

`web/src/components/Card.tsx`:

```tsx
import type { CSSProperties, ReactNode } from 'react'

export default function Card({
  title,
  children,
  className = '',
  style,
}: {
  title?: string
  children: ReactNode
  className?: string
  style?: CSSProperties
}) {
  return (
    <div className={`card ${className}`} style={style}>
      {title && <h2>{title}</h2>}
      {children}
    </div>
  )
}
```

`web/src/components/Table.tsx`:

```tsx
import type { ReactNode } from 'react'

export interface Column<T> {
  header: string
  render: (row: T) => ReactNode
}

export default function Table<T>({
  columns,
  rows,
  rowKey,
  onRowClick,
  rowClass,
  empty = 'Nothing here yet.',
}: {
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T) => string
  onRowClick?: (row: T) => void
  rowClass?: (row: T) => string
  empty?: string
}) {
  if (rows.length === 0) return <div className="empty">{empty}</div>
  return (
    <table className="tbl">
      <thead>
        <tr>
          {columns.map((c) => (
            <th key={c.header}>{c.header}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr
            key={rowKey(row)}
            className={`${onRowClick ? 'clickable ' : ''}${rowClass ? rowClass(row) : ''}`}
            onClick={onRowClick ? () => onRowClick(row) : undefined}
          >
            {columns.map((c) => (
              <td key={c.header}>{c.render(row)}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}
```

`web/src/components/Modal.tsx`:

```tsx
import { useEffect, type ReactNode } from 'react'

export default function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>{title}</h2>
        {children}
      </div>
    </div>
  )
}
```

`web/src/components/Badge.tsx`:

```tsx
export default function Badge({ text, kind = '' }: { text: string; kind?: '' | 'warn' | 'ok' }) {
  return <span className={`badge ${kind}`}>{text}</span>
}
```

`web/src/components/StatusPill.tsx`:

```tsx
export default function StatusPill({ status }: { status: string }) {
  return <span className={`pill ${status}`}>{status}</span>
}
```

`web/src/components/AvatarBubble.tsx`:

```tsx
const PALETTE = ['#e06c75', '#e5c07b', '#98c379', '#56b6c2', '#61afef', '#c678dd', '#d19a66', '#be8c6c']

// FNV-1a over the name picks a stable palette color (mirrors the planned
// SP4c server-side fallback behavior).
function hashName(name: string): number {
  let h = 2166136261
  for (const ch of name) {
    h ^= ch.codePointAt(0) ?? 0
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  const first = [...parts[0]][0] ?? '?'
  if (parts.length === 1) return first.toUpperCase()
  const last = [...parts[parts.length - 1]][0] ?? ''
  return (first + last).toUpperCase()
}

export default function AvatarBubble({ name, url, size = 40 }: { name: string; url?: string | null; size?: number }) {
  if (url) return <img className="avatar" src={url} alt={name} width={size} height={size} />
  return (
    <div
      className="avatar avatar-initials"
      style={{
        width: size,
        height: size,
        background: PALETTE[hashName(name) % PALETTE.length],
        fontSize: Math.round(size * 0.4),
      }}
    >
      {initials(name)}
    </div>
  )
}
```

`web/src/components/ConfirmButton.tsx`:

```tsx
import { useEffect, useRef, useState } from 'react'

// Two-step destructive button: first click arms it ("Confirm?"), second
// click within 3s fires onConfirm.
export default function ConfirmButton({
  label,
  onConfirm,
  disabled = false,
  title,
}: {
  label: string
  onConfirm: () => void
  disabled?: boolean
  title?: string
}) {
  const [arming, setArming] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])

  const click = () => {
    if (!arming) {
      setArming(true)
      timer.current = setTimeout(() => setArming(false), 3000)
      return
    }
    if (timer.current) clearTimeout(timer.current)
    setArming(false)
    onConfirm()
  }

  return (
    <button className={`danger small ${arming ? 'confirming' : ''}`} onClick={click} disabled={disabled} title={title}>
      {arming ? 'Confirm?' : label}
    </button>
  )
}
```

`web/src/components/JsonView.tsx`:

```tsx
// Collapsible viewer for payloads/transcripts. Pass text for prose
// (transcripts, prompts) or value for JSON structures.
export default function JsonView({
  label,
  value,
  text,
  open = false,
}: {
  label: string
  value?: unknown
  text?: string
  open?: boolean
}) {
  const content = text !== undefined ? text : JSON.stringify(value, null, 2)
  if (content === undefined || content === 'null' || content === '') return null
  return (
    <details className="fold" open={open}>
      <summary>{label}</summary>
      <pre>{content}</pre>
    </details>
  )
}
```

- [x] **Step 4: Gate + commit**

```bash
cd web && NODE_OPTIONS= npm run build
```

Expected: tsc + vite build pass.

```bash
git add web/src/lib web/src/components
git commit -m "feat(sp4b): shared UI components, toast bus, format helpers"
```

---

### Task 8: App shell, router, session context, Login page

**Files:**
- Modify: `web/src/main.tsx` (replace the placeholder)
- Create: `web/src/App.tsx`
- Create: `web/src/pages/Login.tsx`
- Create: `web/src/pages/Dashboard.tsx`, `Agents.tsx`, `AgentDetail.tsx`, `Duties.tsx`, `Settings.tsx` — **stubs** so the router compiles (each page is fleshed out in Tasks 9–13)

- [x] **Step 1: Create the page stubs**

Each of the five files gets the same shape (shown for `web/src/pages/Dashboard.tsx`; repeat with the matching component name and heading for `Agents`, `AgentDetail`, `Duties`, `Settings`):

```tsx
export default function Dashboard() {
  return <h1>Dashboard</h1>
}
```

- [x] **Step 2: Replace `web/src/main.tsx` with the real router**

```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Route, Routes } from 'react-router-dom'
import App from './App'
import AgentDetail from './pages/AgentDetail'
import Agents from './pages/Agents'
import Dashboard from './pages/Dashboard'
import Duties from './pages/Duties'
import Login from './pages/Login'
import Settings from './pages/Settings'
import './styles.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route element={<App />}>
          <Route path="/" element={<Dashboard />} />
          <Route path="/agents" element={<Agents />} />
          <Route path="/agents/:id" element={<AgentDetail />} />
          <Route path="/duties" element={<Duties />} />
          <Route path="/settings" element={<Settings />} />
        </Route>
      </Routes>
    </BrowserRouter>
  </StrictMode>,
)
```

- [x] **Step 3: Create `web/src/App.tsx`** (layout shell + session guard)

```tsx
import { createContext, useContext, useEffect, useState } from 'react'
import { NavLink, Outlet } from 'react-router-dom'
import { api } from './api/client'
import type { Me } from './api/types'
import Toasts from './components/Toasts'

interface Session {
  me: Me
  isAdmin: boolean
}

const SessionContext = createContext<Session | null>(null)

// useSession is only rendered under <App/>, which never renders children
// before /me resolves — the assertion is safe.
export function useSession(): Session {
  const s = useContext(SessionContext)
  if (!s) throw new Error('useSession outside <App/>')
  return s
}

export default function App() {
  const [me, setMe] = useState<Me | null>(null)

  useEffect(() => {
    // A 401 here triggers the client's onUnauthorized redirect to /login.
    api.get<Me>('/api/v1/me').then(setMe, () => {})
  }, [])

  const logout = async () => {
    try {
      await api.post('/api/v1/logout')
    } finally {
      window.location.assign('/login')
    }
  }

  if (!me) return null // brief blank while /me resolves (or redirects)

  return (
    <SessionContext.Provider value={{ me, isAdmin: me.role === 'admin' }}>
      <div className="shell">
        <aside className="sidebar">
          <div className="brand">OfficeFleet</div>
          <nav>
            <NavLink to="/" end>
              Dashboard
            </NavLink>
            <NavLink to="/agents">Agents</NavLink>
            <NavLink to="/duties">Duties</NavLink>
            <NavLink to="/settings">Settings</NavLink>
          </nav>
          <div className="session">
            <div>
              {me.username} <span className="dim">({me.role})</span>
            </div>
            <button className="small mt" onClick={logout}>
              Log out
            </button>
          </div>
        </aside>
        <main className="main">
          <Outlet />
        </main>
      </div>
      <Toasts />
    </SessionContext.Provider>
  )
}
```

(`NavLink` applies the `active` class automatically — the CSS targets `.sidebar nav a.active`.)

- [x] **Step 4: Create `web/src/pages/Login.tsx`**

```tsx
import { useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ApiError, api } from '../api/client'
import Card from '../components/Card'

// Only same-site relative paths are honored as return-to targets.
function safeNext(raw: string | null): string {
  if (raw && raw.startsWith('/') && !raw.startsWith('//')) return raw
  return '/'
}

export default function Login() {
  const [params] = useSearchParams()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      await api.post('/api/v1/login', { username, password })
      // Full navigation (not router navigate): App refetches /me on load.
      window.location.assign(safeNext(params.get('next')))
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'login failed')
    }
  }

  return (
    <div className="login-wrap">
      <Card title="Sign in to OfficeFleet" className="login-card">
        <form onSubmit={submit}>
          <label className="field">
            <span>Username</span>
            <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
          </label>
          <label className="field">
            <span>Password</span>
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </label>
          {error && <div className="form-error">{error}</div>}
          <button className="primary" type="submit" disabled={busy || !username || !password}>
            {busy ? 'Signing in…' : 'Sign in'}
          </button>
        </form>
      </Card>
    </div>
  )
}
```

- [x] **Step 5: Gate + commit**

```bash
cd web && NODE_OPTIONS= npm run build
```

Expected: clean. Optional sanity: `cd web && NODE_OPTIONS= npm run dev` against a running `fleet serve` (if a local DB is configured) — login page renders at the Vite URL; skip if no local stack.

```bash
git add web/src
git commit -m "feat(sp4b): app shell, router, session guard, login page"
```

---

### Task 9: Dashboard page (live SSE feed, counters, recent runs)

**Files:**
- Modify: `web/src/pages/Dashboard.tsx` (replace the stub)

- [x] **Step 1: Implement the page**

```tsx
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import { connectStream } from '../api/sse'
import type { Agent, Duty, Run, StreamMsg } from '../api/types'
import Card from '../components/Card'
import StatusPill from '../components/StatusPill'
import Table from '../components/Table'
import { fmtDateTime, isToday } from '../lib/format'
import { toast } from '../lib/toast'

const FEED_CAP = 50

interface FeedItem {
  msg: StreamMsg
  at: Date
}

export default function Dashboard() {
  const [runs, setRuns] = useState<Run[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [duties, setDuties] = useState<Duty[]>([])
  const [feed, setFeed] = useState<FeedItem[]>([])
  const [connected, setConnected] = useState(true)
  const [loadError, setLoadError] = useState(false)

  const load = useCallback(() => {
    setLoadError(false)
    Promise.all([
      api.get<Run[]>('/api/v1/runs?limit=50'),
      api.get<Agent[]>('/api/v1/agents'),
      api.get<Duty[]>('/api/v1/duties'),
    ]).then(
      ([r, a, d]) => {
        setRuns(r ?? [])
        setAgents(a ?? [])
        setDuties(d ?? [])
      },
      () => {
        setLoadError(true)
        toast('error', 'failed to load dashboard data')
      },
    )
  }, [])

  // Keep `load` reachable from the stream callbacks without resubscribing.
  const loadRef = useRef(load)
  loadRef.current = load

  useEffect(() => {
    load()
    return connectStream({
      onMessage: (msg) => {
        setFeed((f) => [{ msg, at: new Date() }, ...f].slice(0, FEED_CAP))
        if (msg.event === 'run_finished') loadRef.current()
      },
      onStatus: setConnected,
      onReconnect: () => loadRef.current(),
    })
  }, [load])

  const agentName = useMemo(() => {
    const m = new Map(agents.map((a) => [a.id, a.name]))
    return (id: string) => m.get(id) ?? id.slice(0, 8)
  }, [agents])

  const dutyName = useMemo(() => {
    const m = new Map(duties.map((d) => [d.id, d.name]))
    return (id: string) => m.get(id) ?? id.slice(0, 8)
  }, [duties])

  const sortedRuns = useMemo(
    () => [...runs].sort((a, b) => b.started_at.localeCompare(a.started_at)),
    [runs],
  )

  const activeAgents = agents.filter((a) => a.enabled).length
  const runsToday = runs.filter((r) => isToday(r.started_at)).length
  const failuresToday = runs.filter((r) => r.status === 'failed' && isToday(r.started_at)).length

  return (
    <>
      <div className="row between mb">
        <h1>Dashboard</h1>
        {!connected && <span className="reconnecting">reconnecting…</span>}
        {loadError && (
          <button className="small" onClick={load}>
            Retry
          </button>
        )}
      </div>

      <div className="grid cols-3 mb">
        <Card className="stat">
          <div className="num">{activeAgents}</div>
          <div className="label">active agents</div>
        </Card>
        <Card className="stat">
          <div className="num">{runsToday}</div>
          <div className="label">runs today</div>
        </Card>
        <Card className="stat">
          <div className="num">{failuresToday}</div>
          <div className="label">failures today</div>
        </Card>
      </div>

      <div className="grid cols-3">
        <Card title="Live activity" className="mb">
          <div className="feed">
            {feed.length === 0 && <div className="empty">Waiting for runs…</div>}
            {feed.map((f, i) => (
              <div key={`${f.msg.id}-${f.msg.event}-${i}`} className="item">
                <StatusPill status={f.msg.status} /> {agentName(f.msg.agent_id)} ·{' '}
                {dutyName(f.msg.duty_id)} <span className="dim">({f.msg.trigger_kind})</span>
                <div className="when">{fmtDateTime(f.at.toISOString())}</div>
              </div>
            ))}
          </div>
        </Card>

        <Card title="Recent runs" className="mb" style={{ gridColumn: 'span 2' }}>
          <Table
            columns={[
              { header: 'Status', render: (r: Run) => <StatusPill status={r.status} /> },
              {
                header: 'Agent',
                render: (r: Run) => <Link to={`/agents/${r.agent_id}`}>{agentName(r.agent_id)}</Link>,
              },
              { header: 'Duty', render: (r: Run) => dutyName(r.duty_id) },
              { header: 'Trigger', render: (r: Run) => r.trigger_kind },
              { header: 'Started', render: (r: Run) => fmtDateTime(r.started_at) },
              { header: 'Tokens', render: (r: Run) => String(r.tokens) },
            ]}
            rows={sortedRuns}
            rowKey={(r) => r.id}
            empty="No runs yet."
          />
        </Card>
      </div>
    </>
  )
}
```

- [x] **Step 2: Gate + commit**

```bash
cd web && NODE_OPTIONS= npm run build && NODE_OPTIONS= npm run test
```

Expected: clean build, api suites still green.

```bash
git add web/src/pages/Dashboard.tsx
git commit -m "feat(sp4b): dashboard with live SSE feed, counters, recent runs"
```

---

### Task 10: Agents page (directory grid, create modal, pause/resume)

**Files:**
- Modify: `web/src/pages/Agents.tsx` (replace the stub)

- [x] **Step 1: Implement the page**

```tsx
import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { useSession } from '../App'
import { ApiError, api } from '../api/client'
import type { Agent, AgentStats, BackendView } from '../api/types'
import AvatarBubble from '../components/AvatarBubble'
import Badge from '../components/Badge'
import Card from '../components/Card'
import Modal from '../components/Modal'
import { fmtDate, fmtPercent } from '../lib/format'
import { toast } from '../lib/toast'

// Lazy per-card stats cache: each card fetches once per page lifetime.
const statsCache = new Map<string, AgentStats>()

function StatsStrip({ agentID }: { agentID: string }) {
  const [stats, setStats] = useState<AgentStats | null>(statsCache.get(agentID) ?? null)

  useEffect(() => {
    if (statsCache.has(agentID)) return
    api.get<AgentStats>(`/api/v1/agents/${agentID}/stats`).then(
      (s) => {
        statsCache.set(agentID, s)
        setStats(s)
      },
      () => {}, // stats are decorative on the grid; the card still renders
    )
  }, [agentID])

  if (!stats) return <div className="dim">…</div>
  return (
    <div className="dim">
      {stats.runs_last_30d} runs 30d · {fmtPercent(stats.success_rate)} success · {stats.outputs_last_30d} outputs 30d
    </div>
  )
}

function CreateAgentModal({ backends, onClose, onCreated }: { backends: BackendView[]; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [role, setRole] = useState('')
  const [systemPrompt, setSystemPrompt] = useState('')
  const [backend, setBackend] = useState(backends[0]?.name ?? '')
  const [hiredAt, setHiredAt] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = {
        name,
        role,
        system_prompt: systemPrompt,
        default_backend: { name: backend },
      }
      if (hiredAt) body.hired_at = hiredAt
      await api.post('/api/v1/agents', body)
      onCreated()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'create failed')
    }
  }

  return (
    <Modal title="New agent" onClose={onClose}>
      <form onSubmit={submit}>
        <label className="field">
          <span>Name</span>
          <input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </label>
        <label className="field">
          <span>Role</span>
          <input value={role} onChange={(e) => setRole(e.target.value)} placeholder="e.g. Code Reviewer" />
        </label>
        <label className="field">
          <span>System prompt (persona)</span>
          <textarea rows={5} value={systemPrompt} onChange={(e) => setSystemPrompt(e.target.value)} />
        </label>
        <label className="field">
          <span>Default backend</span>
          <select value={backend} onChange={(e) => setBackend(e.target.value)}>
            {backends.map((b) => (
              <option key={b.name} value={b.name}>
                {b.name} ({b.kind})
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>Hire date</span>
          <input type="date" value={hiredAt} onChange={(e) => setHiredAt(e.target.value)} />
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="row">
          <button className="primary" type="submit" disabled={busy || !name || !backend}>
            {busy ? 'Hiring…' : 'Hire agent'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}

export default function Agents() {
  const { isAdmin } = useSession()
  const [agents, setAgents] = useState<Agent[]>([])
  const [backends, setBackends] = useState<BackendView[]>([])
  const [creating, setCreating] = useState(false)

  const load = () => {
    Promise.all([api.get<Agent[]>('/api/v1/agents'), api.get<BackendView[]>('/api/v1/backends')]).then(
      ([a, b]) => {
        setAgents(a ?? [])
        setBackends(b ?? [])
      },
      () => toast('error', 'failed to load agents'),
    )
  }
  useEffect(load, [])

  const toggleEnabled = async (agent: Agent) => {
    try {
      await api.patch(`/api/v1/agents/${agent.id}`, { enabled: !agent.enabled })
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'update failed')
    }
  }

  return (
    <>
      <div className="row between mb">
        <h1>Agents</h1>
        {isAdmin && (
          <button className="primary" onClick={() => setCreating(true)}>
            New agent
          </button>
        )}
      </div>

      <div className="grid cards">
        {agents.map((a) => (
          <Card key={a.id}>
            <div className="row mb">
              <AvatarBubble name={a.name} url={a.avatar_url} size={48} />
              <div>
                <Link to={`/agents/${a.id}`}>
                  <strong>{a.name}</strong>
                </Link>
                <div className="dim">{a.role || '—'}</div>
              </div>
              <div className="spacer" />
              {!a.enabled && <Badge text="Paused" kind="warn" />}
            </div>
            <div className="dim mb">hired {fmtDate(a.hired_at)}</div>
            <StatsStrip agentID={a.id} />
            {isAdmin && (
              <div className="mt">
                <button className="small" onClick={() => toggleEnabled(a)}>
                  {a.enabled ? 'Pause' : 'Resume'}
                </button>
              </div>
            )}
          </Card>
        ))}
        {agents.length === 0 && <div className="empty">No agents yet — hire one.</div>}
      </div>

      {creating && (
        <CreateAgentModal
          backends={backends}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false)
            load()
          }}
        />
      )}
    </>
  )
}
```

- [x] **Step 2: Gate + commit**

```bash
cd web && NODE_OPTIONS= npm run build
git add web/src/pages/Agents.tsx
git commit -m "feat(sp4b): agents directory grid with create modal and pause toggle"
```

---

### Task 11: Agent detail page (stats panel, assignments, run history + drawer)

**Files:**
- Modify: `web/src/pages/AgentDetail.tsx` (replace the stub)

- [x] **Step 1: Implement the page**

```tsx
import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useSession } from '../App'
import { ApiError, api } from '../api/client'
import type { AgentDetailResponse, Assignment, Duty, Run } from '../api/types'
import AvatarBubble from '../components/AvatarBubble'
import Badge from '../components/Badge'
import Card from '../components/Card'
import JsonView from '../components/JsonView'
import Modal from '../components/Modal'
import StatusPill from '../components/StatusPill'
import Table from '../components/Table'
import { fmtCost, fmtDate, fmtDateTime, fmtPercent } from '../lib/format'
import { toast } from '../lib/toast'

function triggerSummary(a: Assignment): string {
  switch (a.trigger.kind) {
    case 'cron':
      return a.trigger.schedule ?? ''
    case 'event-subscription':
      return a.trigger.filter ? JSON.stringify(a.trigger.filter) : 'any event'
    default:
      return '' // manual/continuous: the kind itself says it all
  }
}

function RunNowModal({ assignment, onClose, onRan }: { assignment: Assignment; onClose: () => void; onRan: () => void }) {
  const [paramsText, setParamsText] = useState('{}')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    let params: Record<string, unknown>
    try {
      params = JSON.parse(paramsText || '{}') as Record<string, unknown>
    } catch {
      setError('params must be valid JSON')
      return
    }
    setBusy(true)
    try {
      await api.post(`/api/v1/assignments/${assignment.id}/run`, { params })
      onRan()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'run failed to start')
    }
  }

  return (
    <Modal title="Run now" onClose={onClose}>
      <form onSubmit={submit}>
        <label className="field">
          <span>Params (JSON)</span>
          <textarea className="mono" rows={6} value={paramsText} onChange={(e) => setParamsText(e.target.value)} />
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="row">
          <button className="primary" type="submit" disabled={busy}>
            {busy ? 'Starting…' : 'Run'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}

function RunDrawer({ run, onClose }: { run: Run; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div className="drawer">
      <div className="row between mb">
        <h2>
          Run <span className="mono dim">{run.id.slice(0, 8)}</span> <StatusPill status={run.status} />
        </h2>
        <button className="small" onClick={onClose}>
          Close
        </button>
      </div>
      <dl className="kv mb">
        <dt>Trigger</dt>
        <dd>{run.trigger_kind}</dd>
        <dt>Started</dt>
        <dd>{fmtDateTime(run.started_at)}</dd>
        <dt>Finished</dt>
        <dd>{fmtDateTime(run.finished_at)}</dd>
        <dt>Tokens / cost</dt>
        <dd>
          {run.tokens} / {fmtCost(run.cost)}
        </dd>
        {run.event_id && (
          <>
            <dt>Event</dt>
            <dd>
              <Link to={`/settings?tab=events&highlight=${run.event_id}`}>{run.event_id.slice(0, 8)}…</Link>
            </dd>
          </>
        )}
        {run.error && (
          <>
            <dt>Error</dt>
            <dd className="form-error">{run.error}</dd>
          </>
        )}
      </dl>
      {run.llm_result?.summary && (
        <Card title="Summary" className="mb">
          {run.llm_result.summary}
        </Card>
      )}
      <JsonView label="Rendered system prompt" text={run.rendered_system_prompt} />
      <JsonView label="Rendered task prompt" text={run.rendered_prompt} />
      {run.llm_result && <JsonView label="Transcript" text={run.llm_result.transcript} />}
      {run.llm_result?.output && <JsonView label="LLM output" value={run.llm_result.output} />}
      {run.outputs_delivered && run.outputs_delivered.length > 0 && (
        <JsonView label={`Outputs delivered (${run.outputs_delivered.length})`} value={run.outputs_delivered} />
      )}
    </div>
  )
}

export default function AgentDetail() {
  const { id } = useParams<{ id: string }>()
  const { isAdmin } = useSession()
  const [detail, setDetail] = useState<AgentDetailResponse | null>(null)
  const [assignments, setAssignments] = useState<Assignment[]>([])
  const [duties, setDuties] = useState<Duty[]>([])
  const [runs, setRuns] = useState<Run[]>([])
  const [statusFilter, setStatusFilter] = useState('')
  const [runningAssignment, setRunningAssignment] = useState<Assignment | null>(null)
  const [openRun, setOpenRun] = useState<Run | null>(null)

  const load = useCallback(() => {
    if (!id) return
    Promise.all([
      api.get<AgentDetailResponse>(`/api/v1/agents/${id}`),
      api.get<Assignment[]>('/api/v1/assignments'),
      api.get<Duty[]>('/api/v1/duties'),
    ]).then(
      ([d, asg, du]) => {
        setDetail(d)
        setAssignments((asg ?? []).filter((a) => a.agent_id === id))
        setDuties(du ?? [])
      },
      () => toast('error', 'failed to load agent'),
    )
  }, [id])

  const loadRuns = useCallback(() => {
    if (!id) return
    const status = statusFilter ? `&status=${statusFilter}` : ''
    api.get<Run[]>(`/api/v1/runs?agent_id=${id}&limit=50${status}`).then(
      (r) => setRuns(r ?? []),
      () => toast('error', 'failed to load runs'),
    )
  }, [id, statusFilter])

  useEffect(load, [load])
  useEffect(loadRuns, [loadRuns])

  const dutyName = useMemo(() => {
    const m = new Map(duties.map((d) => [d.id, d.name]))
    return (dutyID: string) => m.get(dutyID) ?? dutyID.slice(0, 8)
  }, [duties])

  const sortedRuns = useMemo(
    () => [...runs].sort((a, b) => b.started_at.localeCompare(a.started_at)),
    [runs],
  )

  const toggleAgent = async () => {
    if (!detail) return
    try {
      await api.patch(`/api/v1/agents/${detail.agent.id}`, { enabled: !detail.agent.enabled })
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'update failed')
    }
  }

  const toggleAssignment = async (a: Assignment) => {
    try {
      await api.patch(`/api/v1/assignments/${a.id}`, { enabled: !a.enabled })
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'update failed')
    }
  }

  if (!detail) return <h1 className="dim">Loading…</h1>
  const { agent, stats } = detail

  return (
    <>
      <div className="row mb">
        <AvatarBubble name={agent.name} url={agent.avatar_url} size={64} />
        <div>
          <h1 style={{ marginBottom: 2 }}>{agent.name}</h1>
          <div className="dim">
            {agent.role || '—'} · hired {fmtDate(agent.hired_at)}
          </div>
        </div>
        <div className="spacer" />
        {!agent.enabled && <Badge text="Paused" kind="warn" />}
        {isAdmin && (
          <button className="small" onClick={toggleAgent}>
            {agent.enabled ? 'Pause' : 'Resume'}
          </button>
        )}
      </div>

      {stats && (
        <div className="grid cols-3 mb">
          <Card className="stat">
            <div className="num">{stats.runs_last_30d}</div>
            <div className="label">runs 30d ({stats.total_runs} total)</div>
          </Card>
          <Card className="stat">
            <div className="num">{fmtPercent(stats.success_rate)}</div>
            <div className="label">success · skip {fmtPercent(stats.skip_rate)}</div>
          </Card>
          <Card className="stat">
            <div className="num">{fmtCost(stats.cost_last_30d_usd)}</div>
            <div className="label">
              cost 30d · {stats.tokens_last_30d} tokens · {stats.outputs_last_30d} outputs
            </div>
          </Card>
        </div>
      )}

      <Card title="Assignments" className="mb">
        <Table
          columns={[
            { header: 'Duty', render: (a: Assignment) => dutyName(a.duty_id) },
            {
              header: 'Trigger',
              render: (a: Assignment) => (
                <>
                  {a.trigger.kind} <span className="dim mono">{triggerSummary(a)}</span>
                </>
              ),
            },
            {
              header: 'Enabled',
              render: (a: Assignment) =>
                isAdmin ? (
                  <button className="small" onClick={() => toggleAssignment(a)}>
                    {a.enabled ? 'On' : 'Off'}
                  </button>
                ) : (
                  <span>{a.enabled ? 'On' : 'Off'}</span>
                ),
            },
            {
              header: '',
              render: (a: Assignment) =>
                isAdmin ? (
                  <button className="small" onClick={() => setRunningAssignment(a)}>
                    Run now
                  </button>
                ) : null,
            },
          ]}
          rows={assignments}
          rowKey={(a) => a.id}
          empty="No duties assigned."
        />
      </Card>

      <Card title="Run history">
        <div className="row mb">
          <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)} style={{ width: 180 }}>
            <option value="">all statuses</option>
            <option value="queued">queued</option>
            <option value="running">running</option>
            <option value="succeeded">succeeded</option>
            <option value="failed">failed</option>
            <option value="skipped">skipped</option>
          </select>
        </div>
        <Table
          columns={[
            { header: 'Status', render: (r: Run) => <StatusPill status={r.status} /> },
            { header: 'Duty', render: (r: Run) => dutyName(r.duty_id) },
            { header: 'Trigger', render: (r: Run) => r.trigger_kind },
            { header: 'Started', render: (r: Run) => fmtDateTime(r.started_at) },
            { header: 'Tokens', render: (r: Run) => String(r.tokens) },
            { header: 'Cost', render: (r: Run) => fmtCost(r.cost) },
          ]}
          rows={sortedRuns}
          rowKey={(r) => r.id}
          onRowClick={setOpenRun}
          empty="No runs match."
        />
      </Card>

      {runningAssignment && (
        <RunNowModal
          assignment={runningAssignment}
          onClose={() => setRunningAssignment(null)}
          onRan={() => {
            setRunningAssignment(null)
            loadRuns()
          }}
        />
      )}
      {openRun && <RunDrawer run={openRun} onClose={() => setOpenRun(null)} />}
    </>
  )
}
```

- [x] **Step 2: Gate + commit**

```bash
cd web && NODE_OPTIONS= npm run build
git add web/src/pages/AgentDetail.tsx
git commit -m "feat(sp4b): agent detail with stats, assignments, run history drawer"
```

---

### Task 12: Duty library page (table + create/edit modal)

**Files:**
- Modify: `web/src/pages/Duties.tsx` (replace the stub)

- [x] **Step 1: Implement the page**

```tsx
import { useEffect, useState, type FormEvent } from 'react'
import { useSession } from '../App'
import { ApiError, api } from '../api/client'
import type { Duty, OutputActionType } from '../api/types'
import Card from '../components/Card'
import ConfirmButton from '../components/ConfirmButton'
import Modal from '../components/Modal'
import Table from '../components/Table'
import { fmtDate } from '../lib/format'
import { toast } from '../lib/toast'

const TRIGGER_KINDS = ['manual', 'cron', 'event-subscription', 'continuous']

interface DutyForm {
  name: string
  role: string
  description: string
  trigger_kinds: string[]
  required_tools: string // comma-separated in the form
  prompt: string
  output_actions: OutputActionType[]
  config_schema: string // JSON text in the form
}

function emptyForm(): DutyForm {
  return {
    name: '',
    role: '',
    description: '',
    trigger_kinds: ['manual'],
    required_tools: '',
    prompt: '',
    output_actions: [],
    config_schema: '',
  }
}

function formFromDuty(d: Duty): DutyForm {
  return {
    name: d.name,
    role: d.role,
    description: d.description,
    trigger_kinds: d.trigger_kinds ?? [],
    required_tools: (d.required_tools ?? []).join(', '),
    prompt: d.prompt,
    output_actions: d.output_actions ?? [],
    config_schema: d.config_schema ? JSON.stringify(d.config_schema, null, 2) : '',
  }
}

function DutyModal({ duty, onClose, onSaved }: { duty: Duty | null; onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState<DutyForm>(duty ? formFromDuty(duty) : emptyForm())
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const set = <K extends keyof DutyForm>(key: K, value: DutyForm[K]) => setForm((f) => ({ ...f, [key]: value }))

  const toggleKind = (kind: string) =>
    set(
      'trigger_kinds',
      form.trigger_kinds.includes(kind) ? form.trigger_kinds.filter((k) => k !== kind) : [...form.trigger_kinds, kind],
    )

  const setAction = (i: number, field: keyof OutputActionType, value: string) =>
    set(
      'output_actions',
      form.output_actions.map((a, j) => (j === i ? { ...a, [field]: value } : a)),
    )

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')

    let configSchema: Record<string, unknown> | null = null
    if (form.config_schema.trim()) {
      try {
        configSchema = JSON.parse(form.config_schema) as Record<string, unknown>
      } catch {
        setError('config schema must be valid JSON')
        return
      }
    }

    const body = {
      name: form.name,
      role: form.role,
      description: form.description,
      trigger_kinds: form.trigger_kinds,
      required_tools: form.required_tools
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean),
      prompt: form.prompt,
      output_actions: form.output_actions.filter((a) => a.plugin && a.action),
      config_schema: configSchema,
    }

    setBusy(true)
    try {
      if (duty) {
        await api.patch(`/api/v1/duties/${duty.id}`, body)
      } else {
        await api.post('/api/v1/duties', body)
      }
      onSaved()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError ? err.message : 'save failed')
    }
  }

  return (
    <Modal title={duty ? `Edit duty: ${duty.name}` : 'New duty'} onClose={onClose}>
      <form onSubmit={submit}>
        <label className="field">
          <span>Name</span>
          <input value={form.name} onChange={(e) => set('name', e.target.value)} autoFocus />
        </label>
        <label className="field">
          <span>Role category</span>
          <input value={form.role} onChange={(e) => set('role', e.target.value)} placeholder="e.g. engineering" />
        </label>
        <label className="field">
          <span>Description</span>
          <input value={form.description} onChange={(e) => set('description', e.target.value)} />
        </label>
        <div className="field">
          <span className="dim" style={{ fontSize: 12 }}>
            Trigger kinds
          </span>
          <div className="row wrap">
            {TRIGGER_KINDS.map((kind) => (
              <label key={kind} className="row" style={{ width: 'auto', gap: 4 }}>
                <input
                  type="checkbox"
                  style={{ width: 'auto' }}
                  checked={form.trigger_kinds.includes(kind)}
                  onChange={() => toggleKind(kind)}
                />
                {kind}
              </label>
            ))}
          </div>
        </div>
        <label className="field">
          <span>Required tools (comma-separated)</span>
          <input value={form.required_tools} onChange={(e) => set('required_tools', e.target.value)} placeholder="bash, files" />
        </label>
        <label className="field">
          <span>Prompt template</span>
          <textarea className="mono" rows={8} value={form.prompt} onChange={(e) => set('prompt', e.target.value)} />
        </label>
        <div className="field">
          <span className="dim" style={{ fontSize: 12 }}>
            Output actions
          </span>
          {form.output_actions.map((a, i) => (
            <div key={i} className="row mb">
              <input placeholder="plugin" value={a.plugin} onChange={(e) => setAction(i, 'plugin', e.target.value)} />
              <input placeholder="action" value={a.action} onChange={(e) => setAction(i, 'action', e.target.value)} />
              <button
                type="button"
                className="small danger"
                onClick={() => set('output_actions', form.output_actions.filter((_, j) => j !== i))}
              >
                ✕
              </button>
            </div>
          ))}
          <button type="button" className="small" onClick={() => set('output_actions', [...form.output_actions, { plugin: '', action: '' }])}>
            + add action
          </button>
        </div>
        <label className="field mt">
          <span>Config schema (JSON, optional)</span>
          <textarea className="mono" rows={4} value={form.config_schema} onChange={(e) => set('config_schema', e.target.value)} />
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="row">
          <button className="primary" type="submit" disabled={busy || !form.name}>
            {busy ? 'Saving…' : 'Save duty'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}

export default function Duties() {
  const { isAdmin } = useSession()
  const [duties, setDuties] = useState<Duty[]>([])
  const [editing, setEditing] = useState<Duty | null>(null)
  const [creating, setCreating] = useState(false)

  const load = () => {
    api.get<Duty[]>('/api/v1/duties').then(
      (d) => setDuties(d ?? []),
      () => toast('error', 'failed to load duties'),
    )
  }
  useEffect(load, [])

  const remove = async (d: Duty) => {
    try {
      await api.del(`/api/v1/duties/${d.id}`)
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'delete failed')
    }
  }

  return (
    <>
      <div className="row between mb">
        <h1>Duties</h1>
        {isAdmin && (
          <button className="primary" onClick={() => setCreating(true)}>
            New duty
          </button>
        )}
      </div>

      <Card>
        <Table
          columns={[
            {
              header: 'Name',
              render: (d: Duty) =>
                isAdmin ? (
                  <a
                    href="#edit"
                    onClick={(e) => {
                      e.preventDefault()
                      setEditing(d)
                    }}
                  >
                    {d.name}
                  </a>
                ) : (
                  <strong>{d.name}</strong>
                ),
            },
            { header: 'Role', render: (d: Duty) => d.role || '—' },
            { header: 'Description', render: (d: Duty) => d.description || '—' },
            { header: 'Triggers', render: (d: Duty) => (d.trigger_kinds ?? []).join(', ') || '—' },
            { header: 'Tools', render: (d: Duty) => (d.required_tools ?? []).join(', ') || '—' },
            { header: 'Updated', render: (d: Duty) => fmtDate(d.updated_at) },
            {
              header: '',
              render: (d: Duty) =>
                isAdmin ? <ConfirmButton label="Delete" onConfirm={() => remove(d)} /> : null,
            },
          ]}
          rows={duties}
          rowKey={(d) => d.id}
          empty="No duties defined."
        />
      </Card>

      {(creating || editing) && (
        <DutyModal
          duty={editing}
          onClose={() => {
            setCreating(false)
            setEditing(null)
          }}
          onSaved={() => {
            setCreating(false)
            setEditing(null)
            load()
          }}
        />
      )}
    </>
  )
}
```

- [x] **Step 2: Gate + commit**

```bash
cd web && NODE_OPTIONS= npm run build
git add web/src/pages/Duties.tsx
git commit -m "feat(sp4b): duty library with create/edit modal and delete"
```

---

### Task 13: Settings page (backends, secrets, users, events tabs)

**Files:**
- Modify: `web/src/pages/Settings.tsx` (replace the stub)

- [x] **Step 1: Implement the page**

```tsx
import { useEffect, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useSession } from '../App'
import { ApiError, api } from '../api/client'
import type { BackendView, FleetEvent, SecretInfo, User } from '../api/types'
import Badge from '../components/Badge'
import Card from '../components/Card'
import ConfirmButton from '../components/ConfirmButton'
import Table from '../components/Table'
import { fmtDate, fmtDateTime } from '../lib/format'
import StatusPill from '../components/StatusPill'
import { toast } from '../lib/toast'

const TABS = ['backends', 'secrets', 'users', 'events'] as const
type Tab = (typeof TABS)[number]

function BackendsTab() {
  const [backends, setBackends] = useState<BackendView[]>([])
  useEffect(() => {
    api.get<BackendView[]>('/api/v1/backends').then(
      (b) => setBackends(b ?? []),
      () => toast('error', 'failed to load backends'),
    )
  }, [])
  return (
    <Card>
      <p className="dim">Backends are defined in fleet.yaml (read-only here).</p>
      <Table
        columns={[
          { header: 'Name', render: (b: BackendView) => <strong>{b.name}</strong> },
          { header: 'Kind', render: (b: BackendView) => b.kind },
          { header: 'Auth', render: (b: BackendView) => b.auth_mode },
          { header: 'Model', render: (b: BackendView) => b.model ?? '—' },
          { header: 'Effort', render: (b: BackendView) => b.default_effort ?? '—' },
        ]}
        rows={backends}
        rowKey={(b) => b.name}
        empty="No backends configured."
      />
    </Card>
  )
}

function SecretsTab({ isAdmin }: { isAdmin: boolean }) {
  const [secrets, setSecrets] = useState<SecretInfo[]>([])
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [error, setError] = useState('')

  const load = () => {
    api.get<SecretInfo[]>('/api/v1/secrets').then(
      (s) => setSecrets(s ?? []),
      () => toast('error', 'failed to load secrets'),
    )
  }
  useEffect(load, [])

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      await api.put(`/api/v1/secrets/${encodeURIComponent(name)}`, { value })
      setName('')
      setValue('')
      load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'save failed')
    }
  }

  const remove = async (s: SecretInfo) => {
    try {
      await api.del(`/api/v1/secrets/${encodeURIComponent(s.name)}`)
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'delete failed')
    }
  }

  return (
    <>
      <Card className="mb">
        <p className="dim">Values are write-only: they are never displayed after saving.</p>
        <Table
          columns={[
            { header: 'Name', render: (s: SecretInfo) => <span className="mono">{s.name}</span> },
            {
              header: 'Storage',
              render: (s: SecretInfo) =>
                s.encrypted ? <Badge text="encrypted" kind="ok" /> : <Badge text="plaintext" kind="warn" />,
            },
            {
              header: '',
              render: (s: SecretInfo) => (isAdmin ? <ConfirmButton label="Delete" onConfirm={() => remove(s)} /> : null),
            },
          ]}
          rows={secrets}
          rowKey={(s) => s.name}
          empty="No secrets stored."
        />
      </Card>
      {isAdmin && (
        <Card title="Set secret">
          <form onSubmit={save}>
            <div className="row">
              <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
              <input placeholder="value" type="password" value={value} onChange={(e) => setValue(e.target.value)} />
              <button className="primary" type="submit" disabled={!name || !value}>
                Save
              </button>
            </div>
            {error && <div className="form-error">{error}</div>}
          </form>
        </Card>
      )}
    </>
  )
}

function UsersTab({ isAdmin, myUsername }: { isAdmin: boolean; myUsername: string }) {
  const [users, setUsers] = useState<User[]>([])
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<'admin' | 'viewer'>('viewer')
  const [error, setError] = useState('')

  const load = () => {
    api.get<User[]>('/api/v1/users').then(
      (u) => setUsers(u ?? []),
      () => toast('error', 'failed to load users'),
    )
  }
  useEffect(load, [])

  const create = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      await api.post('/api/v1/users', { username, password, role })
      setUsername('')
      setPassword('')
      setRole('viewer')
      load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'create failed')
    }
  }

  const remove = async (u: User) => {
    try {
      await api.del(`/api/v1/users/${encodeURIComponent(u.username)}`)
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'delete failed')
    }
  }

  return (
    <>
      <Card className="mb">
        <Table
          columns={[
            { header: 'Username', render: (u: User) => <strong>{u.username}</strong> },
            { header: 'Role', render: (u: User) => u.role },
            { header: 'Created', render: (u: User) => fmtDate(u.created_at) },
            {
              header: '',
              render: (u: User) =>
                isAdmin ? (
                  <ConfirmButton
                    label="Delete"
                    onConfirm={() => remove(u)}
                    disabled={u.username === myUsername}
                    title={u.username === myUsername ? 'you cannot delete your own account' : undefined}
                  />
                ) : null,
            },
          ]}
          rows={users}
          rowKey={(u) => u.id}
          empty="No users."
        />
      </Card>
      {isAdmin && (
        <Card title="Create user">
          <form onSubmit={create}>
            <div className="row">
              <input placeholder="username" value={username} onChange={(e) => setUsername(e.target.value)} />
              <input placeholder="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
              <select value={role} onChange={(e) => setRole(e.target.value as 'admin' | 'viewer')} style={{ width: 130 }}>
                <option value="viewer">viewer</option>
                <option value="admin">admin</option>
              </select>
              <button className="primary" type="submit" disabled={!username || !password}>
                Create
              </button>
            </div>
            {error && <div className="form-error">{error}</div>}
          </form>
        </Card>
      )}
    </>
  )
}

function EventsTab({ isAdmin, highlight }: { isAdmin: boolean; highlight: string | null }) {
  const [events, setEvents] = useState<FleetEvent[]>([])

  const load = () => {
    api.get<FleetEvent[]>('/api/v1/events?limit=50').then(
      (e) => setEvents(e ?? []),
      () => toast('error', 'failed to load events'),
    )
  }
  useEffect(load, [])

  const replay = async (ev: FleetEvent) => {
    try {
      await api.post(`/api/v1/events/${ev.id}/replay`)
      toast('info', 'event requeued')
      load()
    } catch (err) {
      toast('error', err instanceof ApiError ? err.message : 'replay failed')
    }
  }

  return (
    <Card>
      <Table
        columns={[
          { header: 'Source', render: (e: FleetEvent) => e.source_plugin },
          { header: 'Type', render: (e: FleetEvent) => e.event_type },
          { header: 'Status', render: (e: FleetEvent) => <StatusPill status={e.status} /> },
          { header: 'Received', render: (e: FleetEvent) => fmtDateTime(e.received_at) },
          { header: 'Dedup key', render: (e: FleetEvent) => <span className="mono dim">{e.dedup_key}</span> },
          {
            header: '',
            render: (e: FleetEvent) =>
              isAdmin ? (
                <button className="small" onClick={() => replay(e)}>
                  Replay
                </button>
              ) : null,
          },
        ]}
        rows={events}
        rowKey={(e) => e.id}
        rowClass={(e) => (e.id === highlight ? 'highlight' : '')}
        empty="No events received."
      />
    </Card>
  )
}

export default function Settings() {
  const { me, isAdmin } = useSession()
  const [params, setParams] = useSearchParams()
  const raw = params.get('tab')
  const tab: Tab = TABS.includes(raw as Tab) ? (raw as Tab) : 'backends'

  return (
    <>
      <h1>Settings</h1>
      <div className="tabs">
        {TABS.map((t) => (
          <button key={t} className={t === tab ? 'active' : ''} onClick={() => setParams({ tab: t })}>
            {t}
          </button>
        ))}
      </div>
      {tab === 'backends' && <BackendsTab />}
      {tab === 'secrets' && <SecretsTab isAdmin={isAdmin} />}
      {tab === 'users' && <UsersTab isAdmin={isAdmin} myUsername={me.username} />}
      {tab === 'events' && <EventsTab isAdmin={isAdmin} highlight={params.get('highlight')} />}
    </>
  )
}
```

- [x] **Step 2: Gate + commit**

```bash
cd web && NODE_OPTIONS= npm run build
git add web/src/pages/Settings.tsx
git commit -m "feat(sp4b): settings tabs — backends, secrets, users, events with replay"
```

---

### Task 14: Full gate, worktree invariant, manual smoke checklist

**Files:** none new — verification only (plus any fixes it surfaces).

- [x] **Step 1: Full automated gate**

```bash
NODE_OPTIONS= make test
```

Expected: `go test ./...` all green, then `tsc --noEmit` + `vitest run` green. Also:

```bash
gofmt -l .            # prints nothing
go vet ./...          # clean
git diff --stat go.mod go.sum   # EMPTY — zero new Go deps (AC6)
```

- [x] **Step 2: Worktree invariant (AC1)**

```bash
NODE_OPTIONS= make web-clean && go build ./... && git status --short
NODE_OPTIONS= make build && git status --short
```

Expected: both `git status --short` outputs show **no** modified/untracked files (dist contents and `fleet` are ignored; `.gitkeep` survives `web-clean`).

- [x] **Step 3: Manual smoke (run if a local Postgres + fleet.yaml are configured; otherwise record as deferred-to-dogfood in the final report)**

```bash
NODE_OPTIONS= make build && ./fleet serve
```

Click through, as the seeded admin:
1. `http://localhost:8080/` redirects to `/login`; bad password shows an inline error; good login lands on the dashboard.
2. Dashboard: counters render; "Run now" on an assignment (from agent detail) makes the live feed prepend `run_started`/`run_finished` and the runs table refresh.
3. Agents: grid renders with initials bubbles; create an agent; pause/resume it.
4. Agent detail: stats panel, assignments, run history; open a run drawer — prompts/transcript/outputs render; the event link jumps to Settings → events with the row highlighted.
5. Duties: create, edit, delete a duty; invalid config-schema JSON shows the inline error.
6. Settings: backends listed; set + delete a secret (encrypted badge); create a viewer user, log in as it in a private window — mutating controls are hidden and a direct POST 403s; self-delete button is disabled; events tab lists + replays.
7. Logout: lands on `/login`; back-button does not reveal data (the /me 401 redirects).

- [x] **Step 4: Commit any fixes, then final commit if anything changed**

```bash
git add -A && git status --short  # review staged set carefully first
git commit -m "test(sp4b): final gate fixes"  # only if there were fixes
```

---

## Self-review checklist (run after writing, before execution)

- **Spec coverage:** §3 build pipeline → Tasks 3–4; §4 serving → Task 3; §5 SPA architecture + five surfaces → Tasks 5–13; §6 users API → Tasks 1–2; §7 error/UX conventions → client (T5), toasts (T7), per-page inline errors (T9–13); §8 testing → Go tests (T1–3), tsc/vite/vitest gates (every frontend task), manual smoke (T14); §9 acceptance criteria → T14 maps them.
- **Type consistency:** `mountFS(mux, dist fs.FS)` (T3 tests and impl); `connectStream(handlers, makeSource)` returning `() => void` (T6 impl + tests); `useSession()` exported from `App.tsx` (T8) consumed in T10–13; `api.get/post/patch/put/del` (T5) used everywhere; `AgentDetailResponse` envelope (T5) consumed in T11; `User.role` union matches the Settings select (T13).
- **No placeholders:** every code step ships complete code; the page stubs in T8 are explicitly replaced by T9–13.
- **Known simplification (recorded):** spec §7 asks for "toast + retry button on page-level loads"; the Dashboard implements the retry-button pattern, the other pages surface the failure toast (re-navigation refetches). Raise at review if it should be uniform.







