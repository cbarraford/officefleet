# SP1 — Core Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the SP1 vertical slice of OfficeFleet — a "developer" Agent assigned an `mr-reviewer` Duty, runnable end-to-end via CLI using `fleet run`, with a Claude CLI executor, GitLab plugin, per-assignment state, and complete Run recording.

**Architecture:** Modular Go monolith. Frozen contracts (domain types, interfaces) are already written. Remaining work fans out across leaf packages (db, repo, plugins/gitlab, executor, prompt, state, outputs) then integrates in the run pipeline and CLI. All LLM calls are stubbed behind a Fake executor; real claude CLI is the production path.

**Tech Stack:** Go 1.22+, PostgreSQL (pgx/v5), Cobra CLI, text/template, gopkg.in/yaml.v3

**Module:** `github.com/cbarraford/office-fleet`

**Already written (frozen contracts — do not change signatures):**
- `internal/domain/types.go` — Agent, Duty, Assignment, Run, LLMResult, BackendRef, TriggerConfig, OutputBinding, OutputActionType, OutputDelivery, RunStatus
- `internal/plugin/plugin.go` — Plugin interface, Register(), Get(), All()
- `internal/executor/executor.go` — LLMRequest, Executor interface
- `internal/trigger/trigger.go` + `cron.go` — ManualTrigger, CronTrigger, Scheduler
- `internal/state/store.go` — Store interface
- `internal/config/config.go` — Config, Backend, ResolveBackend(), Load(), Validate()
- `internal/db/migrations/001_initial_schema.sql` — full schema (agents, duties, assignments, runs, assignment_state, assignment_notes, assignment_processed, secrets)

---

## File Map

| File | Responsibility |
|------|---------------|
| `internal/db/db.go` | pgx pool init, migrations runner |
| `internal/repo/agents.go` | Agent CRUD (Insert, GetByName, List) |
| `internal/repo/duties.go` | Duty CRUD (Insert, GetByName, List) |
| `internal/repo/assignments.go` | Assignment CRUD (Insert, GetByAgentAndDuty, List) |
| `internal/repo/runs.go` | Run CRUD (Insert, UpdateStatus, UpdateResult) |
| `internal/plugins/gitlab/gitlab.go` | GitLab plugin: Init, Do(post_mr_comment), Actions(), EventSources() |
| `internal/executor/claude.go` | ClaudeExecutor: shells out to `claude` CLI with system+task prompt, captures JSON result |
| `internal/executor/fake.go` | FakeExecutor: returns canned LLMResult for tests |
| `internal/prompt/engine.go` | Render(template, context) using text/template; three-layer composer |
| `internal/state/postgres.go` | PostgresStore: implements Store over pgx |
| `internal/outputs/deliver.go` | Deliver(ctx, outputs, llmResult, plugins) — renders params, calls plugin.Do |
| `internal/run/pipeline.go` | RunPipeline.Execute: resolve backend → render prompts → run executor → deliver outputs → record Run |
| `cmd/fleet/main.go` | cobra root; subcommands: migrate, config validate, backends list/login, agents/duties/assignments list, run, schedule |
| `configs/fleet.yaml` | Sample config: claude-default backend, dev-1 agent, mr-reviewer duty, one assignment |

---

## Task 1: DB package — pool + migrations runner

**Files:**
- Create: `internal/db/db.go`
- Test: `internal/db/db_test.go`

- [x] **Step 1: Write `internal/db/db.go`**

```go
package db

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// New opens a pgx connection pool.
func New(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return pool, nil
}

// Migrate runs all pending UP migrations in order.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version := strings.TrimSuffix(e.Name(), ".sql")

		var applied bool
		_ = pool.QueryRow(ctx, `SELECT TRUE FROM schema_migrations WHERE version=$1`, version).Scan(&applied)
		if applied {
			continue
		}

		data, err := migrationFS.ReadFile(filepath.Join("migrations", e.Name()))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}

		upSQL := extractUpBlock(string(data))
		if _, err := pool.Exec(ctx, upSQL); err != nil {
			return fmt.Errorf("apply migration %s: %w", e.Name(), err)
		}

		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, version); err != nil {
			return fmt.Errorf("record migration %s: %w", e.Name(), err)
		}
	}
	return nil
}

// extractUpBlock returns only the SQL between -- +migrate Up and -- +migrate Down.
func extractUpBlock(sql string) string {
	up := strings.Index(sql, "-- +migrate Up")
	if up == -1 {
		return sql
	}
	sql = sql[up+len("-- +migrate Up"):]
	down := strings.Index(sql, "-- +migrate Down")
	if down != -1 {
		sql = sql[:down]
	}
	return strings.TrimSpace(sql)
}
```

