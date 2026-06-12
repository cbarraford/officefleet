# SP4a — API Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Session-authenticated REST API + SSE over the whole platform, real AES-GCM secrets encryption, built-in users/roles, and DB-as-source-of-truth seeding — mounted into `fleet serve`.

**Architecture:** New packages `internal/secrets` (AES-256-GCM cipher), `internal/auth` (PBKDF2 passwords + session service), and `internal/api` (router, middleware, handlers, SSE broadcaster) compose over extended repos (entity Update/Delete, users/sessions/secrets repos, filtered runs, stats aggregation). The pipeline gains one additive `SetRunUpdateHook`. `seed.FromConfig` gains an only-if-empty guard with `fleet seed --force` as the explicit override. `server.Handler` gains variadic mount functions so the API and webhooks share one mux.

**Tech Stack:** Go 1.26 stdlib only (crypto/aes+cipher+pbkdf2+hmac+sha256+rand, net/http). Existing: pgx, cobra, uuid, yaml. Zero new module dependencies.

**Spec:** `docs/superpowers/specs/2026-06-11-sp4a-api-foundation-design.md`

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `internal/db/migrations/005_users_sessions.sql` | Create | users, sessions, agents.avatar_url/hired_at |
| `internal/domain/types.go` | Modify | Agent += AvatarURL/HiredAt; new User, AgentStats |
| `internal/repo/agents.go` | Modify | New columns everywhere + Update + Delete |
| `internal/repo/duties.go` | Modify | Update + Delete |
| `internal/repo/assignments.go` | Modify | Update + Delete |
| `internal/repo/runs.go` | Modify | ListFiltered + AgentStats |
| `internal/repo/users.go` | Create | UserRepo (create/get/list/delete) |
| `internal/repo/sessions.go` | Create | SessionRepo (create/lookup-join/delete/delete-expired) |
| `internal/repo/secrets.go` | Create | SecretRepo (upsert/get/list/delete) |
| `internal/secrets/cipher.go` (+test) | Create | AES-256-GCM, FSEC1 format, IsEncrypted |
| `internal/auth/password.go` (+test) | Create | PBKDF2 versioned hash/verify |
| `internal/auth/session.go` (+test) | Create | Token gen/hash, TTL, service over store iface |
| `internal/seed/seed.go` (+test) | Modify | Interface seams + only-if-empty guard + force |
| `internal/run/pipeline.go` (+test) | Modify | SetRunUpdateHook (additive, nil-safe) |
| `internal/api/api.go` | Create | API struct, dep interfaces, Mount |
| `internal/api/middleware.go` (+test) | Create | Session auth + role enforcement |
| `internal/api/auth_handlers.go` | Create | login/logout/me |
| `internal/api/entity_handlers.go` | Create | agents/duties/assignments CRUD |
| `internal/api/record_handlers.go` | Create | runs/events/backends/stats/secrets/run-now/replay |
| `internal/api/stream.go` (+test) | Create | Broadcaster + SSE handler |
| `internal/api/api_test.go` | Create | httptest integration over fakes |
| `internal/config/config.go` (+test) | Modify | ServeConfig.SecureCookies |
| `internal/server/server.go` (+test) | Modify | Handler(mounts ...func(*http.ServeMux)) |
| `cmd/fleet/main.go` | Modify | users/secrets/seed CLIs; secret decryption read path; serve wiring |
| `configs/fleet.yaml` | Modify | serve.secure_cookies comment |

Conventions for every task: run from repo root; commit to `master` (consented); do NOT push until Task 12; read the named reference files before coding; gofmt/vet must be clean before each commit. The reference style for repos is `internal/repo/runs.go`; for CLI command groups, `eventsCmd` in `cmd/fleet/main.go`.

Dependency order: 1 → 2 ∥ 3 → 4 → 5 ∥ 6 → 7 → 8 → 9 → 10 → 11 → 12.

---

### Task 1: Migration 005 + domain types + agent repo columns

**Files:**
- Create: `internal/db/migrations/005_users_sessions.sql`
- Modify: `internal/domain/types.go`, `internal/repo/agents.go`
- Test: existing suites must stay green (domain/repo have no unit tests per project precedent)

- [x] **Step 1: Create `internal/db/migrations/005_users_sessions.sql`**

```sql
-- +migrate Up

CREATE TABLE IF NOT EXISTS users (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    username        TEXT NOT NULL,
    CONSTRAINT users_username_unique UNIQUE (username),
    password_hash   TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('admin','viewer')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash      TEXT PRIMARY KEY,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at);

ALTER TABLE agents ADD COLUMN IF NOT EXISTS avatar_url TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS hired_at DATE;

-- +migrate Down

ALTER TABLE agents DROP COLUMN IF EXISTS hired_at;
ALTER TABLE agents DROP COLUMN IF EXISTS avatar_url;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
```

- [x] **Step 2: Extend `internal/domain/types.go`**

Add to the `Agent` struct, after `Enabled`:

```go
	AvatarURL      *string    `db:"avatar_url"` // generated/uploaded avatar (SP4c fills it)
	HiredAt        *time.Time `db:"hired_at"`   // "hire date" flavour shown in the UI
```

Append the new types at the end of the file:

```go
// User is an operator account. Roles: admin (full control) | viewer (read-only).
type User struct {
	ID           uuid.UUID `db:"id"`
	Username     string    `db:"username"`
	PasswordHash string    `db:"password_hash"`
	Role         string    `db:"role"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// AgentStats is the derived per-agent metrics view (spec.md §6) — computed
// from runs on demand, never stored.
type AgentStats struct {
	AgentID          uuid.UUID  `json:"agent_id"`
	TotalRuns        int        `json:"total_runs"`
	RunsLast30d      int        `json:"runs_last_30d"`
	SuccessRate      float64    `json:"success_rate"` // succeeded/(succeeded+failed), last 30d; 0 when no terminal runs
	SkipRate         float64    `json:"skip_rate"`    // skipped/total, last 30d; 0 when no runs
	TotalTokens      int        `json:"total_tokens"`
	TotalCostUSD     float64    `json:"total_cost_usd"`
	TokensLast30d    int        `json:"tokens_last_30d"`
	CostLast30dUSD   float64    `json:"cost_last_30d_usd"`
	OutputsDelivered int        `json:"outputs_delivered"`
	OutputsLast30d   int        `json:"outputs_last_30d"`
	AvgRunDurationS  float64    `json:"avg_run_duration_s"`
	LastRunAt        *time.Time `json:"last_run_at"`
}
```

- [x] **Step 3: Update `internal/repo/agents.go` for the new columns and add Update/Delete**

Replace the four SQL touchpoints and `scanAgent`, and append two methods:

```go
func (r *AgentRepo) Insert(ctx context.Context, a *domain.Agent) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	backendJSON, _ := json.Marshal(a.DefaultBackend)
	_, err := r.db.Exec(ctx,
		"INSERT INTO agents (id, name, role, system_prompt, default_backend, enabled, avatar_url, hired_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)",
		a.ID, a.Name, a.Role, a.SystemPrompt, backendJSON, a.Enabled, a.AvatarURL, a.HiredAt)
	return err
}

func (r *AgentRepo) UpsertByName(ctx context.Context, a *domain.Agent) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	backendJSON, _ := json.Marshal(a.DefaultBackend)
	return r.db.QueryRow(ctx,
		`INSERT INTO agents (id, name, role, system_prompt, default_backend, enabled, avatar_url, hired_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (name) DO UPDATE SET
		   role=EXCLUDED.role,
		   system_prompt=EXCLUDED.system_prompt,
		   default_backend=EXCLUDED.default_backend,
		   enabled=EXCLUDED.enabled,
		   updated_at=NOW()
		 RETURNING id`,
		a.ID, a.Name, a.Role, a.SystemPrompt, backendJSON, a.Enabled, a.AvatarURL, a.HiredAt,
	).Scan(&a.ID)
}
```

(NOTE: the upsert deliberately does NOT overwrite avatar_url/hired_at on conflict — YAML has no persona fields, and a `seed --force` must not wipe UI-set personas.)

`GetByName`/`List` SELECT lists become:
`id, name, role, system_prompt, default_backend, enabled, avatar_url, hired_at, created_at, updated_at`
and add `GetByID`:

```go
func (r *AgentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, role, system_prompt, default_backend, enabled, avatar_url, hired_at, created_at, updated_at FROM agents WHERE id=$1", id)
	return scanAgent(row)
}
```

`scanAgent` scans the two new fields between `Enabled` and `CreatedAt`:

```go
func scanAgent(s scanner) (*domain.Agent, error) {
	var a domain.Agent
	var backendJSON []byte
	if err := s.Scan(&a.ID, &a.Name, &a.Role, &a.SystemPrompt, &backendJSON, &a.Enabled,
		&a.AvatarURL, &a.HiredAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan agent: %w", err)
	}
	_ = json.Unmarshal(backendJSON, &a.DefaultBackend)
	return &a, nil
}
```

Append:

```go
// Update persists every editable field by id (PATCH semantics live in the API
// layer: it loads, applies provided fields, then calls Update).
func (r *AgentRepo) Update(ctx context.Context, a *domain.Agent) error {
	backendJSON, _ := json.Marshal(a.DefaultBackend)
	tag, err := r.db.Exec(ctx,
		`UPDATE agents SET name=$2, role=$3, system_prompt=$4, default_backend=$5,
		   enabled=$6, avatar_url=$7, hired_at=$8, updated_at=NOW() WHERE id=$1`,
		a.ID, a.Name, a.Role, a.SystemPrompt, backendJSON, a.Enabled, a.AvatarURL, a.HiredAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("agent %s not found", a.ID)
	}
	return nil
}