- [x] **Step 2: Write `internal/db/db_test.go`** (compile-only; real DB test lives in integration suite)

```go
package db

import "testing"

func TestExtractUpBlock(t *testing.T) {
	sql := `-- +migrate Up
CREATE TABLE foo (id INT);
-- +migrate Down
DROP TABLE foo;`
	got := extractUpBlock(sql)
	if got != "CREATE TABLE foo (id INT);" {
		t.Fatalf("unexpected: %q", got)
	}
}
```

- [x] **Step 3: Run tests**

```bash
go test ./internal/db/...
```
Expected: PASS

- [x] **Step 4: Commit**

```bash
git add internal/db/
git commit -m "feat(db): pgx pool + embed migrations runner"
```

---

## Task 2: Repo — Agent, Duty, Assignment CRUD

**Files:**
- Create: `internal/repo/agents.go`
- Create: `internal/repo/duties.go`
- Create: `internal/repo/assignments.go`

- [x] **Step 1: Write `internal/repo/agents.go`**

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

type AgentRepo struct{ db *pgxpool.Pool }

func NewAgentRepo(db *pgxpool.Pool) *AgentRepo { return &AgentRepo{db: db} }

func (r *AgentRepo) Insert(ctx context.Context, a *domain.Agent) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	backendJSON, _ := json.Marshal(a.DefaultBackend)
	_, err := r.db.Exec(ctx, `
		INSERT INTO agents (id, name, role, system_prompt, default_backend, enabled)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		a.ID, a.Name, a.Role, a.SystemPrompt, backendJSON, a.Enabled)
	return err
}

func (r *AgentRepo) GetByName(ctx context.Context, name string) (*domain.Agent, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, name, role, system_prompt, default_backend, enabled, created_at, updated_at
		FROM agents WHERE name=$1`, name)
	return scanAgent(row)
}

func (r *AgentRepo) List(ctx context.Context) ([]*domain.Agent, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, role, system_prompt, default_backend, enabled, created_at, updated_at
		FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAgent(s scanner) (*domain.Agent, error) {
	var a domain.Agent
	var backendJSON []byte
	if err := s.Scan(&a.ID, &a.Name, &a.Role, &a.SystemPrompt, &backendJSON, &a.Enabled, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan agent: %w", err)
	}
	_ = json.Unmarshal(backendJSON, &a.DefaultBackend)
	return &a, nil
}
```

- [x] **Step 2: Write `internal/repo/duties.go`**

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

type DutyRepo struct{ db *pgxpool.Pool }

func NewDutyRepo(db *pgxpool.Pool) *DutyRepo { return &DutyRepo{db: db} }

func (r *DutyRepo) Insert(ctx context.Context, d *domain.Duty) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	outputActionsJSON, _ := json.Marshal(d.OutputActions)
	configSchemaJSON, _ := json.Marshal(d.ConfigSchema)
	var backendJSON []byte
	if d.Backend != nil {
		backendJSON, _ = json.Marshal(d.Backend)
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO duties (id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		d.ID, d.Name, d.Role, d.Description, d.TriggerKinds, d.Prompt, d.RequiredTools,
		outputActionsJSON, configSchemaJSON, backendJSON)
	return err
}

func (r *DutyRepo) GetByName(ctx context.Context, name string) (*domain.Duty, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend, created_at, updated_at
		FROM duties WHERE name=$1`, name)
	return scanDuty(row)
}

func (r *DutyRepo) List(ctx context.Context) ([]*domain.Duty, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend, created_at, updated_at
		FROM duties ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Duty
	for rows.Next() {
		d, err := scanDuty(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanDuty(s scanner) (*domain.Duty, error) {
	var d domain.Duty
	var outputActionsJSON, configSchemaJSON []byte
	var backendJSON []byte
	if err := s.Scan(&d.ID, &d.Name, &d.Role, &d.Description, &d.TriggerKinds, &d.Prompt,
		&d.RequiredTools, &outputActionsJSON, &configSchemaJSON, &backendJSON,
		&d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan duty: %w", err)
	}
	_ = json.Unmarshal(outputActionsJSON, &d.OutputActions)
	_ = json.Unmarshal(configSchemaJSON, &d.ConfigSchema)
	if len(backendJSON) > 0 {
		var b domain.BackendRef
		_ = json.Unmarshal(backendJSON, &b)
		d.Backend = &b
	}
	return &d, nil
}
```

- [x] **Step 3: Write `internal/repo/assignments.go`**

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

type AssignmentRepo struct{ db *pgxpool.Pool }

func NewAssignmentRepo(db *pgxpool.Pool) *AssignmentRepo { return &AssignmentRepo{db: db} }

func (r *AssignmentRepo) Insert(ctx context.Context, a *domain.Assignment) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	triggerJSON, _ := json.Marshal(a.Trigger)
	outputsJSON, _ := json.Marshal(a.Outputs)
	configJSON, _ := json.Marshal(a.Config)
	var backendJSON []byte
	if a.Backend != nil {
		backendJSON, _ = json.Marshal(a.Backend)
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO assignments (id, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		a.ID, a.AgentID, a.DutyID, a.Enabled, triggerJSON, outputsJSON, configJSON,
		backendJSON, a.TaskPromptOverride, a.ExtraInstructions)
	return err
}

func (r *AssignmentRepo) GetByAgentAndDuty(ctx context.Context, agentID, dutyID uuid.UUID) (*domain.Assignment, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, agent_id, duty_id, enabled, trigger, outputs, config, backend,
		       task_prompt_override, extra_instructions, created_at, updated_at
		FROM assignments WHERE agent_id=$1 AND duty_id=$2 LIMIT 1`, agentID, dutyID)
	return scanAssignment(row)
}

func (r *AssignmentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Assignment, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, agent_id, duty_id, enabled, trigger, outputs, config, backend,
		       task_prompt_override, extra_instructions, created_at, updated_at
		FROM assignments WHERE id=$1`, id)
	return scanAssignment(row)
}

func (r *AssignmentRepo) List(ctx context.Context) ([]*domain.Assignment, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, agent_id, duty_id, enabled, trigger, outputs, config, backend,
		       task_prompt_override, extra_instructions, created_at, updated_at
		FROM assignments ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAssignment(s scanner) (*domain.Assignment, error) {
	var a domain.Assignment
	var triggerJSON, outputsJSON, configJSON, backendJSON []byte
	if err := s.Scan(&a.ID, &a.AgentID, &a.DutyID, &a.Enabled,
		&triggerJSON, &outputsJSON, &configJSON, &backendJSON,
		&a.TaskPromptOverride, &a.ExtraInstructions,
		&a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan assignment: %w", err)
	}
	_ = json.Unmarshal(triggerJSON, &a.Trigger)
	_ = json.Unmarshal(outputsJSON, &a.Outputs)
	_ = json.Unmarshal(configJSON, &a.Config)
	if len(backendJSON) > 2 {
		var b domain.BackendRef
		_ = json.Unmarshal(backendJSON, &b)
		a.Backend = &b
	}
	return &a, nil
}
```

- [x] **Step 4: Compile check**

```bash
go build ./internal/repo/...
```
Expected: no errors

- [x] **Step 5: Commit**

```bash
git add internal/repo/
git commit -m "feat(repo): Agent, Duty, Assignment CRUD"
```

---

## Task 3: Repo — Run CRUD

**Files:**
- Create: `internal/repo/runs.go`

- [x] **Step 1: Write `internal/repo/runs.go`**

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

type RunRepo struct{ db *pgxpool.Pool }

func NewRunRepo(db *pgxpool.Pool) *RunRepo { return &RunRepo{db: db} }

func (r *RunRepo) Insert(ctx context.Context, run *domain.Run) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	outputsJSON, _ := json.Marshal(run.OutputsDelivered)
	_, err := r.db.Exec(ctx, `
		INSERT INTO runs (id, assignment_id, agent_id, duty_id, trigger_kind, event_id,
		                  rendered_system_prompt, rendered_prompt, outputs_delivered, status, started_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		run.ID, run.AssignmentID, run.AgentID, run.DutyID, run.TriggerKind, run.EventID,
		run.RenderedSystemPrompt, run.RenderedPrompt, outputsJSON, run.Status, run.StartedAt)
	return err
}

func (r *RunRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.RunStatus, errMsg *string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE runs SET status=$1, error=$2, finished_at=NOW() WHERE id=$3`,
		status, errMsg, id)
	return err
}

func (r *RunRepo) UpdateResult(ctx context.Context, id uuid.UUID, result *domain.LLMResult, outputs []domain.OutputDelivery, status domain.RunStatus) error {
	resultJSON, _ := json.Marshal(result)
	outputsJSON, _ := json.Marshal(outputs)
	var tokens int
	var cost float64
	if result != nil {
		tokens = result.Tokens
		cost = result.Cost
	}
	_, err := r.db.Exec(ctx, `
		UPDATE runs SET llm_result=$1, outputs_delivered=$2, status=$3, tokens=$4, cost=$5, finished_at=NOW()
		WHERE id=$6`,
		resultJSON, outputsJSON, status, tokens, cost, id)
	return err
}

func (r *RunRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Run, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, assignment_id, agent_id, duty_id, trigger_kind, event_id,
		       rendered_system_prompt, rendered_prompt, llm_result, outputs_delivered,
		       status, tokens, cost, started_at, finished_at, error
		FROM runs WHERE id=$1`, id)
	return scanRun(row)
}

func scanRun(s scanner) (*domain.Run, error) {
	var run domain.Run
	var llmResultJSON, outputsJSON []byte
	if err := s.Scan(&run.ID, &run.AssignmentID, &run.AgentID, &run.DutyID,
		&run.TriggerKind, &run.EventID, &run.RenderedSystemPrompt, &run.RenderedPrompt,
		&llmResultJSON, &outputsJSON, &run.Status, &run.Tokens, &run.Cost,
		&run.StartedAt, &run.FinishedAt, &run.Error); err != nil {
		return nil, fmt.Errorf("scan run: %w", err)
	}
	if len(llmResultJSON) > 0 {
		var r domain.LLMResult
		_ = json.Unmarshal(llmResultJSON, &r)
		run.LLMResult = &r
	}
	_ = json.Unmarshal(outputsJSON, &run.OutputsDelivered)
	return &run, nil
}
```

- [x] **Step 2: Compile check**

```bash
go build ./internal/repo/...
```
Expected: no errors

- [x] **Step 3: Commit**

```bash
git add internal/repo/runs.go
git commit -m "feat(repo): Run CRUD"
```

---

## Task 4: State store — Postgres implementation

**Files:**
- Create: `internal/state/postgres.go`
- Test: `internal/state/store_test.go`

- [x] **Step 1: Write `internal/state/postgres.go`**

```go
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store over a pgx pool.
type PostgresStore struct{ db *pgxpool.Pool }

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Get(ctx context.Context, assignmentID, key string) ([]byte, bool, error) {
	var val []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM assignment_state WHERE assignment_id=$1 AND key=$2`,
		assignmentID, key).Scan(&val)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return val, err == nil, err
}