func (r *AgentRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM agents WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("agent %s not found", id)
	}
	return nil
}
```

- [x] **Step 4: Verify and commit**

Run: `go build ./... && go test ./... -count=1 && go vet ./... && gofmt -l internal/`
Expected: all clean (no existing test reads the new columns yet).

```bash
git add internal/db/migrations/005_users_sessions.sql internal/domain/types.go internal/repo/agents.go
git commit -m "feat(sp4a): users/sessions schema, agent persona columns, agent update/delete"
```

---

### Task 2: Secrets cipher (`internal/secrets`)

**Files:**
- Create: `internal/secrets/cipher.go`
- Test: `internal/secrets/cipher_test.go`

- [x] **Step 1: Write the failing tests** — `internal/secrets/cipher_test.go`:

```go
package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestRoundTrip(t *testing.T) {
	c, err := NewCipher(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("glpat-supersecret-token")
	stored, err := c.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(stored) {
		t.Error("encrypted value not detected by IsEncrypted")
	}
	if bytes.Contains(stored, plain) {
		t.Error("ciphertext contains plaintext")
	}
	got, err := c.Decrypt(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round trip = %q, want %q", got, plain)
	}
}

func TestEncryptIsNondeterministic(t *testing.T) {
	c, _ := NewCipher(testKey(t))
	a, _ := c.Encrypt([]byte("x"))
	b, _ := c.Encrypt([]byte("x"))
	if bytes.Equal(a, b) {
		t.Error("two encryptions of the same plaintext must differ (random nonce)")
	}
}

func TestTamperDetected(t *testing.T) {
	c, _ := NewCipher(testKey(t))
	stored, _ := c.Encrypt([]byte("secret"))
	stored[len(stored)-1] ^= 0xFF
	if _, err := c.Decrypt(stored); err == nil {
		t.Error("tampered ciphertext must fail to decrypt (GCM auth)")
	}
}

func TestWrongKeyFails(t *testing.T) {
	c1, _ := NewCipher(testKey(t))
	c2, _ := NewCipher(testKey(t))
	stored, _ := c1.Encrypt([]byte("secret"))
	if _, err := c2.Decrypt(stored); err == nil {
		t.Error("decrypt with wrong key must fail")
	}
}

func TestIsEncryptedOnLegacyPlaintext(t *testing.T) {
	for _, legacy := range [][]byte{[]byte("glpat-plain"), []byte(""), []byte("FSEC"), nil} {
		if IsEncrypted(legacy) {
			t.Errorf("IsEncrypted(%q) = true, want false", legacy)
		}
	}
}

func TestDecryptRejectsNonEncrypted(t *testing.T) {
	c, _ := NewCipher(testKey(t))
	if _, err := c.Decrypt([]byte("plain-bytes")); err == nil {
		t.Error("Decrypt of non-FSEC1 bytes must error")
	}
	if _, err := c.Decrypt([]byte("FSEC1short")); err == nil {
		t.Error("Decrypt of truncated FSEC1 value must error, not panic")
	}
}

func TestNewCipherKeyValidation(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"not-base64": "!!!not-base64!!!",
		"short key":  base64.StdEncoding.EncodeToString(make([]byte, 16)),
	}
	for name, key := range cases {
		if _, err := NewCipher(key); err == nil {
			t.Errorf("%s: expected error", name)
		} else if name == "short key" && !strings.Contains(err.Error(), "32") {
			t.Errorf("short-key error should mention 32 bytes: %v", err)
		}
	}
}
```

- [x] **Step 2:** Run `go test ./internal/secrets/ -v` — FAIL (package missing).

- [x] **Step 3: Implement** — `internal/secrets/cipher.go`:

```go
// Package secrets provides AES-256-GCM encryption-at-rest for the secrets
// table. Stored format: ASCII magic "FSEC1" + 12-byte nonce + ciphertext+tag.
// Rows lacking the magic are legacy plaintext (pre-SP4a), detectable without
// schema changes; `fleet secrets encrypt-existing` migrates them.
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// MasterKeyEnv names the environment variable holding the base64 32-byte key.
const MasterKeyEnv = "FLEET_MASTER_KEY"

var magic = []byte("FSEC1")

const nonceLen = 12

// Cipher encrypts/decrypts secret values with a fixed master key.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a Cipher from a base64-encoded 32-byte key.
func NewCipher(keyB64 string) (*Cipher, error) {
	if keyB64 == "" {
		return nil, fmt.Errorf("secrets: master key is empty (set %s to a base64-encoded 32-byte key)", MasterKeyEnv)
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("secrets: master key is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets: master key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: init cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: init gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plain into the stored format with a fresh random nonce.
func (c *Cipher) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	out := make([]byte, 0, len(magic)+nonceLen+len(plain)+c.aead.Overhead())
	out = append(out, magic...)
	out = append(out, nonce...)
	return c.aead.Seal(out, nonce, plain, nil), nil
}

// Decrypt opens a stored value. It errors on legacy plaintext (no magic),
// truncated values, and authentication failures (tamper / wrong key).
func (c *Cipher) Decrypt(stored []byte) ([]byte, error) {
	if !IsEncrypted(stored) {
		return nil, fmt.Errorf("secrets: value is not encrypted (legacy plaintext?)")
	}
	rest := stored[len(magic):]
	if len(rest) < nonceLen+c.aead.Overhead() {
		return nil, fmt.Errorf("secrets: encrypted value is truncated")
	}
	nonce, ct := rest[:nonceLen], rest[nonceLen:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: decrypt failed (wrong key or corrupted value): %w", err)
	}
	return plain, nil
}

// IsEncrypted reports whether a stored value carries the FSEC1 format.
func IsEncrypted(stored []byte) bool {
	return bytes.HasPrefix(stored, magic)
}
```

- [x] **Step 4:** Run `go test ./internal/secrets/ -v` — PASS (7 tests). gofmt/vet clean.

- [x] **Step 5: Commit**

```bash
git add internal/secrets/
git commit -m "feat(sp4a): AES-256-GCM secrets cipher with legacy-plaintext detection"
```

---

### Task 3: Auth core — passwords and sessions (`internal/auth`)

**Files:**
- Create: `internal/auth/password.go`, `internal/auth/session.go`
- Test: `internal/auth/password_test.go`, `internal/auth/session_test.go`

- [x] **Step 1: Write the failing password tests** — `internal/auth/password_test.go`:

```go
package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$600000$") {
		t.Errorf("hash format = %q", hash)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Error("correct password rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Error("wrong password accepted")
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Error("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, h := range []string{"", "nonsense", "pbkdf2-sha256$notanumber$AA$BB",
		"pbkdf2-sha256$600000$!badb64!$AA", "argon2id$future$x$y"} {
		if VerifyPassword(h, "pw") {
			t.Errorf("malformed hash %q verified", h)
		}
	}
}

func TestEmptyPasswordRejected(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Error("empty password must be rejected")
	}
}
```

- [x] **Step 2: Write the failing session tests** — `internal/auth/session_test.go`:

```go
package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// memSessionStore is an in-memory SessionStore for tests.
type memSessionStore struct {
	rows map[string]memSession
}

type memSession struct {
	userID  uuid.UUID
	role    string
	expires time.Time
}

func newMemSessionStore() *memSessionStore { return &memSessionStore{rows: map[string]memSession{}} }

func (m *memSessionStore) Create(_ context.Context, tokenHash string, userID uuid.UUID, expiresAt time.Time) error {
	m.rows[tokenHash] = memSession{userID: userID, role: domain.RoleAdmin, expires: expiresAt}
	return nil
}

func (m *memSessionStore) Lookup(_ context.Context, tokenHash string) (uuid.UUID, string, error) {
	s, ok := m.rows[tokenHash]
	if !ok || time.Now().After(s.expires) {
		delete(m.rows, tokenHash)
		return uuid.Nil, "", fmt.Errorf("session not found")
	}
	return s.userID, s.role, nil
}

func (m *memSessionStore) Delete(_ context.Context, tokenHash string) error {
	delete(m.rows, tokenHash)
	return nil
}

func (m *memSessionStore) DeleteExpired(_ context.Context) error { return nil }

func TestSessionLifecycle(t *testing.T) {
	store := newMemSessionStore()
	svc := NewSessions(store)
	userID := uuid.New()

	token, err := svc.Start(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 32 {
		t.Errorf("token too short: %d chars", len(token))
	}
	// The raw token must NOT be a store key (only its hash is stored).
	if _, ok := store.rows[token]; ok {
		t.Error("raw token stored; only the hash may be persisted")
	}

	gotID, role, err := svc.Validate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != userID || role != domain.RoleAdmin {
		t.Errorf("validate = %v/%q", gotID, role)
	}

	if err := svc.End(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Validate(context.Background(), token); err == nil {
		t.Error("validated after End")
	}
}

func TestValidateUnknownToken(t *testing.T) {
	svc := NewSessions(newMemSessionStore())
	if _, _, err := svc.Validate(context.Background(), "no-such-token"); err == nil {
		t.Error("unknown token validated")
	}
}
```

- [x] **Step 3:** Run `go test ./internal/auth/ -v` — FAIL (package missing).

- [x] **Step 4: Implement** — `internal/auth/password.go`:

```go
// Package auth provides operator authentication: PBKDF2 password hashing
// (stdlib, versioned format) and opaque session tokens stored hashed.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	pbkdf2Iterations = 600_000
	saltLen          = 16
	derivedKeyLen    = 32
	hashScheme       = "pbkdf2-sha256"
)

// HashPassword derives a versioned PBKDF2-HMAC-SHA256 hash:
// pbkdf2-sha256$<iter>$<b64 salt>$<b64 key>. The scheme prefix lets a future
// KDF coexist and migrate on next login.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("auth: password must not be empty")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, derivedKeyLen)
	if err != nil {
		return "", fmt.Errorf("auth: derive key: %w", err)
	}
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, pbkdf2Iterations,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the stored versioned hash.
// Malformed or unknown-scheme hashes verify false (never panic).
func VerifyPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}
```

NOTE: `crypto/pbkdf2` is stdlib since Go 1.24. Check the exact signature with
`go doc crypto/pbkdf2.Key` — if the installed toolchain's signature differs from
`Key(h func() hash.Hash, password string, salt []byte, iter, keyLength int) ([]byte, error)`,
adapt the two call sites (the tests pin the behavior, not the signature).

- [x] **Step 5: Implement** — `internal/auth/session.go`:

```go
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SessionTTL is the fixed session lifetime.
const SessionTTL = 7 * 24 * time.Hour

// CookieName is the session cookie's name.
const CookieName = "fleet_session"

// SessionStore persists sessions; *repo.SessionRepo satisfies it. Lookup
// returns the user id and role for an unexpired session (joining users) and
// treats expired rows as not-found.
type SessionStore interface {
	Create(ctx context.Context, tokenHash string, userID uuid.UUID, expiresAt time.Time) error
	Lookup(ctx context.Context, tokenHash string) (uuid.UUID, string, error)
	Delete(ctx context.Context, tokenHash string) error
	DeleteExpired(ctx context.Context) error
}

// Sessions issues and validates opaque tokens. The store only ever sees
// SHA-256(token): a leaked sessions table cannot be replayed.
type Sessions struct {
	store SessionStore
}

func NewSessions(store SessionStore) *Sessions { return &Sessions{store: store} }

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Start creates a session for userID and returns the raw token (cookie value).
func (s *Sessions) Start(ctx context.Context, userID uuid.UUID) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: token: %w", err)
	}
	token := hex.EncodeToString(raw)
	_ = s.store.DeleteExpired(ctx) // opportunistic cleanup
	if err := s.store.Create(ctx, hashToken(token), userID, time.Now().Add(SessionTTL)); err != nil {
		return "", fmt.Errorf("auth: create session: %w", err)
	}
	return token, nil
}

// Validate resolves a raw token to (userID, role); errors when missing/expired.
func (s *Sessions) Validate(ctx context.Context, token string) (uuid.UUID, string, error) {
	return s.store.Lookup(ctx, hashToken(token))
}

// End destroys the session for a raw token.
func (s *Sessions) End(ctx context.Context, token string) error {
	return s.store.Delete(ctx, hashToken(token))
}
```

- [x] **Step 6:** Run `go test ./internal/auth/ -v` — PASS. The PBKDF2 tests take ~1s each (600k iterations × a few hashes) — acceptable; do NOT lower iterations in tests.

- [x] **Step 7: Commit**

```bash
git add internal/auth/
git commit -m "feat(sp4a): pbkdf2 password hashing and hashed session service"
```

---

### Task 4: Repos — users, sessions, secrets, entity Update/Delete, filtered runs, stats

**Files:**
- Create: `internal/repo/users.go`, `internal/repo/sessions.go`, `internal/repo/secrets.go`
- Modify: `internal/repo/duties.go`, `internal/repo/assignments.go`, `internal/repo/runs.go`

Repo precedent: thin pgx SQL, no unit tests (logic is exercised through fakes; SQL through migrate+smoke). Match `runs.go` style.

- [x] **Step 1: `internal/repo/users.go`**

```go
package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UserRepo struct{ db *pgxpool.Pool }

func NewUserRepo(db *pgxpool.Pool) *UserRepo { return &UserRepo{db: db} }

func (r *UserRepo) Create(ctx context.Context, u *domain.User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	_, err := r.db.Exec(ctx,
		"INSERT INTO users (id, username, password_hash, role) VALUES ($1,$2,$3,$4)",
		u.ID, u.Username, u.PasswordHash, u.Role)
	return err
}

// GetByUsername returns (nil, nil) when the user does not exist — callers use
// a uniform bad-credentials error to avoid a username oracle.
func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	var u domain.User
	err := r.db.QueryRow(ctx,
		"SELECT id, username, password_hash, role, created_at, updated_at FROM users WHERE username=$1",
		username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepo) List(ctx context.Context) ([]*domain.User, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, username, password_hash, role, created_at, updated_at FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.User
	for rows.Next() {
		var u domain.User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

func (r *UserRepo) Delete(ctx context.Context, username string) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM users WHERE username=$1", username)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user %q not found", username)
	}
	return nil
}
```

- [x] **Step 2: `internal/repo/sessions.go`** (implements `auth.SessionStore`)

```go
package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SessionRepo struct{ db *pgxpool.Pool }

func NewSessionRepo(db *pgxpool.Pool) *SessionRepo { return &SessionRepo{db: db} }

func (r *SessionRepo) Create(ctx context.Context, tokenHash string, userID uuid.UUID, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx,
		"INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1,$2,$3)",
		tokenHash, userID, expiresAt)
	return err
}

// Lookup resolves an unexpired session to (userID, role), lazily deleting
// expired rows it encounters.
func (r *SessionRepo) Lookup(ctx context.Context, tokenHash string) (uuid.UUID, string, error) {
	var userID uuid.UUID
	var role string
	var expiresAt time.Time
	err := r.db.QueryRow(ctx,
		`SELECT s.user_id, u.role, s.expires_at FROM sessions s
		 JOIN users u ON u.id = s.user_id WHERE s.token_hash=$1`,
		tokenHash).Scan(&userID, &role, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", fmt.Errorf("session not found")
	}
	if err != nil {
		return uuid.Nil, "", err
	}
	if time.Now().After(expiresAt) {
		_, _ = r.db.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", tokenHash)
		return uuid.Nil, "", fmt.Errorf("session expired")
	}
	return userID, role, nil
}

func (r *SessionRepo) Delete(ctx context.Context, tokenHash string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", tokenHash)
	return err
}

func (r *SessionRepo) DeleteExpired(ctx context.Context) error {
	_, err := r.db.Exec(ctx, "DELETE FROM sessions WHERE expires_at < NOW()")
	return err
}
```

- [x] **Step 3: `internal/repo/secrets.go`**

```go
package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SecretRepo struct{ db *pgxpool.Pool }

func NewSecretRepo(db *pgxpool.Pool) *SecretRepo { return &SecretRepo{db: db} }

func (r *SecretRepo) Upsert(ctx context.Context, name string, value []byte) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO secrets (name, encrypted_value) VALUES ($1,$2)
		 ON CONFLICT (name) DO UPDATE SET encrypted_value=EXCLUDED.encrypted_value, updated_at=NOW()`,
		name, value)
	return err
}

func (r *SecretRepo) Get(ctx context.Context, name string) ([]byte, error) {
	var v []byte
	err := r.db.QueryRow(ctx, "SELECT encrypted_value FROM secrets WHERE name=$1", name).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("secret %q not found", name)
	}
	return v, err
}

// SecretInfo is a value-free listing entry.
type SecretInfo struct {
	Name      string `json:"name"`
	Encrypted bool   `json:"encrypted"`
}

// List returns names + raw values (the caller derives encrypted-status and
// MUST NOT expose the values).
func (r *SecretRepo) List(ctx context.Context) (map[string][]byte, error) {
	rows, err := r.db.Query(ctx, "SELECT name, encrypted_value FROM secrets ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var name string
		var v []byte
		if err := rows.Scan(&name, &v); err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, rows.Err()
}

func (r *SecretRepo) Delete(ctx context.Context, name string) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM secrets WHERE name=$1", name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("secret %q not found", name)
	}
	return nil
}
```

- [x] **Step 4: Duty + Assignment Update/Delete** (append to the respective files; mirror the agent versions)

`internal/repo/duties.go`:

```go
func (r *DutyRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Duty, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend, created_at, updated_at FROM duties WHERE id=$1", id)
	return scanDuty(row)
}

func (r *DutyRepo) Update(ctx context.Context, d *domain.Duty) error {
	outputActionsJSON, _ := json.Marshal(d.OutputActions)
	configSchemaJSON, _ := json.Marshal(d.ConfigSchema)
	var backendJSON []byte
	if d.Backend != nil {
		backendJSON, _ = json.Marshal(d.Backend)
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE duties SET name=$2, role=$3, description=$4, trigger_kinds=$5, prompt=$6,
		   required_tools=$7, output_actions=$8, config_schema=$9, backend=$10, updated_at=NOW()
		 WHERE id=$1`,
		d.ID, d.Name, d.Role, d.Description, d.TriggerKinds, d.Prompt, d.RequiredTools,
		outputActionsJSON, configSchemaJSON, backendJSON)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("duty %s not found", d.ID)
	}
	return nil
}

func (r *DutyRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM duties WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("duty %s not found", id)
	}
	return nil
}
```

(Check whether `GetByID` already exists on DutyRepo — it does not; only GetByName/List. Verify imports include `uuid`.)

`internal/repo/assignments.go`:

```go
func (r *AssignmentRepo) Update(ctx context.Context, a *domain.Assignment) error {
	triggerJSON, _ := json.Marshal(a.Trigger)
	outputsJSON, _ := json.Marshal(a.Outputs)
	configJSON, _ := json.Marshal(a.Config)
	var backendJSON []byte
	if a.Backend != nil {
		backendJSON, _ = json.Marshal(a.Backend)
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE assignments SET enabled=$2, trigger=$3, outputs=$4, config=$5, backend=$6,
		   task_prompt_override=$7, extra_instructions=$8, updated_at=NOW() WHERE id=$1`,
		a.ID, a.Enabled, triggerJSON, outputsJSON, configJSON, backendJSON,
		a.TaskPromptOverride, a.ExtraInstructions)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("assignment %s not found", a.ID)
	}
	return nil
}

func (r *AssignmentRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM assignments WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("assignment %s not found", id)
	}
	return nil
}
```

- [x] **Step 5: Filtered runs + stats** (append to `internal/repo/runs.go`)

```go
// ListFiltered returns run summaries newest-first. status/agentID filter when
// non-zero. Summaries exclude llm_result (transcripts can be large).
func (r *RunRepo) ListFiltered(ctx context.Context, status string, agentID uuid.UUID, limit int) ([]*domain.Run, error) {
	q := `SELECT id, assignment_id, agent_id, duty_id, trigger_kind, event_id,
	        '' AS rendered_system_prompt, '' AS rendered_prompt, NULL AS llm_result,
	        outputs_delivered, status, tokens, cost, started_at, finished_at, error
	      FROM runs WHERE 1=1`
	args := []any{}
	n := 1
	if status != "" {
		q += fmt.Sprintf(" AND status=$%d", n)
		args = append(args, status)
		n++
	}
	if agentID != uuid.Nil {
		q += fmt.Sprintf(" AND agent_id=$%d", n)
		args = append(args, agentID)
		n++
	}
	q += fmt.Sprintf(" ORDER BY started_at DESC LIMIT $%d", n)
	args = append(args, limit)

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// AgentStats computes the spec.md §6 derived metrics for one agent.
func (r *RunRepo) AgentStats(ctx context.Context, agentID uuid.UUID) (*domain.AgentStats, error) {
	st := &domain.AgentStats{AgentID: agentID}
	var ok30, fail30, skip30, total30 int
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE started_at > NOW() - INTERVAL '30 days'),
		       COUNT(*) FILTER (WHERE status='succeeded' AND started_at > NOW() - INTERVAL '30 days'),
		       COUNT(*) FILTER (WHERE status='failed'    AND started_at > NOW() - INTERVAL '30 days'),
		       COUNT(*) FILTER (WHERE status='skipped'   AND started_at > NOW() - INTERVAL '30 days'),
		       COALESCE(SUM(tokens),0), COALESCE(SUM(cost),0),
		       COALESCE(SUM(tokens) FILTER (WHERE started_at > NOW() - INTERVAL '30 days'),0),
		       COALESCE(SUM(cost)   FILTER (WHERE started_at > NOW() - INTERVAL '30 days'),0),
		       COALESCE(AVG(EXTRACT(EPOCH FROM (finished_at - started_at))) FILTER (WHERE finished_at IS NOT NULL),0),
		       MAX(started_at)
		FROM runs WHERE agent_id=$1`, agentID).Scan(
		&st.TotalRuns, &total30, &ok30, &fail30, &skip30,
		&st.TotalTokens, &st.TotalCostUSD, &st.TokensLast30d, &st.CostLast30dUSD,
		&st.AvgRunDurationS, &st.LastRunAt)
	if err != nil {
		return nil, fmt.Errorf("agent stats: %w", err)
	}
	st.RunsLast30d = total30
	if ok30+fail30 > 0 {
		st.SuccessRate = float64(ok30) / float64(ok30+fail30)
	}
	if total30 > 0 {
		st.SkipRate = float64(skip30) / float64(total30)
	}
	err = r.db.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE r.started_at > NOW() - INTERVAL '30 days')
		FROM runs r, jsonb_array_elements(r.outputs_delivered) o
		WHERE r.agent_id=$1 AND o->>'status'='delivered'`, agentID).Scan(
		&st.OutputsDelivered, &st.OutputsLast30d)
	if err != nil {
		return nil, fmt.Errorf("agent output stats: %w", err)
	}
	return st, nil
}
```