func (s *PostgresStore) Set(ctx context.Context, assignmentID, key string, val []byte) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO assignment_state (assignment_id, key, value, updated_at)
		VALUES ($1,$2,$3,NOW())
		ON CONFLICT (assignment_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()`,
		assignmentID, key, val)
	return err
}

func (s *PostgresStore) Delete(ctx context.Context, assignmentID, key string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM assignment_state WHERE assignment_id=$1 AND key=$2`,
		assignmentID, key)
	return err
}

func (s *PostgresStore) AppendNote(ctx context.Context, assignmentID string, note any) error {
	noteJSON, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("marshal note: %w", err)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO assignment_notes (assignment_id, note) VALUES ($1,$2)`,
		assignmentID, noteJSON)
	return err
}

func (s *PostgresStore) HasProcessed(ctx context.Context, assignmentID, dedupKey string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT TRUE FROM assignment_processed WHERE assignment_id=$1 AND dedup_key=$2`,
		assignmentID, dedupKey).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return exists, err
}

func (s *PostgresStore) MarkProcessed(ctx context.Context, assignmentID, dedupKey string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO assignment_processed (assignment_id, dedup_key)
		VALUES ($1,$2) ON CONFLICT DO NOTHING`,
		assignmentID, dedupKey)
	return err
}
```

- [x] **Step 2: Write `internal/state/store_test.go`** (unit test with in-memory fake)

```go
package state_test

import (
	"context"
	"sync"
	"testing"

	"github.com/cbarraford/office-fleet/internal/state"
)

// MemStore is an in-memory Store for tests.
type MemStore struct {
	mu        sync.Mutex
	kv        map[string][]byte
	processed map[string]bool
}

func NewMemStore() *MemStore {
	return &MemStore{kv: map[string][]byte{}, processed: map[string]bool{}}
}

func (m *MemStore) Get(_ context.Context, assignmentID, key string) ([]byte, bool, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	v, ok := m.kv[assignmentID+":"+key]
	return v, ok, nil
}
func (m *MemStore) Set(_ context.Context, assignmentID, key string, val []byte) error {
	m.mu.Lock(); defer m.mu.Unlock()
	m.kv[assignmentID+":"+key] = val; return nil
}
func (m *MemStore) Delete(_ context.Context, assignmentID, key string) error {
	m.mu.Lock(); defer m.mu.Unlock()
	delete(m.kv, assignmentID+":"+key); return nil
}
func (m *MemStore) AppendNote(_ context.Context, _ string, _ any) error { return nil }
func (m *MemStore) HasProcessed(_ context.Context, assignmentID, dedupKey string) (bool, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	return m.processed[assignmentID+":"+dedupKey], nil
}
func (m *MemStore) MarkProcessed(_ context.Context, assignmentID, dedupKey string) error {
	m.mu.Lock(); defer m.mu.Unlock()
	m.processed[assignmentID+":"+dedupKey] = true; return nil
}

var _ state.Store = (*MemStore)(nil) // compile-time interface check

func TestMemStore_SetGet(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.Set(ctx, "a1", "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Get(ctx, "a1", "k")
	if err != nil || !ok || string(v) != "v" {
		t.Fatalf("got %q ok=%v err=%v", v, ok, err)
	}
}

func TestMemStore_HasProcessed(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	ok, _ := s.HasProcessed(ctx, "a1", "commit-abc")
	if ok {
		t.Fatal("should not be processed yet")
	}
	_ = s.MarkProcessed(ctx, "a1", "commit-abc")
	ok, _ = s.HasProcessed(ctx, "a1", "commit-abc")
	if !ok {
		t.Fatal("should be processed after MarkProcessed")
	}
}
```

- [x] **Step 3: Run tests**

```bash
go test ./internal/state/...
```
Expected: PASS

- [x] **Step 4: Commit**

```bash
git add internal/state/
git commit -m "feat(state): Postgres store + in-memory test double"
```

---

## Task 5: Prompt engine

**Files:**
- Create: `internal/prompt/engine.go`
- Test: `internal/prompt/engine_test.go`

- [x] **Step 1: Write `internal/prompt/engine.go`**

```go
package prompt

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"time"
)

// Context is the data available inside a prompt template.
type Context struct {
	Event      map[string]any
	Agent      map[string]any
	Duty       map[string]any
	Assignment map[string]any
	State      map[string]any
	Now        time.Time
}

// Render executes a Go text/template with the given context.
func Render(tmpl string, ctx Context) (string, error) {
	t, err := template.New("prompt").Funcs(helpers()).Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// ComposePrompts renders the three-layer prompt composition:
// system = agent.system_prompt
// task = task_prompt_override ?? duty.prompt + optional extra_instructions
func ComposePrompts(
	systemTemplate string,
	taskTemplate string,
	extraInstructions string,
	ctx Context,
) (system, task string, err error) {
	system, err = Render(systemTemplate, ctx)
	if err != nil {
		return "", "", fmt.Errorf("render system prompt: %w", err)
	}
	task, err = Render(taskTemplate, ctx)
	if err != nil {
		return "", "", fmt.Errorf("render task prompt: %w", err)
	}
	if extra := strings.TrimSpace(extraInstructions); extra != "" {
		renderedExtra, err := Render(extra, ctx)
		if err != nil {
			return "", "", fmt.Errorf("render extra instructions: %w", err)
		}
		task = task + "\n\n" + renderedExtra
	}
	return system, task, nil
}

func helpers() template.FuncMap {
	return template.FuncMap{
		"date": func() string { return time.Now().Format("2006-01-02") },
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"default": func(def, val string) string {
			if val == "" {
				return def
			}
			return val
		},
	}
}
```

- [x] **Step 2: Write `internal/prompt/engine_test.go`**

```go
package prompt_test

import (
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/prompt"
)

func ctx() prompt.Context {
	return prompt.Context{
		Event:      map[string]any{"mr_iid": "42", "title": "Fix the bug"},
		Agent:      map[string]any{"name": "dev-1", "role": "developer"},
		Duty:       map[string]any{"name": "mr-reviewer"},
		Assignment: map[string]any{"project": "myorg/myrepo"},
		State:      map[string]any{},
		Now:        time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC),
	}
}

func TestRender_Basic(t *testing.T) {
	out, err := prompt.Render("Review MR #{{.Event.mr_iid}} in {{.Assignment.project}}", ctx())
	if err != nil {
		t.Fatal(err)
	}
	if out != "Review MR #42 in myorg/myrepo" {
		t.Fatalf("unexpected: %q", out)
	}
}

func TestComposePrompts_ThreeLayers(t *testing.T) {
	sys := "You are a {{.Agent.role}} named {{.Agent.name}}."
	task := "Review MR #{{.Event.mr_iid}}."
	extra := "Focus on error handling."

	system, taskOut, err := prompt.ComposePrompts(sys, task, extra, ctx())
	if err != nil {
		t.Fatal(err)
	}
	if system != "You are a developer named dev-1." {
		t.Fatalf("system: %q", system)
	}
	if !strings.HasPrefix(taskOut, "Review MR #42.") {
		t.Fatalf("task: %q", taskOut)
	}
	if !strings.Contains(taskOut, "Focus on error handling.") {
		t.Fatalf("extra instructions missing: %q", taskOut)
	}
}

func TestComposePrompts_TaskPromptOverride(t *testing.T) {
	sys := "You are a {{.Agent.role}}."
	override := "Perform a security audit of MR #{{.Event.mr_iid}}."
	_, taskOut, err := prompt.ComposePrompts(sys, override, "", ctx())
	if err != nil {
		t.Fatal(err)
	}
	if taskOut != "Perform a security audit of MR #42." {
		t.Fatalf("override not applied: %q", taskOut)
	}
}

func TestRender_Truncate(t *testing.T) {
	out, err := prompt.Render(`{{truncate .Event.title 6}}`, ctx())
	if err != nil {
		t.Fatal(err)
	}
	if out != "Fix th..." {
		t.Fatalf("truncate: %q", out)
	}
}
```

- [x] **Step 3: Run tests**

```bash
go test ./internal/prompt/... -v
```
Expected: all PASS

- [x] **Step 4: Commit**

```bash
git add internal/prompt/
git commit -m "feat(prompt): text/template engine with three-layer composition"
```

---

## Task 6: GitLab plugin

**Files:**
- Create: `internal/plugins/gitlab/gitlab.go`
- Test: `internal/plugins/gitlab/gitlab_test.go`

- [x] **Step 1: Write `internal/plugins/gitlab/gitlab.go`**

```go
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

func init() {
	plugin.Register(&GitLabPlugin{})
}

// GitLabPlugin is the GitLab integration plugin.
type GitLabPlugin struct {
	token   string
	baseURL string
}

func (g *GitLabPlugin) Name() string { return "gitlab" }

func (g *GitLabPlugin) EventSources() []plugin.EventSource {
	// Declared but not wired in SP1 (no bus yet).
	return []plugin.EventSource{
		{Name: "mr_events", Description: "Merge request opened/updated events"},
	}
}

func (g *GitLabPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "post_mr_comment", Description: "Post a comment on a merge request"},
	}
}

func (g *GitLabPlugin) ConfigSchema() plugin.Schema {
	return plugin.Schema{
		"type": "object",
		"properties": map[string]any{
			"base_url": map[string]any{"type": "string"},
		},
	}
}

func (g *GitLabPlugin) Init(_ context.Context, cfg map[string]any, secrets plugin.SecretLookup) error {
	tok, err := secrets("gitlab_token")
	if err != nil {
		return fmt.Errorf("gitlab: resolve secret gitlab_token: %w", err)
	}
	g.token = tok
	if u, ok := cfg["base_url"].(string); ok && u != "" {
		g.baseURL = strings.TrimRight(u, "/")
	} else {
		g.baseURL = "https://gitlab.com"
	}
	return nil
}

// Do dispatches a named action.
func (g *GitLabPlugin) Do(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "post_mr_comment":
		return g.postMRComment(ctx, params)
	default:
		return nil, fmt.Errorf("gitlab: unknown action %q", action)
	}
}

func (g *GitLabPlugin) postMRComment(ctx context.Context, params map[string]any) (map[string]any, error) {
	project, _ := params["project"].(string)
	mrIID, _ := params["mr_iid"].(string)
	body, _ := params["body"].(string)

	if project == "" || mrIID == "" || body == "" {
		return nil, fmt.Errorf("gitlab post_mr_comment: project, mr_iid, and body are required")
	}

	// URL-encode the project path (replace / with %2F).
	encodedProject := strings.ReplaceAll(project, "/", "%2F")
	url := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/notes", g.baseURL, encodedProject, mrIID)

	payload, _ := json.Marshal(map[string]string{"body": body})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("gitlab: create request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: post comment: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gitlab: post comment returned %d: %s", resp.StatusCode, respBody)
	}

	var result map[string]any
	_ = json.Unmarshal(respBody, &result)
	return result, nil
}
```

- [x] **Step 2: Write `internal/plugins/gitlab/gitlab_test.go`**

```go
package gitlab_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/cbarraford/office-fleet/internal/plugins/gitlab"
	"github.com/cbarraford/office-fleet/internal/plugin"
)

func TestGitLabPlugin_PostMRComment(t *testing.T) {
	var gotBody, gotProject, gotMRIID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProject = r.URL.Path
		gotMRIID = r.URL.Path
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotBody = payload["body"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "body": gotBody})
	}))
	defer srv.Close()

	p, ok := plugin.Get("gitlab")
	if !ok {
		t.Fatal("gitlab plugin not registered")
	}

	secrets := func(name string) (string, error) { return "test-token", nil }
	if err := p.Init(context.Background(), map[string]any{"base_url": srv.URL}, secrets); err != nil {
		t.Fatal(err)
	}

	result, err := p.Do(context.Background(), "post_mr_comment", map[string]any{
		"project": "myorg/myrepo",
		"mr_iid":  "42",
		"body":    "LGTM",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != "LGTM" {
		t.Fatalf("body not sent: %q, project=%q", gotBody, gotProject)
	}
	if result["id"] == nil {
		t.Fatal("expected id in response")
	}
	_ = gotMRIID
}

func TestGitLabPlugin_UnknownAction(t *testing.T) {
	p, _ := plugin.Get("gitlab")
	secrets := func(name string) (string, error) { return "tok", nil }
	_ = p.Init(context.Background(), nil, secrets)
	_, err := p.Do(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}
```

- [x] **Step 3: Run tests**

```bash
go test ./internal/plugins/gitlab/... -v
```
Expected: PASS (uses httptest, no real GitLab)

- [x] **Step 4: Commit**

```bash
git add internal/plugins/gitlab/
git commit -m "feat(plugins/gitlab): post_mr_comment action + httptest suite"
```

---

## Task 7: Executor — Claude CLI backend + Fake

**Files:**
- Create: `internal/executor/claude.go`
- Create: `internal/executor/fake.go`
- Test: `internal/executor/executor_test.go`

- [x] **Step 1: Write `internal/executor/fake.go`**

```go
package executor

import (
	"context"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// FakeExecutor returns a canned LLMResult for tests.
// It records the last request it received for assertion.
type FakeExecutor struct {
	Result  domain.LLMResult
	LastReq LLMRequest
	Err     error
}

func (f *FakeExecutor) Kind() string { return "fake" }

func (f *FakeExecutor) Run(_ context.Context, req LLMRequest) (domain.LLMResult, error) {
	f.LastReq = req
	return f.Result, f.Err
}
```

- [x] **Step 2: Write `internal/executor/claude.go`**

```go
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// ClaudeExecutor shells out to the `claude` CLI agentic backend.
// The CLI handles its own tool-use loop; OfficeFleet just provides the prompt and reads the output.
type ClaudeExecutor struct {
	// APIKey, if non-empty, is passed via ANTHROPIC_API_KEY env var.
	APIKey string
}

func NewClaudeExecutor(apiKey string) *ClaudeExecutor {
	return &ClaudeExecutor{APIKey: apiKey}
}

func (c *ClaudeExecutor) Kind() string { return "claude" }

// Run invokes the claude CLI with the system + task prompts, captures JSON output.
func (c *ClaudeExecutor) Run(ctx context.Context, req LLMRequest) (domain.LLMResult, error) {
	// Build a combined prompt: system prompt first, then the task.
	combinedPrompt := buildClaudePrompt(req)

	// Write the prompt to a temp file so we can pipe it via --input-file.
	promptFile := filepath.Join(req.Workspace, "fleet_prompt.txt")
	if err := os.WriteFile(promptFile, []byte(combinedPrompt), 0600); err != nil {
		return domain.LLMResult{}, fmt.Errorf("write prompt file: %w", err)
	}

	args := []string{
		"--print",
		"--output-format", "json",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	// Pipe the combined prompt via stdin.
	args = append(args, "--print")

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = req.Workspace
	cmd.Stdin = strings.NewReader(combinedPrompt)

	// Ensure required tools are on PATH (verify presence, not install).
	if err := verifyTools(req.Tools); err != nil {
		return domain.LLMResult{}, err
	}

	env := os.Environ()
	if c.APIKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+c.APIKey)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		return domain.LLMResult{Status: 1, Summary: errMsg},
			fmt.Errorf("claude CLI: %w\nstderr: %s", err, errMsg)
	}

	return parseClaudeOutput(stdout.Bytes())
}

// buildClaudePrompt constructs the input for the claude CLI.
// System prompt is prepended; task prompt follows.
func buildClaudePrompt(req LLMRequest) string {
	var sb strings.Builder
	if req.SystemPrompt != "" {
		sb.WriteString("<system>\n")
		sb.WriteString(req.SystemPrompt)
		sb.WriteString("\n</system>\n\n")
	}
	sb.WriteString(req.Prompt)
	return sb.String()
}

// parseClaudeOutput extracts an LLMResult from claude CLI JSON output.
// The CLI outputs a JSON object with at minimum a "result" field.
func parseClaudeOutput(data []byte) (domain.LLMResult, error) {
	// claude --output-format json returns a stream of JSON objects; last one has the result.
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	var last []byte
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) > 0 {
			last = lines[i]
			break
		}
	}
	if len(last) == 0 {
		return domain.LLMResult{}, fmt.Errorf("claude: empty output")
	}

	var raw map[string]any
	if err := json.Unmarshal(last, &raw); err != nil {
		// Not JSON — treat as plain text summary.
		return domain.LLMResult{
			Status:     0,
			Summary:    string(data),
			Output:     map[string]any{"raw": string(data)},
			Transcript: string(data),
		}, nil
	}

	result := domain.LLMResult{Output: map[string]any{}}
	if v, ok := raw["result"].(string); ok {
		result.Summary = v
		result.Output["raw"] = v
	}
	if v, ok := raw["cost_usd"].(float64); ok {
		result.Cost = v
	}
	if v, ok := raw["usage"].(map[string]any); ok {
		if tok, ok := v["output_tokens"].(float64); ok {
			result.Tokens = int(tok)
		}
	}
	result.Transcript = string(data)
	return result, nil
}

// verifyTools checks that each required CLI tool is present on PATH.
func verifyTools(tools []string) error {
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("required tool %q not found on PATH: %w", tool, err)
		}
	}
	return nil
}
```

- [x] **Step 3: Write `internal/executor/executor_test.go`**

```go
package executor_test

import (
	"context"
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
)

func TestFakeExecutor_RecordsRequest(t *testing.T) {
	fake := &executor.FakeExecutor{
		Result: domain.LLMResult{
			Status:  0,
			Summary: "LGTM: no issues found",
			Output:  map[string]any{"approved": true},
			Tokens:  100,
			Cost:    0.001,
		},
	}

	req := executor.LLMRequest{
		SystemPrompt: "You are a developer.",
		Prompt:       "Review MR #42.",
		Workspace:    "/tmp",
		Tools:        []string{"glab"},
		Model:        "claude-opus-4-5",
		Effort:       "high",
	}

	result, err := fake.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "LGTM: no issues found" {
		t.Fatalf("unexpected summary: %q", result.Summary)
	}
	if fake.LastReq.Prompt != req.Prompt {
		t.Fatalf("request not recorded: %+v", fake.LastReq)
	}
}

func TestFakeExecutor_Kind(t *testing.T) {
	fake := &executor.FakeExecutor{}
	if fake.Kind() != "fake" {
		t.Fatalf("unexpected kind: %q", fake.Kind())
	}
}
```

- [x] **Step 4: Run tests**

```bash
go test ./internal/executor/... -v
```
Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/executor/
git commit -m "feat(executor): claude CLI backend + fake executor for tests"
```

---

## Task 8: Output delivery

**Files:**
- Create: `internal/outputs/deliver.go`
- Test: `internal/outputs/deliver_test.go`

- [x] **Step 1: Write `internal/outputs/deliver.go`**

```go
package outputs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/prompt"
)