(NOTE: `outputs_delivered` may hold JSON `null` for old rows — `jsonb_array_elements(null)` errors. Guard with `AND jsonb_typeof(r.outputs_delivered)='array'` in the second query's WHERE. Add that.)

- [x] **Step 6: Verify and commit**

Run: `go build ./... && go test ./... -count=1 && go vet ./... && gofmt -l internal/repo/`
Expected: clean.

```bash
git add internal/repo/
git commit -m "feat(sp4a): user/session/secret repos, entity update-delete, run filters and stats"
```

---

### Task 5: Seed guard + `fleet seed --force`

**Files:**
- Modify: `internal/seed/seed.go`
- Create: `internal/seed/seed_test.go`
- Modify: `cmd/fleet/main.go` (seedCmd; migrate call site)

- [x] **Step 1: Refactor `seed.FromConfig` onto interfaces with a force flag.** Replace the signature and add the guard (the function body's upsert loops are unchanged — only the parameter types and the new guard at the top):

```go
// AgentSeeder, DutySeeder, AssignmentSeeder are the repo capabilities seeding
// needs; the concrete repos satisfy them.
type AgentSeeder interface {
	UpsertByName(ctx context.Context, a *domain.Agent) error
	List(ctx context.Context) ([]*domain.Agent, error)
}

type DutySeeder interface {
	UpsertByName(ctx context.Context, d *domain.Duty) error
	List(ctx context.Context) ([]*domain.Duty, error)
}

type AssignmentSeeder interface {
	UpsertByAgentAndDuty(ctx context.Context, a *domain.Assignment) error
	List(ctx context.Context) ([]*domain.Assignment, error)
}

// FromConfig seeds agents/duties/assignments from cfg. The DB is the source
// of truth once populated: without force, seeding is skipped unless ALL three
// entity tables are empty (first boot). force re-seeds unconditionally,
// overwriting same-named entities (UI edits included).
func FromConfig(ctx context.Context, cfg *config.Config,
	agentRepo AgentSeeder, dutyRepo DutySeeder, assignRepo AssignmentSeeder, force bool,
) error {
	if !force {
		agents, err := agentRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("seed precheck (agents): %w", err)
		}
		duties, err := dutyRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("seed precheck (duties): %w", err)
		}
		assignments, err := assignRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("seed precheck (assignments): %w", err)
		}
		if len(agents) > 0 || len(duties) > 0 || len(assignments) > 0 {
			fmt.Println("DB already populated; skipping config seed (use 'fleet seed --force' to overwrite)")
			return nil
		}
	}
	// ... existing upsert loops, unchanged ...
}
```

- [x] **Step 2: Write the tests** — `internal/seed/seed_test.go` (fakes implement the three seeder interfaces; record upsert calls; List returns configurable rows):

```go
package seed

import (
	"context"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
)

type fakeAgentSeeder struct {
	existing []*domain.Agent
	upserts  int
}

func (f *fakeAgentSeeder) UpsertByName(_ context.Context, a *domain.Agent) error {
	f.upserts++
	return nil
}
func (f *fakeAgentSeeder) List(_ context.Context) ([]*domain.Agent, error) { return f.existing, nil }

type fakeDutySeeder struct {
	existing []*domain.Duty
	upserts  int
}

func (f *fakeDutySeeder) UpsertByName(_ context.Context, d *domain.Duty) error {
	f.upserts++
	return nil
}
func (f *fakeDutySeeder) List(_ context.Context) ([]*domain.Duty, error) { return f.existing, nil }

type fakeAssignmentSeeder struct {
	existing []*domain.Assignment
	upserts  int
}

func (f *fakeAssignmentSeeder) UpsertByAgentAndDuty(_ context.Context, a *domain.Assignment) error {
	f.upserts++
	return nil
}
func (f *fakeAssignmentSeeder) List(_ context.Context) ([]*domain.Assignment, error) {
	return f.existing, nil
}

func seedCfg() *config.Config {
	return &config.Config{
		Agents: []config.AgentConfig{{Name: "a1"}},
		Duties: []config.DutyConfig{{Name: "d1"}},
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}},
	}
}

func TestSeed_EmptyDBSeeds(t *testing.T) {
	ag, du, as := &fakeAgentSeeder{}, &fakeDutySeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), seedCfg(), ag, du, as, false); err != nil {
		t.Fatal(err)
	}
	if ag.upserts != 1 || du.upserts != 1 || as.upserts != 1 {
		t.Errorf("upserts = %d/%d/%d, want 1/1/1", ag.upserts, du.upserts, as.upserts)
	}
}

func TestSeed_PopulatedDBSkips(t *testing.T) {
	ag := &fakeAgentSeeder{existing: []*domain.Agent{{Name: "existing"}}}
	du, as := &fakeDutySeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), seedCfg(), ag, du, as, false); err != nil {
		t.Fatal(err)
	}
	if ag.upserts+du.upserts+as.upserts != 0 {
		t.Errorf("populated DB must not be reseeded; upserts = %d/%d/%d", ag.upserts, du.upserts, as.upserts)
	}
}

func TestSeed_ForceOverwrites(t *testing.T) {
	ag := &fakeAgentSeeder{existing: []*domain.Agent{{Name: "existing"}}}
	du, as := &fakeDutySeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), seedCfg(), ag, du, as, true); err != nil {
		t.Fatal(err)
	}
	if ag.upserts != 1 {
		t.Errorf("force must seed; agent upserts = %d", ag.upserts)
	}
}
```

NOTE: the existing FromConfig body uses `agent.ID` after UpsertByName via the name→id maps — the fakes above return nil without setting IDs, which leaves uuid.Nil in the maps; that is fine for these tests (assignment upsert is still counted). Verify the body doesn't error on Nil ids; it does not.

- [x] **Step 3: Update the call site in migrateCmd and add seedCmd.** In `cmd/fleet/main.go`, the migrate body's `seed.FromConfig(ctx, cfg, agentRepo, dutyRepo, assignmentRepo)` gains `, false`. Add the new command (register `root.AddCommand(seedCmd())`):

```go
// seedCmd returns the "seed" command (DB is the source of truth; this is the
// explicit override).
func seedCmd() *cobra.Command {
	var flagForce bool
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed entities from fleet.yaml (no-op unless DB is empty; --force overwrites)",
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
			if flagForce {
				fmt.Println("WARNING: --force overwrites same-named entities, including UI edits")
			}
			if err := seed.FromConfig(ctx, cfg,
				repo.NewAgentRepo(pool), repo.NewDutyRepo(pool), repo.NewAssignmentRepo(pool), flagForce); err != nil {
				return fmt.Errorf("seed: %w", err)
			}
			fmt.Println("seed complete")
			return nil
		},
	}
	cmd.Flags().BoolVar(&flagForce, "force", false, "overwrite existing entities from fleet.yaml")
	return cmd
}
```

- [x] **Step 4: Verify and commit**

Run: `go build ./... && go test ./... -count=1 && go vet ./...`

```bash
git add internal/seed/ cmd/fleet/main.go
git commit -m "feat(sp4a): DB-owns seeding — guard FromConfig, add fleet seed --force"
```

---

### Task 6: Pipeline run-update hook

**Files:**
- Modify: `internal/run/pipeline.go`
- Test: `internal/run/pipeline_test.go` (append)

- [x] **Step 1: Write the failing test** (append to pipeline_test.go):

```go
func TestPipelineExecute_RunUpdateHook(t *testing.T) {
	ctx := context.Background()
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "ok"})
	backendName := "hook-backend"
	cfg := &config.Config{Backends: []config.Backend{{
		Name: backendName, Kind: "claude", Model: "claude-3-5-sonnet",
		DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
	}}}
	pipeline := &Pipeline{cfg: cfg, runRepo: newFakeRunRepo(), store: state.NewMemStore()}

	var mu sync.Mutex
	var statuses []domain.RunStatus
	pipeline.SetRunUpdateHook(func(r *domain.Run) {
		mu.Lock()
		statuses = append(statuses, r.Status)
		mu.Unlock()
	})

	agentID, dutyID := uuid.New(), uuid.New()
	_, err := pipeline.Execute(ctx, ExecuteRequest{
		Assignment: &domain.Assignment{ID: uuid.New(), AgentID: agentID, DutyID: dutyID,
			Enabled: true, Backend: &domain.BackendRef{Name: backendName}, Config: map[string]any{}},
		Agent: &domain.Agent{ID: agentID, Name: "hook-agent", Role: "t", SystemPrompt: "s", Enabled: true},
		Duty:  &domain.Duty{ID: dutyID, Name: "hook-duty", Role: "t", Description: "d", Prompt: "p"},
		TriggerKind: "manual", EventParams: map[string]any{}, Executor: fakeExec,
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(statuses) != 2 {
		t.Fatalf("hook fired %d times, want 2 (started + terminal): %v", len(statuses), statuses)
	}
	if statuses[0] != domain.RunStatusRunning {
		t.Errorf("first hook status = %q, want running", statuses[0])
	}
	if statuses[1] != domain.RunStatusSucceeded {
		t.Errorf("second hook status = %q, want succeeded", statuses[1])
	}
}

func TestPipelineExecute_RunUpdateHookOnPauseSkip(t *testing.T) {
	pipeline, req, _, _ := pausedTestFixture(false, true)
	fired := 0
	pipeline.SetRunUpdateHook(func(r *domain.Run) { fired++ })
	if _, err := pipeline.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Errorf("hook fired %d times on pause-skip, want 1", fired)
	}
}
```

(`sync` may need adding to pipeline_test.go imports — check.)

- [x] **Step 2:** Run — FAIL (`SetRunUpdateHook` undefined).

- [x] **Step 3: Implement in `internal/run/pipeline.go`:**

(a) Add the field to `Pipeline`:
```go
	// onRunUpdate, when set, fires after a run is first recorded and after
	// each terminal record (succeeded/failed/skipped). Used by the API's SSE
	// feed; nil-safe; must not block (callers fan out non-blocking).
	onRunUpdate func(*domain.Run)
```

(b) Add the setter + a nil-safe emit helper:
```go
// SetRunUpdateHook registers fn to receive run lifecycle updates.
func (p *Pipeline) SetRunUpdateHook(fn func(*domain.Run)) { p.onRunUpdate = fn }

func (p *Pipeline) emitRunUpdate(run *domain.Run) {
	if p.onRunUpdate != nil {
		p.onRunUpdate(run)
	}
}
```

(c) Call sites in `Execute` — exactly six:
1. Pause-skip path: after the `UpdateStatus` recording the skip, before `return run, nil` → `p.emitRunUpdate(run)`.
2. After the main `runRepo.Insert` success (status running) → `p.emitRunUpdate(run)`.
3. Dedup-skip path: after setting `run.Status = domain.RunStatusSkipped`, before return → `p.emitRunUpdate(run)`.
4. Executor-error path: after run fields populated, before `return run, fmt.Errorf(...)` → `p.emitRunUpdate(run)`.
5. Model-failure (Status != 0) path: before `return run, nil` → `p.emitRunUpdate(run)`.
6. Success path: after `run.FinishedAt` is set, before `return run, nil` → `p.emitRunUpdate(run)`.

- [x] **Step 4:** Run `go test ./internal/run/ -race -count=1` — ALL pass (new + every pre-existing test runs with a nil hook).

- [x] **Step 5: Commit**

```bash
git add internal/run/pipeline.go internal/run/pipeline_test.go
git commit -m "feat(sp4a): additive run-update hook on the pipeline"
```

---

### Task 7: Secrets read-path integration + secrets CLI

**Files:**
- Modify: `cmd/fleet/main.go`

No new automated tests (main.go precedent); the cipher and repo are already tested. Verification is compile + behavioral smoke in Task 12.

- [x] **Step 1: Central decrypt helper + cipher construction.** In main.go add:

```go
// loadCipher returns the secrets cipher, or nil when FLEET_MASTER_KEY is unset
// (legacy-plaintext-only mode). An invalid key is a hard error.
func loadCipher() (*secrets.Cipher, error) {
	keyB64 := os.Getenv(secrets.MasterKeyEnv)
	if keyB64 == "" {
		return nil, nil
	}
	return secrets.NewCipher(keyB64)
}

// decryptSecret resolves a stored value: FSEC1 → decrypt (cipher required);
// legacy plaintext → returned as-is.
func decryptSecret(c *secrets.Cipher, name string, stored []byte) (string, error) {
	if !secrets.IsEncrypted(stored) {
		return string(stored), nil
	}
	if c == nil {
		return "", fmt.Errorf("secret %q is encrypted but %s is not set", name, secrets.MasterKeyEnv)
	}
	plain, err := c.Decrypt(stored)
	if err != nil {
		return "", fmt.Errorf("secret %q: %w", name, err)
	}
	return string(plain), nil
}
```

- [x] **Step 2: Route the two read paths through it.** `buildSecretLookup` and `dbSecretsProvider.Load` gain a `*secrets.Cipher` (field/param — thread it from the call sites, which all already call `loadCipher()` once at command start; a nil cipher is valid). Replace their raw `string(val)` with `decryptSecret(cipher, name, val)`. In `dbSecretsProvider.Load`, a decrypt error for one secret fails the load (loud, names the secret). Update every constructor call site (`runCmd`, `scheduleCmd`, `serveCmd`, `initPlugins` callers) to obtain and pass the cipher; commands fail fast on an invalid key.

- [x] **Step 3: Startup warning in serveCmd.** After initPlugins, list secrets via `repo.NewSecretRepo(pool).List(ctx)` and print a warning naming any rows where `!secrets.IsEncrypted(value)`, plus a note when the master key is unset.

- [x] **Step 4: Add the secrets CLI** (register `root.AddCommand(secretsCmd())`):

```go
func secretsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "secrets", Short: "Secret management commands"}
	cmd.AddCommand(secretsSetCmd(), secretsListCmd(), secretsDeleteCmd(), secretsEncryptExistingCmd())
	return cmd
}

func secretsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name>",
		Short: "Set a secret (value read from stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cipher, err := loadCipher()
			if err != nil {
				return err
			}
			if cipher == nil {
				return fmt.Errorf("%s is not set; refusing to store a plaintext secret", secrets.MasterKeyEnv)
			}
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read value from stdin: %w", err)
			}
			value := strings.TrimRight(string(data), "\r\n")
			if value == "" {
				return fmt.Errorf("empty secret value")
			}
			cfg, _ := loadConfig()
			pool, err := db.New(ctx, mustDSN(cfg))
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			enc, err := cipher.Encrypt([]byte(value))
			if err != nil {
				return err
			}
			if err := repo.NewSecretRepo(pool).Upsert(ctx, args[0], enc); err != nil {
				return fmt.Errorf("store secret: %w", err)
			}
			fmt.Printf("secret %q stored (encrypted)\n", args[0])
			return nil
		},
	}
}
```

`secretsListCmd` prints `NAME / ENCRYPTED` columns from `SecretRepo.List` + `secrets.IsEncrypted` — never values. `secretsDeleteCmd` calls Delete. `secretsEncryptExistingCmd` loads the cipher (required), lists, and for each non-encrypted row re-Upserts `cipher.Encrypt(raw)`, printing a per-secret line and a final count; idempotent because already-encrypted rows are skipped. Add a small `mustDSN(cfg)` helper wrapping the existing `resolveDSN` + empty-check pattern (or inline the existing three-liner; match surrounding style).

- [x] **Step 5: Verify and commit**

Run: `go build ./... && go test ./... -count=1 && go vet ./...`

```bash
git add cmd/fleet/main.go
git commit -m "feat(sp4a): decrypting secret read path and fleet secrets CLI"
```

---

### Task 8: Users CLI

**Files:**
- Modify: `cmd/fleet/main.go`

- [x] **Step 1: Add `usersCmd`** (register in main()):

```go
func usersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "users", Short: "Operator account commands"}
	cmd.AddCommand(usersCreateCmd(), usersListCmd(), usersDeleteCmd())
	return cmd
}

func usersCreateCmd() *cobra.Command {
	var flagRole string
	var flagPasswordStdin bool
	cmd := &cobra.Command{
		Use:   "create <username>",
		Short: "Create an operator account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			if flagRole != domain.RoleAdmin && flagRole != domain.RoleViewer {
				return fmt.Errorf("--role must be admin or viewer")
			}
			var password string
			if flagPasswordStdin {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read password from stdin: %w", err)
				}
				password = strings.TrimRight(string(data), "\r\n")
			} else {
				fmt.Print("Password: ")
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("read password: %w", err)
				}
				password = strings.TrimRight(line, "\r\n")
			}
			hash, err := auth.HashPassword(password)
			if err != nil {
				return err
			}
			cfg, _ := loadConfig()
			pool, err := db.New(ctx, mustDSN(cfg))
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			u := &domain.User{Username: args[0], PasswordHash: hash, Role: flagRole}
			if err := repo.NewUserRepo(pool).Create(ctx, u); err != nil {
				return fmt.Errorf("create user: %w", err)
			}
			fmt.Printf("user %q created with role %s\n", u.Username, u.Role)
			return nil
		},
	}
	cmd.Flags().StringVar(&flagRole, "role", "viewer", "admin or viewer")
	cmd.Flags().BoolVar(&flagPasswordStdin, "password-stdin", false, "read password from stdin")
	return cmd
}
```

`usersListCmd`: table of username/role/created (never hashes). `usersDeleteCmd`: `Use: "delete <username>"`, calls Delete. New imports: `bufio`, `internal/auth` (io/strings present from Task 7).

- [x] **Step 2: Verify and commit**

Run: `go build ./... && go vet ./...`

```bash
git add cmd/fleet/main.go
git commit -m "feat(sp4a): fleet users CLI"
```

---

### Task 9: API core — router, middleware, auth endpoints, broadcaster

**Files:**
- Create: `internal/api/api.go`, `internal/api/middleware.go`, `internal/api/auth_handlers.go`, `internal/api/stream.go`
- Test: `internal/api/middleware_test.go`, `internal/api/stream_test.go`

- [x] **Step 1: `internal/api/api.go`** — the API struct, dependency interfaces, JSON helpers, and Mount:

```go
// Package api implements the /api/v1 REST surface and SSE stream consumed by
// the SP4b SPA. Handlers depend on narrow interfaces (satisfied by the repos,
// the Invoker, and the dispatcher) so the whole package tests with fakes.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

type AgentStore interface {
	List(ctx context.Context) ([]*domain.Agent, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error)
	Insert(ctx context.Context, a *domain.Agent) error
	Update(ctx context.Context, a *domain.Agent) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type DutyStore interface {
	List(ctx context.Context) ([]*domain.Duty, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Duty, error)
	Insert(ctx context.Context, d *domain.Duty) error
	Update(ctx context.Context, d *domain.Duty) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type AssignmentStore interface {
	List(ctx context.Context) ([]*domain.Assignment, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Assignment, error)
	Insert(ctx context.Context, a *domain.Assignment) error
	Update(ctx context.Context, a *domain.Assignment) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type RunStore interface {
	ListFiltered(ctx context.Context, status string, agentID uuid.UUID, limit int) ([]*domain.Run, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Run, error)
	AgentStats(ctx context.Context, agentID uuid.UUID) (*domain.AgentStats, error)
}

type EventStore interface {
	ListRecent(ctx context.Context, status string, limit int) ([]*domain.Event, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Event, error)
	MarkPending(ctx context.Context, id uuid.UUID) error
}

type SecretStore interface {
	Upsert(ctx context.Context, name string, value []byte) error
	List(ctx context.Context) (map[string][]byte, error)
	Delete(ctx context.Context, name string) error
}

type UserStore interface {
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
}

type Invoker interface {
	Invoke(ctx context.Context, assignmentID uuid.UUID, triggerKind string, eventID *string, params map[string]any) (*domain.Run, error)
}

// Encryptor seals secret values; nil means the master key is unset.
type Encryptor interface {
	Encrypt(plain []byte) ([]byte, error)
}

// API carries the dependencies for every handler.
type API struct {
	agents      AgentStore
	duties      DutyStore
	assignments AssignmentStore
	runs        RunStore
	events      EventStore
	secretsRepo SecretStore
	users       UserStore
	sessions    *auth.Sessions
	invoker     Invoker
	encryptor   Encryptor          // nil = no master key
	isEncrypted func([]byte) bool  // secrets.IsEncrypted
	notify      func(uuid.UUID)    // dispatcher nudge for replay; nil-safe
	cfg         *config.Config     // backends listing + validation parity
	broadcaster *Broadcaster
	secureCookies bool
	logf        func(format string, args ...any)

	inner     *http.ServeMux // authenticated routes, built once
	innerOnce sync.Once
}

type Deps struct {
	Agents        AgentStore
	Duties        DutyStore
	Assignments   AssignmentStore
	Runs          RunStore
	Events        EventStore
	Secrets       SecretStore
	Users         UserStore
	Sessions      *auth.Sessions
	Invoker       Invoker
	Encryptor     Encryptor
	IsEncrypted   func([]byte) bool
	Notify        func(uuid.UUID)
	Config        *config.Config
	SecureCookies bool
}

func New(d Deps) *API {
	return &API{
		agents: d.Agents, duties: d.Duties, assignments: d.Assignments,
		runs: d.Runs, events: d.Events, secretsRepo: d.Secrets, users: d.Users,
		sessions: d.Sessions, invoker: d.Invoker, encryptor: d.Encryptor,
		isEncrypted: d.IsEncrypted, notify: d.Notify, cfg: d.Config,
		broadcaster:   NewBroadcaster(),
		secureCookies: d.SecureCookies,
		logf:          func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}
}

// Broadcaster exposes the run-update sink for serve wiring (pipeline hook).
func (a *API) RunUpdateSink() func(*domain.Run) { return a.broadcaster.PublishRun }

// Mount registers all routes on mux. Every route except POST /api/v1/login
// passes through the auth middleware.
func (a *API) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/login", a.handleLogin)
	mux.Handle("/api/v1/", a.requireAuth(http.HandlerFunc(a.route)))
}

// route dispatches authenticated requests on an inner mux (built once).
// Constructed in Mount-time order; Go 1.22 patterns handle method+path.
func (a *API) authedMux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/v1/logout", a.handleLogout)
	m.HandleFunc("GET /api/v1/me", a.handleMe)

	m.HandleFunc("GET /api/v1/agents", a.handleListAgents)
	m.HandleFunc("POST /api/v1/agents", a.handleCreateAgent)
	m.HandleFunc("GET /api/v1/agents/{id}", a.handleGetAgent)
	m.HandleFunc("PATCH /api/v1/agents/{id}", a.handlePatchAgent)
	m.HandleFunc("DELETE /api/v1/agents/{id}", a.handleDeleteAgent)
	m.HandleFunc("GET /api/v1/agents/{id}/stats", a.handleAgentStats)

	m.HandleFunc("GET /api/v1/duties", a.handleListDuties)
	m.HandleFunc("POST /api/v1/duties", a.handleCreateDuty)
	m.HandleFunc("GET /api/v1/duties/{id}", a.handleGetDuty)
	m.HandleFunc("PATCH /api/v1/duties/{id}", a.handlePatchDuty)
	m.HandleFunc("DELETE /api/v1/duties/{id}", a.handleDeleteDuty)

	m.HandleFunc("GET /api/v1/assignments", a.handleListAssignments)
	m.HandleFunc("POST /api/v1/assignments", a.handleCreateAssignment)
	m.HandleFunc("GET /api/v1/assignments/{id}", a.handleGetAssignment)
	m.HandleFunc("PATCH /api/v1/assignments/{id}", a.handlePatchAssignment)
	m.HandleFunc("DELETE /api/v1/assignments/{id}", a.handleDeleteAssignment)
	m.HandleFunc("POST /api/v1/assignments/{id}/run", a.handleRunNow)

	m.HandleFunc("GET /api/v1/runs", a.handleListRuns)
	m.HandleFunc("GET /api/v1/runs/{id}", a.handleGetRun)
	m.HandleFunc("GET /api/v1/events", a.handleListEvents)
	m.HandleFunc("POST /api/v1/events/{id}/replay", a.handleReplayEvent)
	m.HandleFunc("GET /api/v1/backends", a.handleListBackends)
	m.HandleFunc("GET /api/v1/secrets", a.handleListSecrets)
	m.HandleFunc("PUT /api/v1/secrets/{name}", a.handlePutSecret)
	m.HandleFunc("DELETE /api/v1/secrets/{name}", a.handleDeleteSecret)
	m.HandleFunc("GET /api/v1/stream", a.handleStream)
	return m
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func parseIDParam(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(r.PathValue("id"))
}

const defaultListLimit = 50

func parseLimit(r *http.Request) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 && n <= 500 {
			return n
		}
	}
	return defaultListLimit
}

// sessionCookie builds the session cookie consistently.
func (a *API) sessionCookie(value string, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{
		Name: auth.CookieName, Value: value, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: a.secureCookies, MaxAge: int(maxAge.Seconds()),
	}
}
```

(`route` is implemented in middleware.go below; it lazily builds `authedMux` once via the struct's `innerOnce`.)

- [x] **Step 2: `internal/api/middleware.go`:**

```go
package api

import (
	"context"
	"net/http"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
)

type ctxKey int

const (
	ctxKeyRole ctxKey = iota
	ctxKeyUsername
)

// route serves authenticated requests via the inner mux (built once per API
// instance — the once lives on the struct so tests can build several APIs).
func (a *API) route(w http.ResponseWriter, r *http.Request) {
	a.innerOnce.Do(func() { a.inner = a.authedMux() })
	a.inner.ServeHTTP(w, r)
}

// requireAuth authenticates the session cookie and enforces roles:
// viewers may only GET (the SSE stream is a GET).
func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		userID, role, err := a.sessions.Validate(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		_ = userID
		if role != domain.RoleAdmin && r.Method != http.MethodGet {
			writeError(w, http.StatusForbidden, "viewer role is read-only")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyRole, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

- [x] **Step 3: `internal/api/auth_handlers.go`:**

```go
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/cbarraford/office-fleet/internal/auth"
)

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, err := a.users.GetByUsername(r.Context(), body.Username)
	if err != nil {
		a.logf("api: login lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Uniform 401 for unknown user and wrong password (no username oracle).
	// VerifyPassword on a dummy hash keeps timing roughly uniform.
	if user == nil {
		_ = auth.VerifyPassword("pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA==$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", body.Password)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, body.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, err := a.sessions.Start(r.Context(), user.ID)
	if err != nil {
		a.logf("api: start session: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	http.SetCookie(w, a.sessionCookie(token, auth.SessionTTL))
	writeJSON(w, http.StatusOK, map[string]string{"username": user.Username, "role": user.Role})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.CookieName); err == nil {
		_ = a.sessions.End(r.Context(), cookie.Value)
	}
	expired := a.sessionCookie("", -time.Hour)
	http.SetCookie(w, expired)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	role, _ := r.Context().Value(ctxKeyRole).(string)
	writeJSON(w, http.StatusOK, map[string]string{"role": role})
}
```

(`/me` returning only the role is sufficient for SP4a; SP4b can extend. If username-in-context is wanted, thread it from Validate via a Users lookup — out of scope.)

- [x] **Step 4: `internal/api/stream.go`** — Broadcaster + SSE handler:

```go
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// Broadcaster fans run updates out to SSE subscribers. Publishing never
// blocks: a slow subscriber's buffer fills and messages are dropped for that
// subscriber only — the feed is advisory, the runs table is truth.
type Broadcaster struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[chan []byte]struct{}{}}
}

func (b *Broadcaster) Subscribe() chan []byte {
	ch := make(chan []byte, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// PublishRun emits run_started (status running) or run_finished (terminal).
func (b *Broadcaster) PublishRun(run *domain.Run) {
	event := "run_finished"
	if run.Status == domain.RunStatusRunning {
		event = "run_started"
	}
	payload, err := json.Marshal(map[string]any{
		"event": event, "id": run.ID, "assignment_id": run.AssignmentID,
		"agent_id": run.AgentID, "duty_id": run.DutyID,
		"trigger_kind": run.TriggerKind, "status": run.Status,
		"tokens": run.Tokens, "cost": run.Cost,
	})
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- payload:
		default: // slow consumer: drop
		}
	}
}

func (a *API) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := a.broadcaster.Subscribe()
	defer a.broadcaster.Unsubscribe(ch)
	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-ch:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}
```

- [x] **Step 5: Write the middleware test** — `internal/api/middleware_test.go` (the mem session store defined here is REUSED by entity/integration tests in Tasks 10–11):

```go
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// memSessionStore is an in-memory auth.SessionStore whose role is fixed per
// store instance (tests build one per role).
type memSessionStore struct {
	mu   sync.Mutex
	role string
	rows map[string]uuid.UUID
}

func newMemSessionStore(role string) *memSessionStore {
	return &memSessionStore{role: role, rows: map[string]uuid.UUID{}}
}

func (m *memSessionStore) Create(_ context.Context, tokenHash string, userID uuid.UUID, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[tokenHash] = userID
	return nil
}

func (m *memSessionStore) Lookup(_ context.Context, tokenHash string) (uuid.UUID, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.rows[tokenHash]
	if !ok {
		return uuid.Nil, "", fmt.Errorf("session not found")
	}
	return id, m.role, nil
}

func (m *memSessionStore) Delete(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, tokenHash)
	return nil
}

func (m *memSessionStore) DeleteExpired(_ context.Context) error { return nil }

// authedClient returns a server over a minimal API plus a logged-in client
// cookie for the given role.
func authedAPI(t *testing.T, role string) (*API, string) {
	t.Helper()
	sessions := auth.NewSessions(newMemSessionStore(role))
	a := New(Deps{Sessions: sessions})
	token, err := sessions.Start(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return a, token
}

func doReq(t *testing.T, handler http.Handler, method, path, token string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(method, srv.URL+path, nil)
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

func mountedMux(a *API) *http.ServeMux {
	mux := http.NewServeMux()
	a.Mount(mux)
	return mux
}

func TestMiddleware_NoCookie401(t *testing.T) {
	a, _ := authedAPI(t, domain.RoleAdmin)
	resp := doReq(t, mountedMux(a), http.MethodGet, "/api/v1/me", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_BadToken401(t *testing.T) {
	a, _ := authedAPI(t, domain.RoleAdmin)
	resp := doReq(t, mountedMux(a), http.MethodGet, "/api/v1/me", "not-a-real-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_ViewerGetAllowed(t *testing.T) {
	a, token := authedAPI(t, domain.RoleViewer)
	resp := doReq(t, mountedMux(a), http.MethodGet, "/api/v1/me", token)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMiddleware_ViewerMutation403(t *testing.T) {
	a, token := authedAPI(t, domain.RoleViewer)
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		resp := doReq(t, mountedMux(a), method, "/api/v1/agents", token)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", method, resp.StatusCode)
		}
	}
}

func TestMiddleware_AdminMutationPasses(t *testing.T) {
	// Admin POST must clear the middleware (handler itself may 4xx/5xx on the
	// nil stores — assert specifically NOT 401/403).
	a, token := authedAPI(t, domain.RoleAdmin)
	resp := doReq(t, mountedMux(a), http.MethodPost, "/api/v1/logout", token)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Errorf("admin POST blocked by middleware: %d", resp.StatusCode)
	}
}
```

Also write `internal/api/stream_test.go` per this contract (complete the code yourself following the broadcaster API): subscribe two channels; `PublishRun` with a running run → both receive a payload containing `"event":"run_started"` and the run id; publish 20 messages while one subscriber never reads → the publish loop completes within a 1s watchdog (no blocking) and the reading subscriber got at least its buffer's worth; after `Unsubscribe`, a publish delivers nothing to that channel. Use `domain.Run{ID: uuid.New(), Status: domain.RunStatusRunning}` / `RunStatusSucceeded` fixtures.

`internal/api/stream_test.go`: subscribe two channels; PublishRun(running) → both receive run_started; fill one subscriber's buffer (17 publishes) → publisher never blocks (publish in the test goroutine with a 1s watchdog), the healthy subscriber received everything its buffer allowed; Unsubscribe removes delivery.

Write complete tests following those specifications — assert payload JSON contains `"event":"run_started"` / `"run_finished"` and the run id.

- [x] **Step 6:** Run `go test ./internal/api/ -race -v` — PASS. Build/vet/gofmt clean.

- [x] **Step 7: Commit**

```bash
git add internal/api/
git commit -m "feat(sp4a): api core — router, session middleware, auth endpoints, sse broadcaster"
```

---

### Task 10: Entity CRUD handlers

**Files:**
- Create: `internal/api/entity_handlers.go`
- Test: `internal/api/entity_handlers_test.go` (fakes for the three stores)

- [x] **Step 1: Implement `entity_handlers.go`.** Pattern per entity (agents shown; duties/assignments follow identically with their fields):

```go
package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// --- Agents ---

func (a *API) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := a.agents.List(r.Context())
	if err != nil {
		a.logf("api: list agents: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

type agentBody struct {
	Name           *string            `json:"name"`
	Role           *string            `json:"role"`
	SystemPrompt   *string            `json:"system_prompt"`
	DefaultBackend *domain.BackendRef `json:"default_backend"`
	Enabled        *bool              `json:"enabled"`
	AvatarURL      *string            `json:"avatar_url"`
	HiredAt        *string            `json:"hired_at"` // YYYY-MM-DD
}

type validationError string

func (e validationError) Error() string { return string(e) }
func errValidation(msg string) error    { return validationError(msg) }

// backendNameExists reports whether a BackendRef names a configured backend.
func (a *API) backendNameExists(ref *domain.BackendRef) bool {
	if ref == nil || ref.Name == "" {
		return true // empty ref is legal (resolution falls through)
	}
	for i := range a.cfg.Backends {
		if a.cfg.Backends[i].Name == ref.Name {
			return true
		}
	}
	return false
}

// applyAgentBody applies provided (non-nil) fields onto agent — PATCH
// semantics; create passes a zero-value agent.
func (a *API) applyAgentBody(b *agentBody, agent *domain.Agent) error {
	if b.Name != nil {
		if strings.TrimSpace(*b.Name) == "" {
			return errValidation("name must not be empty")
		}
		agent.Name = *b.Name
	}
	if b.Role != nil {
		agent.Role = *b.Role
	}
	if b.SystemPrompt != nil {
		agent.SystemPrompt = *b.SystemPrompt
	}
	if b.DefaultBackend != nil {
		if !a.backendNameExists(b.DefaultBackend) {
			return errValidation("unknown backend " + b.DefaultBackend.Name)
		}
		agent.DefaultBackend = *b.DefaultBackend
	}
	if b.Enabled != nil {
		agent.Enabled = *b.Enabled
	}
	if b.AvatarURL != nil {
		agent.AvatarURL = b.AvatarURL
	}
	if b.HiredAt != nil {
		t, err := time.Parse("2006-01-02", *b.HiredAt)
		if err != nil {
			return errValidation("hired_at must be YYYY-MM-DD")
		}
		agent.HiredAt = &t
	}
	return nil
}
```

Create/patch/delete handlers:

```go
func (a *API) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var body agentBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	agent := &domain.Agent{Enabled: true}
	if err := a.applyAgentBody(&body, agent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if agent.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := a.agents.Insert(r.Context(), agent); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an agent with that name already exists")
			return
		}
		a.logf("api: create agent: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, agent)
}

func (a *API) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	agent, err := a.agents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	stats, err := a.runs.AgentStats(r.Context(), id)
	if err != nil {
		a.logf("api: agent stats: %v", err)
		stats = nil // embed-best-effort: agent detail still loads
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agent, "stats": stats})
}

func (a *API) handlePatchAgent(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	agent, err := a.agents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	var body agentBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := a.applyAgentBody(&body, agent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.agents.Update(r.Context(), agent); err != nil {
		a.logf("api: update agent: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (a *API) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.agents.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// isUniqueViolation matches Postgres unique-violation errors without
// importing pgconn: SQLSTATE 23505 appears in the error text.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}
```

Duties: same shape; `dutyBody` carries name/role/description/trigger_kinds/prompt/required_tools/output_actions/config_schema/backend; validate trigger_kinds values ∈ {manual, cron, event-subscription, continuous} and backend via `backendNameExists`.

Assignments: `assignmentBody` carries agent_id/duty_id (create only), enabled/trigger/outputs/config/backend/task_prompt_override/extra_instructions; validation parity:

```go
// validateAssignment mirrors config.Validate's event-subscription rules so
// the API cannot create what the dispatcher would reject.
func (a *API) validateAssignment(ctx context.Context, asg *domain.Assignment) error {
	if asg.Trigger.Kind == "event-subscription" {
		src, _ := asg.Trigger.Filter["source"].(string)
		typ, _ := asg.Trigger.Filter["event_type"].(string)
		if src == "" || typ == "" {
			return errValidation("event-subscription trigger requires non-empty filter.source and filter.event_type")
		}
	}
	if asg.Trigger.Kind != "" {
		duty, err := a.duties.GetByID(ctx, asg.DutyID)
		if err == nil && len(duty.TriggerKinds) > 0 && !slices.Contains(duty.TriggerKinds, asg.Trigger.Kind) {
			return errValidation("duty does not support trigger kind " + asg.Trigger.Kind)
		}
	}
	if !a.backendNameExists(asg.Backend) {
		return errValidation("unknown backend " + asg.Backend.Name)
	}
	return nil
}
```

- [x] **Step 2: Write the tests** — `entity_handlers_test.go`: in-memory fakes for AgentStore/DutyStore/AssignmentStore/RunStore (maps keyed by id; Insert assigns uuid; Update/Delete error on missing); an apiFixture helper building `New(Deps{...})` with an admin session pre-seeded (reuse a mem session store + `auth.NewSessions`) and an `http.Client` with the cookie set; tests:
  - create agent → 201 + listed; duplicate name (fake returns an error containing "23505") → 409.
  - PATCH partial: set only `{"enabled":false}` → other fields unchanged; bad hired_at → 400; unknown backend ref → 400.
  - GET /agents/{id} embeds stats from the fake RunStore.
  - delete → subsequent GET 404.
  - assignment create with event-subscription missing source → 400; with mismatched duty trigger_kinds → 400; valid → 201.
  - duty create with bad trigger kind value → 400.

Write the complete fixture + tests (model them on the route table; each is a few lines of request/assert via httptest.NewServer(api-mounted mux)).

- [x] **Step 3:** Run `go test ./internal/api/ -race -v` — PASS.

- [x] **Step 4: Commit**

```bash
git add internal/api/
git commit -m "feat(sp4a): entity CRUD handlers with validation parity"
```

---

### Task 11: Records handlers (runs/events/backends/stats/secrets/run-now/replay) + API integration test

**Files:**
- Create: `internal/api/record_handlers.go`
- Test: `internal/api/api_test.go` (the package-level integration suite)

- [x] **Step 1: Implement `record_handlers.go`:**

```go
package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/google/uuid"
)

func (a *API) handleListRuns(w http.ResponseWriter, r *http.Request) {
	var agentID uuid.UUID
	if v := r.URL.Query().Get("agent_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid agent_id")
			return
		}
		agentID = id
	}
	runs, err := a.runs.ListFiltered(r.Context(), r.URL.Query().Get("status"), agentID, parseLimit(r))
	if err != nil {
		a.logf("api: list runs: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (a *API) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	run, err := a.runs.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (a *API) handleListEvents(w http.ResponseWriter, r *http.Request) {
	events, err := a.events.ListRecent(r.Context(), r.URL.Query().Get("status"), parseLimit(r))
	if err != nil {
		a.logf("api: list events: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (a *API) handleReplayEvent(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := a.events.GetByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err := a.events.MarkPending(r.Context(), id); err != nil {
		a.logf("api: replay event: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if a.notify != nil {
		a.notify(id) // in-process dispatcher nudge: immediate redispatch
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "requeued"})
}

func (a *API) handleListBackends(w http.ResponseWriter, r *http.Request) {
	type backendView struct {
		Name          string `json:"name"`
		Kind          string `json:"kind"`
		AuthMode      string `json:"auth_mode"`
		Model         string `json:"model,omitempty"`
		DefaultEffort string `json:"default_effort,omitempty"`
	}
	out := make([]backendView, 0, len(a.cfg.Backends))
	for i := range a.cfg.Backends {
		b := &a.cfg.Backends[i]
		out = append(out, backendView{
			Name: b.Name, Kind: b.Kind, AuthMode: b.Auth.Mode,
			Model: b.Model, DefaultEffort: b.DefaultEffort,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleAgentStats(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	stats, err := a.runs.AgentStats(r.Context(), id)
	if err != nil {
		a.logf("api: agent stats: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (a *API) handleRunNow(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Params map[string]any `json:"params"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // empty body = no params
	}
	if body.Params == nil {
		body.Params = map[string]any{}
	}
	run, err := a.invoker.Invoke(r.Context(), id, "manual", nil, body.Params)
	if err != nil {
		a.logf("api: run-now %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "run failed to start: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (a *API) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	raw, err := a.secretsRepo.List(r.Context())
	if err != nil {
		a.logf("api: list secrets: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type entry struct {
		Name      string `json:"name"`
		Encrypted bool   `json:"encrypted"`
	}
	out := make([]entry, 0, len(raw))
	for name, v := range raw {
		out = append(out, entry{Name: name, Encrypted: a.isEncrypted(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handlePutSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "secret name required")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}
	if a.encryptor == nil {
		writeError(w, http.StatusInternalServerError, "master key not configured")
		return
	}
	enc, err := a.encryptor.Encrypt([]byte(body.Value))
	if err != nil {
		a.logf("api: encrypt secret: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := a.secretsRepo.Upsert(r.Context(), name, enc); err != nil {
		a.logf("api: store secret: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "encrypted": true})
}

func (a *API) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.secretsRepo.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
```

- [x] **Step 2: Write the integration suite** — `internal/api/api_test.go`. One fixture: full API over all fakes + a real `auth.Sessions`/mem store + a real user with a PBKDF2 hash (use a REDUCED-iteration hash here? NO — hash once in the fixture with the real function; one hash ≈ 0.3s, acceptable). Cover:
  - login wrong password → 401 (uniform body); login unknown user → 401 same body.
  - login ok → cookie set HttpOnly; `/me` → role; logout → subsequent call 401.
  - viewer user: GET /runs 200, PUT secret 403.
  - run-now: posts params `{"mr_iid": "7"}` → fake invoker records (id, "manual", nil, params) and returns a run → 200 echoes it.
  - replay: fake event store MarkPending called; fake notify records the id.
  - backends: redaction (the view never contains an api_key — construct a config WITH an api_key and assert the response body does not contain it).
  - secrets: PUT → fake store received FSEC1-prefixed bytes (use the REAL secrets.Cipher as Encryptor + secrets.IsEncrypted); GET list returns name+encrypted only and the body does NOT contain the plaintext value; DELETE.
  - SSE: connect with cookie, fire `api.RunUpdateSink()(run)` twice (running + succeeded), read two `data:` frames asserting run_started then run_finished (use a bufio.Reader over the response body with a deadline).

Write the complete fixture and tests.

- [x] **Step 3:** Run `go test ./internal/api/ -race -count=1` — PASS (~1-2s incl. one real PBKDF2 hash).

- [x] **Step 4: Commit**

```bash
git add internal/api/
git commit -m "feat(sp4a): record handlers, run-now, secrets api, and api integration suite"
```

---

### Task 12: Serve wiring, config flag, sample config, final verification

**Files:**
- Modify: `internal/server/server.go` (+ `server_test.go`), `internal/config/config.go` (+ test), `cmd/fleet/main.go`, `configs/fleet.yaml`

- [x] **Step 1: `server.Handler` gains mounts.** Change the signature and add a test:

```go
// Handler builds the HTTP mux: webhooks + healthz, plus any extra mounts
// (the /api/v1 surface in fleet serve).
func (s *Server) Handler(mounts ...func(*http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /webhooks/{plugin}", s.handleWebhook)
	for _, mount := range mounts {
		mount(mux)
	}
	return mux
}
```

Existing call sites compile unchanged (variadic). Add to server_test.go:

```go
func TestHandlerMounts(t *testing.T) {
	srv := httptest.NewServer(New(&fakeIngestor{}).Handler(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("pong"))
		})
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "pong" {
		t.Errorf("mounted route: %d %q", resp.StatusCode, body)
	}
}
```

(add `io` import if missing).

- [x] **Step 2: ServeConfig.SecureCookies.** In config.go add `SecureCookies bool \`yaml:"secure_cookies,omitempty"\`` to ServeConfig (booleans need no validation). Config test: a serve block with `secure_cookies: true` parses (extend `TestValidate_ServeBlock`'s valid case construction with the field).

- [x] **Step 3: Wire the API in serveCmd.** After the dispatcher/ingestor construction and before the http.Server:

```go
			cipher, err := loadCipher()
			if err != nil {
				return err
			}
			var enc api.Encryptor
			if cipher != nil {
				enc = cipher
			}
			apiSrv := api.New(api.Deps{
				Agents:        repo.NewAgentRepo(pool),
				Duties:        repo.NewDutyRepo(pool),
				Assignments:   repo.NewAssignmentRepo(pool),
				Runs:          repo.NewRunRepo(pool),
				Events:        eventRepo,
				Secrets:       repo.NewSecretRepo(pool),
				Users:         repo.NewUserRepo(pool),
				Sessions:      auth.NewSessions(repo.NewSessionRepo(pool)),
				Invoker:       inv,
				Encryptor:     enc,
				IsEncrypted:   secrets.IsEncrypted,
				Notify:        dispatcher.Notify,
				Config:        cfg,
				SecureCookies: cfg.Serve.SecureCookies,
			})
			pipeline.SetRunUpdateHook(apiSrv.RunUpdateSink())
			// httpSrv handler:
			httpSrv := &http.Server{Addr: addr, Handler: server.New(ingestor).Handler(apiSrv.Mount)}
```

PREREQUISITE for the `pipeline` variable: `buildInvoker` constructs the Pipeline internally — the hook needs that same Pipeline. Refactor it to `func buildInvoker(cfg *config.Config, pool *pgxpool.Pool) (*run.Invoker, *run.Pipeline)` and update both call sites: scheduleCmd uses `inv, _ := buildInvoker(...)`; serveCmd uses `inv, pipeline := buildInvoker(...)` (the serveCmd snippet above assumes these names). The nil-Encryptor case: passing a typed-nil `*secrets.Cipher` directly as the interface would make `a.encryptor != nil` true — hence the explicit `var enc api.Encryptor` + conditional assignment above. Preserve that exact pattern.

- [x] **Step 4: Sample config.** In configs/fleet.yaml's serve block add:

```yaml
#  secure_cookies: true  # set when serving behind TLS
```

- [x] **Step 5: Full verification**

```bash
gofmt -l . && go vet ./... && go test ./... -count=1 && go test ./internal/... -race -count=1
FLEET_DATABASE_DSN=postgres://localhost/x go run ./cmd/fleet --config configs/fleet.yaml config validate
go run ./cmd/fleet --config configs/fleet.yaml users --help
go run ./cmd/fleet --config configs/fleet.yaml secrets --help
go run ./cmd/fleet --config configs/fleet.yaml seed --help
go run ./cmd/fleet --config configs/fleet.yaml serve --help
```
Expected: all clean/green; helps render.

- [x] **Step 6: SP1–SP3b stability check**

```bash
git diff 0252e93 --stat -- internal/agentloop/ internal/executor/ internal/events/ internal/plugins/ internal/outputs/ internal/prompt/ internal/trigger/ internal/state/
```
Expected: empty — none of the frozen packages changed. (`internal/run/pipeline.go`, `internal/server/server.go`, `internal/seed`, `internal/repo`, `internal/config`, `internal/domain` legitimately changed per this plan.)

- [x] **Step 7: Commit and push**

```bash
git add internal/server/ internal/config/ cmd/fleet/main.go configs/fleet.yaml
git commit -m "feat(sp4a): mount api into fleet serve; secure_cookies flag"
git push origin master
```

---

## Acceptance criteria traceability (spec §9)

| Spec AC | Covered by |
|---|---|
| 1. Seed only-if-empty; --force; UI edits survive migrate | Task 5 (guard + tests + CLI) |
| 2. Users CLI, login/logout/me, 401/403 matrix, hashed sessions | Tasks 3, 8, 9, 11 |
| 3. AES-GCM at rest, encrypt-existing, transparent decrypt, key-absent behavior, values never exposed | Tasks 2, 7, 11 (FSEC1 assertions; list redaction) |
| 4. Entity CRUD + validation parity; PATCH enabled=false pauses | Task 10 (+ existing pause gate) |
| 5. Run-now via Invoker; runs/events/replay/backends/stats endpoints | Task 11 |
| 6. SSE started/finished; slow-consumer isolation | Tasks 6 (hook), 9 (broadcaster), 11 (SSE test) |
| 7. serve hosts everything; SP1–SP3b unchanged; fmt/vet; zero new deps | Task 12 |