// Deliver executes each configured output binding by:
// 1. Rendering the params template against LLMResult + the prompt context.
// 2. Calling the corresponding plugin action.
// Returns one OutputDelivery record per binding (never aborts early).
func Deliver(
	ctx context.Context,
	outputs []domain.OutputBinding,
	result domain.LLMResult,
	promptCtx prompt.Context,
) []domain.OutputDelivery {
	deliveries := make([]domain.OutputDelivery, 0, len(outputs))

	for _, out := range outputs {
		d := domain.OutputDelivery{
			Plugin: out.Plugin,
			Action: out.Action,
		}

		// Render each param value as a template.
		rendered, err := renderParams(out.Params, result, promptCtx)
		if err != nil {
			d.Status = "failed"
			d.Error = fmt.Sprintf("render params: %v", err)
			deliveries = append(deliveries, d)
			continue
		}
		d.Params = rendered

		p, ok := plugin.Get(out.Plugin)
		if !ok {
			d.Status = "failed"
			d.Error = fmt.Sprintf("plugin %q not registered", out.Plugin)
			deliveries = append(deliveries, d)
			continue
		}

		_, err = p.Do(ctx, out.Action, rendered)
		if err != nil {
			d.Status = "failed"
			d.Error = err.Error()
		} else {
			d.Status = "delivered"
		}
		deliveries = append(deliveries, d)
	}

	return deliveries
}

// renderParams resolves each param value: if it's a string, treat it as a template.
func renderParams(params map[string]any, result domain.LLMResult, promptCtx prompt.Context) (map[string]any, error) {
	// Merge LLMResult.Output into prompt context for param templates.
	enriched := promptCtx
	if enriched.Event == nil {
		enriched.Event = map[string]any{}
	}
	// Make LLM output available as .LLMOutput in templates.
	enrichedEvent := make(map[string]any, len(promptCtx.Event)+1)
	for k, v := range promptCtx.Event {
		enrichedEvent[k] = v
	}
	enriched.Event = enrichedEvent

	out := make(map[string]any, len(params))
	for k, v := range params {
		str, ok := v.(string)
		if !ok {
			out[k] = v
			continue
		}
		rendered, err := prompt.Render(str, enriched)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", k, err)
		}
		out[k] = rendered
	}
	return out, nil
}

// RenderLLMBody renders a template against the LLMResult's output fields.
// Convenience for output templates that reference {{.LLMSummary}} etc.
func RenderLLMBody(tmpl string, result domain.LLMResult) (string, error) {
	ctx := prompt.Context{
		Event: map[string]any{
			"llm_summary":    result.Summary,
			"llm_transcript": result.Transcript,
			"llm_output":     result.Output,
		},
	}
	return prompt.Render(tmpl, ctx)
}

// JSON helper for serializing LLM output into param templates.
func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
