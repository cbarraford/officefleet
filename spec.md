# OfficeFleet — Design Spec

**Status:** Draft for review
**Date:** 2026-06-07
**Author:** brainstormed with Claude Code
**Lineage:** Generalization of the `huginn-agent` codebase — the hardcoded GitLab-developer bot generalized into a platform that runs a managed *fleet* of autonomous agentic employees (your AI "office").

---

## 1. Summary

OfficeFleet is a self-hosted platform for running **agentic employees**. Instead of a human
prompting an LLM, OfficeFleet **receives I/O events** from integrations (email, Slack, Discord,
GitLab, GitHub, generic webhooks) and routes them to **Agents** — configured employees with a
role, a system prompt, and a default LLM — which carry out **Duties** (reusable units of work
that subscribe to events or run on a schedule, execute a tool-using LLM, and **deliver outputs**
like posting a comment, sending an email, or pinging Discord). Operators configure *employees and
duties*, not prompts.

Huginn is the proof of concept for a single hardcoded case: one role (developer), one platform
(GitLab), one workflow. OfficeFleet generalizes that into a configurable, plugin-driven platform
where the GitLab-developer workflow is just one Agent performing a few sample Duties.

This document is the **architecture/vision spec for the whole platform** plus an
**implementable, detailed spec for Sub-Project 1 (SP1), the core-engine vertical slice**.
Later sub-projects (SP2–SP5) are summarized here and will each get their own spec → plan →
implementation cycle.

---

## 2. Goals & non-goals

### Goals
- A clean **integration plugin framework** (one plugin per service, exposing event sources
  *and* output actions) that is easy to extend.
- **Agents** as configurable employees: a persona (role + system prompt + default LLM) that is
  assigned **Duties**.
- **Duties** as reusable units of work: trigger + prompt template + required tools + output
  actions, assignable to many Agents.
- **Promptless operation**: the system reacts to I/O events and schedules, not human prompts.
- **Hybrid execution**: the tool-using LLM does the real work; the platform delivers outputs
  declaratively (governed, auditable, previewable).
- **Per-assignment private state** for dedup, memory, and continuity.
- A **prompt templating system** so prompts are composed from event/agent/duty/state, not hand-typed.
- A **web operator UI**: live dashboard, Agent management, Duty library, per-agent detail pages,
  integrations & settings, with built-in users (Admin/Viewer) and DB-encrypted secrets.

### Non-goals (v1)
- Multi-tenant SaaS / hard tenant isolation (target is **multi-user, single org**).
- Untrusted third-party plugins (v1 plugins are **first-party, compiled in**).
- Real per-unit Postgres roles/credentials (isolation is **logical**, one app DB).
- External secret managers and SSO/OIDC (designed *behind interfaces*, implemented later).
- Per-unit containers / remote executors (shared runtime in v1, executor is an interface so this
  can be added later).

---

## 3. Glossary

- **Agent** — a configured employee: a persona with a `name`, `role`, `system_prompt`, and a
  `default LLM`. An Agent is assigned Duties and carries them out. *(Distinct from the agentic
  LLM run that executes a duty — that execution is a **Run** produced by a backend/executor. To
  avoid the overload, the LLM-invocation types are named `LLM*`, not `Agent*`.)*
- **Duty** — a **reusable definition** of work: the trigger kind(s) it supports, prompt template,
  required tools, output action types, and a `role` **category tag**. The "what." One Duty can be
  assigned to many Agents.
- **Assignment** — binds an Agent to a Duty with **per-agent config**: trigger config (schedule,
  subscription filter, project), output routing, optional overrides, enabled flag. The runtime
  unit that actually fires. *(In the UI this is presented simply as "this agent's duties" — operators
  don't reason about a third noun.)*
- **Role (two meanings)** — `Agent.role` is the **operative persona** (developer/marketer/lawyer)
  that, with the system prompt, defines who the agent *is*. `Duty.role` is a **category tag**
  describing which role a duty suits, used to suggest/filter compatible duties. At runtime the
  persona is always `Agent.role`; `Duty.role` never overrides it.
- **Integration plugin** — a first-party, compiled-in module for one external service. Declares
  **event sources** (inputs) and **actions** (outputs), plus its own auth/config.
- **Trigger** — what causes an Assignment to run. Pluggable kinds: `manual`, `cron`,
  `event-subscription`, `continuous`.
- **Event envelope** — the normalized struct representing an inbound event.
- **Run** — one execution of an Assignment; the audit + metrics record.
- **Backend** — a **named, configured LLM provider instance** defined in `fleet.yaml` (+ secrets):
  base URI/model/default effort/custom params + an **auth mode**. Two kinds: **CLI agentic backends**
  (claude/codex/gemini CLIs, which run their own tool loop) and **endpoint backends**
  (Ollama / OpenAI-compatible HTTP, driven by OfficeFleet's generic agent loop — §11/§13). Each
  authenticates by **subscription** (vendor OAuth / persisted CLI session) or **API key** (token,
  pay-per-use; `none` for keyless local). LLM providers are modeled as backends, **not** as
  integration-style plugins.
- **BackendRef** — a selector that references a Backend **by name** with optional `model`/`effort`
  overrides (used by `Agent.default_backend`, `Duty.backend`, `Assignment.backend`).
- **Executor** — the interface that performs a Run given a resolved Backend (`LLMRequest → LLMResult`).
- **Output action** — a configured call to a plugin action that delivers a result.
- **State store** — a private, logically-isolated **per-assignment** KV + structured store.

---

## 4. Key decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Relationship to huginn | **Greenfield, reuse components/concepts** |
| Stack | **Go core + TypeScript/React UI**, PostgreSQL |
| Deployment model | **Multi-user, single org** |
| Name | **OfficeFleet** (working; CLI `fleet`) |
| Plugin loading | **Compiled-in registry (first-party)**, clean interface for out-of-process later |
| DB isolation | **Logical isolation in one app DB** (no per-unit DB creds) |
| Domain model | **Two-level: Agent (persona) ↔ Duty (reusable), many-to-many via Assignment** |
| Role | **On both**: `Agent.role` = operative persona; `Duty.role` = category tag (never overrides) |
| Per-agent duty config | **Assignment** holds trigger/outputs/config-values + backend override + prompt customization (extra instructions, optional task-prompt override); structural fields stay on the Duty |
| Prompt composition | **Three layers**: Agent system prompt → Duty task prompt (overridable) → assignment extra instructions |
| Plugin shape | **Unified integration plugin** (inputs + outputs in one plugin) |
| Execution | **Hybrid**: LLM works with tools; **platform delivers outputs** |
| LLM backends | **Named, configured backends** (not a plugin kind): built-in CLI agentic set (claude/codex/gemini) + a generic **endpoint backend** (Ollama / OpenAI-compatible) via base_uri/model/api-key/default_effort/custom params |
| Execution paths | **Two under one Executor interface**: CLI backends delegate the tool loop; endpoint backends use OfficeFleet's **generic agent loop** (its own sub-project — §13) |
| LLM auth | Per-backend **subscription** (vendor OAuth / persisted CLI session) **or** API key (token / `none`); same `kind` can run both ways as separate named backends |
| State | **Private persistent state store per assignment** |
| Triggers | Pluggable; **SP1 = manual + cron**, event-subscription is SP3's headline |
| Auth | **Built-in users + Admin/Viewer roles**, interface for SSO later |
| Secrets | **Encrypted at rest in the app DB** (master key from env/KMS) |
| Runtime architecture | **Modular monolith, vertical-slice first** |

---

## 5. Architecture overview

A single Go binary, one PostgreSQL database, and a TypeScript/React SPA served by the Go API.
Internal packages have clean boundaries so a plugin or the executor *could* move out-of-process
later without reshaping the domain.

```
                    ┌──────────────────────────────────────────────────┐
   inbound events   │                  OfficeFleet (Go)                 │
  (webhook / poll)  │                                                  │
  ───────────────►  │  Integration plugins ──► Event bus ──► Dispatcher │
                    │       ▲  │                                  │      │
                    │  auth/│  │ actions          match event to  ▼      │
   schedules ─────► │  cfg  │  │                  Assignments  (Agent ×  │
   (cron)           │       │  │                   Duty)          │      │
   manual/UI ─────► │       │  ▼                                  ▼      │
                    │   ┌───┴──────────┐   render system+task   Run     │
                    │   │  Plugins reg. │◄───────  prompt    pipeline    │
                    │   └──────────────┘   run LLM (executor)   │        │
                    │                       deliver outputs      │       │
                    │   State store ◄────────────────────────────┘       │
                    │   Runs / metrics ─────────────► SSE ──► Web UI/API │
                    └──────────────────────────────────────────────────┘
                                  │
                            PostgreSQL (one DB: agents, duties, assignments, runs, …)
```

**Reused concepts from huginn** (re-implemented in Go): the `ai_backends` abstraction
(Claude/Codex/Gemini + factory + voter) → OfficeFleet's **executor**; the `db` migrations/connection
pattern; the notifications event→manager→sink pattern → OfficeFleet's **output actions**; the
`web` SSE live-stream pattern; the GitLab domain knowledge → OfficeFleet's **GitLab plugin**.

---

## 6. Core domain model

The crux of the many-to-many model is **where each piece of config lives**. The boundary:
Duty = the reusable "what"; Agent = the persona; Assignment = the "where/when/how, for this agent."

### Agent (persona)
```
Agent {
  id              uuid
  name            string        // display name (first name or handle, e.g. "Alex")
  role            string        // OPERATIVE persona: developer | marketer | lawyer | ...
  system_prompt   text          // persona instructions; becomes the run's system prompt
  default_backend BackendRef     // references a named backend (§11) + optional model/effort overrides
  avatar_url      text?         // URL to stored avatar image (generated or uploaded; see §6.1)
  hired_at        date?         // "hire date" shown in the UI for flavour/tenure tracking
  enabled         bool          // false = paused: no new runs fire; in-flight runs complete normally
  created_at, updated_at
}
```

**Pause/resume semantics:** toggling `Agent.enabled` is a **soft pause** — it does not cancel
in-flight runs (those complete normally), but it prevents any new runs from being started for any
of the agent's assignments, regardless of trigger kind (manual, cron, or event-subscription).
The run pipeline checks the agent's `enabled` flag at run-start and records the run as `skipped`
(with reason `agent_paused`) if it is false, so the skip is auditable. A paused agent is visually
distinguished in the UI (greyed card, "Paused" badge) and excluded from the live activity
dashboard's "active agents" count. Individual assignments also have their own `enabled` flag for
finer control (pausing one duty without pausing the whole agent).

### Agent statistics (derived, not stored)
Per-agent stats are computed from the `runs` table on demand (or materialised via a periodic job for the dashboard). They are **read-only views** over run data — not columns on `agents` — so they are always current and require no separate update path.

```
AgentStats {
  agent_id
  // Activity
  total_runs          int       // all-time runs for this agent's assignments
  runs_last_30d       int
  // Outcomes
  success_rate        float     // succeeded / (succeeded + failed), last 30d
  skip_rate           float     // skipped / total, last 30d
  // Cost & volume (contribution signals)
  total_tokens        int       // cumulative tokens across all runs
  total_cost_usd      float     // cumulative LLM spend
  tokens_last_30d     int
  cost_last_30d_usd   float
  // Output delivery
  outputs_delivered   int       // total successful output actions (comments posted, emails sent, …)
  outputs_last_30d    int
  // Timing
  avg_run_duration_s  float     // average wall-clock time per run
  last_run_at         timestamp?
}
```

These stats surface on the per-agent detail page (§12 surface 4) and as compact "employee card" metrics on the Agents list view.

### Duty (reusable definition — the "what")
```
Duty {
  id              uuid
  name            string
  role            string         // CATEGORY tag: which role this duty suits (suggest/filter only)
  description     text
  trigger_kinds   []string       // supported kinds: manual | cron | event-subscription | continuous
  prompt          PromptTemplate // the task template (see §9)
  required_tools  []string       // CLIs that must be on PATH (verified at assign/run)
  output_actions  []OutputActionType  // action types it emits: { plugin, action, params_template }
  config_schema   json           // declares the per-assignment config it expects
  backend         BackendRef?     // optional default override
  created_at, updated_at
}
```

### Assignment (Agent ↔ Duty binding — the "where/when/how, for this agent")
```
Assignment {
  id                   uuid
  agent_id             uuid -> Agent
  duty_id              uuid -> Duty
  enabled              bool
  trigger              Trigger        // chosen kind + config (cron schedule / subscription filter / project)
  outputs              []OutputBinding // per-agent routing (channel, project, recipients)
  config               json           // values satisfying duty.config_schema
  backend              BackendRef?     // optional override (highest precedence)
  task_prompt_override text?          // optional: replaces Duty.prompt for THIS agent
  extra_instructions   text?          // optional: appended after the task prompt for THIS agent
  created_at, updated_at
}
```

The Assignment is where an operator **configures a duty for a specific agent**: its trigger,
outputs, declared config values, backend override, and prompt tailoring. **Structural fields stay
on the shared Duty** (required tools, output action types, config schema, supported trigger kinds)
so the library definition can't drift per agent.

**Backend resolution (precedence):** `Assignment.backend ?? Duty.backend ?? Agent.default_backend`
selects a **named backend** (§11). Effective `model`/`effort` = the `BackendRef` override **??** the
backend's own `model`/`default_effort`.
**Persona at runtime:** always `Agent.role` + `Agent.system_prompt`.
**Prompt composition (three layers, all rendered with the §9 context and recorded on the Run):**
1. **System** = `Agent.system_prompt` — who the agent is, across all its duties.
2. **Task** = `Assignment.task_prompt_override ?? Duty.prompt` — the duty's work, optionally replaced for this agent.
3. **Add-on** = `Assignment.extra_instructions` (if set) appended after the task — per-agent nuance without forking the Duty.

> *Many-to-many stays light because event-driven specifics (which MR, which project) arrive in the
> event, not in static config — so an Assignment is usually thin (trigger config + routing + enabled).*

### Integration plugin (interface, compiled-in)
A plugin owns one service's auth/config and declares two capability sets:
- **Event sources** — named inputs it can emit (via webhook and/or poll).
- **Actions** — named outputs it can perform.

### Run
```
Run {
  id
  assignment_id -> Assignment    // denormalized agent_id + duty_id for queries
  agent_id, duty_id
  trigger_kind, event_id?         // what caused it
  rendered_system_prompt          // exact system prompt (from Agent)
  rendered_prompt                 // exact task prompt (from Duty template)
  llm_result                      // structured result + transcript ref
  outputs_delivered  []           // each action + status
  status             enum         // queued|running|succeeded|failed|skipped
  tokens, cost, started_at, finished_at, error?
}
```

### State store (per assignment)
Logically isolated within the one app DB; keyed by `assignment_id` (**not** `duty_id` — two agents
running the same duty on different projects need independent dedup). Provides:
- KV get/set/delete (dedup keys, cursors, small memory).
- Structured records (append-only notes / memory rows).
- Helpers for the common "have I already processed X?" dedup pattern.

---

### 6.1 Agent avatar generation

Agents are given a visual identity — a photorealistic (or illustrated) headshot — generated by a
text-to-image model at creation time and stored as a static asset served by the binary.

**Generation flow:**
1. When an agent is created (or when the operator clicks "Regenerate avatar"), the platform
   constructs a prompt from `agent.role` and optionally `agent.name` (e.g. *"Professional headshot
   of a software developer named Alex, neutral background, corporate style"*).
2. The image is generated via a configured **image backend** — the same named-backend pattern as
   LLM backends: an operator configures a `kind: openai-image` (DALL·E), `kind: stability`,
   `kind: flux`, or any OpenAI-compatible image endpoint in `fleet.yaml`. A `none` mode is
   supported so the platform falls back to a deterministic avatar (e.g. initials on a coloured
   background) when no image backend is configured.
3. The generated image is stored internally (e.g. `assets/avatars/<agent_id>.png`) and served at
   a stable URL; `Agent.avatar_url` records that path. Operators can also upload a custom image
   to override the generated one.

**Design notes:**
- Avatar generation is **non-blocking** — agent creation succeeds immediately; the image is
  generated asynchronously and the UI polls/SSE-updates when it arrives.
- If generation fails, the fallback initials avatar is shown; the operator can retry.
- The prompt template for generation is configurable in settings so operators can tune the style.

---

## 7. Plugin framework

- Plugins implement a Go interface and **self-register** in a registry at init.
- A plugin declares: `Name()`, `EventSources()`, `Actions()`, `ConfigSchema()`, and an
  `Init(config, secrets)` lifecycle.
- **Event sources** expose two optional surfaces behind one interface:
  - **Push**: the plugin provides an HTTP handler OfficeFleet mounts at a stable path (for webhooks).
  - **Poll**: the plugin implements `Poll(ctx, cursor) ([]Event, newCursor)` OfficeFleet calls on an interval.
- **Actions** are invoked by the platform during output delivery: `Do(ctx, action, params) (result, error)`.
- Auth/config per plugin is stored encrypted (see §12) and injected at `Init`.

The interface is intentionally process-local in v1 but shaped (serializable params/results,
explicit config/secret injection) so an out-of-process gRPC transport can wrap it later.

There is **one plugin kind** in this framework: the integration plugin (event sources + actions).
**LLM providers are deliberately not plugins** — they are named, configured *backends* (§11), kept
as a fixed built-in set plus one generic endpoint backend rather than a full plugin SDK.

---

## 8. Triggers

Triggers are a pluggable interface. A **Duty** declares which trigger kinds it supports; an
**Assignment** picks one and supplies its config.

- **`manual`** — fired on-demand (CLI or UI). Carries optional operator-supplied params.
- **`cron`** — fired on a schedule (cron expression / interval).
- **`event-subscription`** — fired when a matching event arrives on the bus (see §10). *SP3.*
- **`continuous`** — a supervised long-running loop with restart logic. *Later.*

**SP1 implements `manual` + `cron`.** These prove the entire pipeline
(render → LLM-with-tools → platform-delivers-output → record) without requiring the event bus.
`event-subscription` is SP3's headline because it pulls in the full bus + dispatcher.

---

## 9. Prompt templating system

- Engine: Go `text/template` with a **registered helper/function library**.
- **Context available to a template:**
  - `event` — the event envelope (raw + normalized payload). For manual triggers, operator params.
  - `agent` — the executing agent (name, role, system prompt).
  - `duty` — the duty definition (name, role category, description).
  - `assignment` — the per-agent config (the `config` object + routing).
  - `state` — the assignment's private state/memory (read access).
  - `secret "ref"` — explicit, audited secret access by reference (never blindly inlined).
  - helpers — `date`, `json`, `truncate`, and `fetch <plugin> <action> <params>` for enrichment.
- **Three-layer composition** (see §6): the **system prompt** is `Agent.system_prompt`; the **task
  prompt** is `Assignment.task_prompt_override ?? Duty.prompt`; and `Assignment.extra_instructions`,
  if set, is appended after the task. Every layer is a template rendered with the context above, and
  the final system prompt + final task prompt are both recorded on the Run.
- Templates are **versioned** and **previewable** in the UI by rendering against a sample/last
  event before saving.

---

## 10. Event flow & the bus (architectural treatment; implemented in SP3)

The core of "promptless." Specified here so SP1's interfaces don't paint us into a corner.

- **Event envelope** (normalized):
  ```
  Event {
    id, source_plugin, event_type
    payload_raw   json     // verbatim from the source
    payload_norm  json     // plugin-normalized, template-friendly shape
    identity      string   // who/what triggered (author, sender, ...)
    dedup_key     string   // stable key for "already processed"
    received_at
  }
  ```
- **Ingestion**: push (plugin webhook handler) or poll (interval) → event written to a Postgres
  `events` table (durability + replay) and placed on an in-process bus (buffered channel/queue).
- **Dispatcher**: matches each event against **Assignments** whose `event-subscription` trigger
  matches `(source, event_type)` + optional filter; checks the per-assignment `dedup_key` against
  state; enqueues a Run. A bounded **worker pool** caps concurrency.
- **Replay/observability**: because events are persisted, the dispatcher can be re-run and the
  UI can show the raw event behind any Run.

---

## 11. Execution (hybrid)

### Named, configured backends
LLM providers are **named backends** declared in `fleet.yaml` (secrets resolved from the encrypted
store). This is the home for the operator's "default effort levels and custom data/URI for custom or
open-source LLMs":

```yaml
backends:
  - name: claude-sub              # CLI agentic backend, SUBSCRIPTION auth
    kind: claude
    auth: { mode: subscription }  # vendor OAuth / persisted CLI session (e.g. Claude plan)
    default_effort: high
  - name: claude-api              # same CLI, API-KEY auth (pay-per-use)
    kind: claude
    auth: { mode: api_key, api_key: ${secret:anthropic_key} }
    default_effort: high
  - name: local-ollama            # endpoint backend (custom / OSS)
    kind: openai-compatible
    base_uri: http://localhost:11434/v1
    model: llama3.1:70b
    auth: { mode: api_key, api_key: ${secret:ollama_key} }   # or { mode: none } for local
    default_effort: medium
    params: { num_ctx: 8192 }     # custom data passed through to the provider
```

### Authentication: subscription or API key
Every backend chooses an **auth mode**:
- **`subscription`** — the vendor CLI is authenticated once via OAuth login against a paid plan
  (Claude / Codex / Gemini subscriptions). Credentials persist on disk (mirroring huginn's
  `~/.claude`, `~/.codex`, `~/.gemini`). `fleet backends login <name>` runs the interactive flow
  (or device-auth for headless hosts). Applies to **CLI agentic backends**.
- **`api_key`** — a token (a `${secret:…}` reference; pay-per-use). Works for CLI backends (vendor
  API key) **and** endpoint backends. `{ mode: none }` covers keyless local endpoints (e.g. Ollama).

Because backends are **named instances**, the same `kind` can appear under both modes (e.g.
`claude-sub` and `claude-api`), so different agents/duties can draw on subscription vs. metered
billing. The platform validates that a backend's required credential (login session or key) is
present at config-validate and at run start.

A `BackendRef` (`Agent.default_backend`, `Duty.backend`, `Assignment.backend`) selects a backend by
name; effective `model`/`effort` = the ref's override **??** the backend's `model`/`default_effort`.

### Two execution paths under one Executor interface
This is a feature, not a compromise — minimal provider machinery, maximal execution capability:

- **CLI agentic backends** (`claude` / `codex` / `gemini`): OfficeFleet shells out to the maintained
  agent CLI, which runs the tool-use loop itself. These are the built-in fixed set (ports huginn's
  `ai_backends` concept to Go) and the path SP1 uses.
- **Endpoint backends** (`openai-compatible` / Ollama / custom HTTP): a raw chat/completions API with
  no built-in agent loop, so **OfficeFleet's own generic agent loop** drives tool use (its own
  sub-project — SP2, §13).

Both implement `Executor.Run(LLMRequest) → LLMResult`. The run flow is the same: each Run gets an
**isolated workspace** and the declared `required_tools`; the **LLM does the real work** and returns
a **structured result** (JSON contract: summary, artifacts, proposed outputs/parameters, status
code); the **platform** then executes the configured **outputs** in order via plugin actions —
declarative, governed, auditable, previewable — with output params rendered from templates against
the LLM result.

> **Key open design question for the generic agent loop (SP2):** CLI backends hand the model a whole
> computer (files, bash, `glab`), so `LLMRequest.Tools` as "CLI names on PATH" is enough for them. The
> generic loop must instead **broker each tool itself**, so it needs a richer tool abstraction (tool
> spec + an execution bridge to declared CLIs/plugin actions + iteration & safety limits + transcript
> capture). The current Executor interface does **not** yet support endpoint tool-use; that's SP2's job.

> **Open seam (decided per sample duty, not now):** output-heavy work like posting *inline* review
> comments may need either a richer structured-output schema (platform posts each comment) or a
> sanctioned plugin tool the LLM calls directly. Noted; resolved when SP5 ports code-review.

---

## 12. Web UI, auth & secrets (architectural treatment; implemented in SP4)

- **API**: Go HTTP API; **UI**: React/TS SPA served by the binary; live data via SSE/websocket.
- **Surfaces:**
  1. **Live activity dashboard** — running now, recent runs, throughput, success/fail, token/cost.
  2. **Agents management** — create/edit/delete agents; set name, role, system prompt, default LLM,
     hire date, and avatar; **assign duties** (pick from the Duty library and configure each for this
     agent: trigger, outputs, config values, backend override, and per-agent prompt tweaks — extra
     instructions or a task-prompt override). "Assignment" stays invisible — the screen reads as
     *"this agent's duties."* The agents list renders as an **employee directory**: avatar, name,
     role, hire date, enabled/paused status badge, and a compact stats strip (runs last 30d,
     success rate, outputs delivered). A **pause/resume toggle** is available inline on each card
     and on the detail page (Admin only); paused agents display a greyed card with a "Paused" badge
     and are excluded from the live dashboard's active-agents count.
  3. **Duty library** — browse, define, and edit reusable Duties (trigger kinds, prompt template,
     required tools, output action types, role category).
  4. **Per-agent detail pages** — the agent's avatar/name/role/hire date, full stats panel (see
     §6.1), duties, run history (event→system+task prompt→result→outputs), and per-assignment
     state/memory.
  5. **Integrations & settings** — connect plugins; manage **LLM backends** (define backends, run the
     subscription login flow or enter API keys, see auth status); manage encrypted secrets; manage users.
- **Auth**: built-in user accounts, session-based, **Admin** (full control) and **Viewer**
  (read-only) roles, behind an interface so SSO/OIDC can be added later.
- **Secrets**: entered via UI, **encrypted at rest in the DB** (master key from env/KMS), injected
  into plugins at `Init`; templates access them only via the explicit, audited `secret` helper.

---

## 13. Decomposition & build order

The platform is too large for one detailed spec. Build order:

- **SP1 — Core engine (vertical slice).** *Detailed below (§14).* Go skeleton, config, Postgres +
  migrations, domain model (**Agent, Duty, Assignment, Run**), plugin interface + registry, executor
  interface + **one CLI agentic backend (claude)** with named-backend config, trigger interface
  (manual + cron), the run pipeline, per-assignment state, run recording, **one GitLab plugin + one
  Agent with one assigned Duty** runnable end-to-end via CLI. No web UI; no Duty-library/catalog UX
  (that's SP4); no generic agent loop / endpoint backends (that's SP2).
- **SP2 — LLM backends & the generic agent loop.** *The largest single workstream.* Endpoint
  backends (Ollama / OpenAI-compatible) configured by base_uri/model/api-key/default_effort/custom
  params, **plus OfficeFleet's own tool-using agent loop** so non-CLI / open-source models can perform
  tool duties: a tool-call protocol, iteration control, a **tool-execution bridge** (brokering declared
  CLIs and plugin actions as model tools), safety limits (max iterations / timeouts), and transcript
  capture. Also the optional multi-model voter. Resolves the §11 tool-abstraction open question.
- **SP3 — Event bus & plugin breadth.** Event envelope, `events` table, bus + dispatcher,
  `event-subscription` + `continuous` triggers, more integration plugins (Slack, Discord, GitHub, Email).
- **SP4 — Web UI & operators.** API + SPA (all surfaces: dashboard, Agents employee-directory,
  Duty library, per-agent pages, integrations/settings), auth + Admin/Viewer roles, encrypted
  secrets, live dashboard via SSE; **agent personas** (name, hire date, avatar generation via
  configured image backend — §6.1); **agent stats panel** (runs, success rate, outputs delivered,
  token/cost contribution — §6).
- **SP5 — Sample Duties / huginn parity.** Port code-review, code-audit, and MR-feedback as Duties
  assigned to a "developer" Agent to dogfood the model; resolve the inline-comment seam (§11).

**Sequencing:** SP2, SP3, and SP4 each depend only on SP1 and are mutually independent, so they can
be ordered by priority. SP2 is called out as first-class (not an afterthought) and is the biggest of
the five — schedule it whenever open-source / custom-model agentic work becomes a priority. SP5
depends on whichever capabilities its ported duties need. Each sub-project gets its own spec → plan →
implementation cycle.

---

## 14. SP1 — Core engine (detailed)

### 14.1 Objective
Prove the full pipeline end-to-end with the smallest real surface: a "developer" **Agent**
(system prompt + default `claude`) has one assigned **Duty** (`mr-reviewer`). An operator (or cron)
fires the **Assignment** → OfficeFleet composes the prompt in three layers (Agent system prompt →
Duty task prompt, optionally overridden → assignment extra instructions) → runs a tool-using LLM in
a workspace with `glab` available → the LLM
produces a structured review → **OfficeFleet posts the result as a GitLab MR comment via the GitLab
plugin** → the Run (agent/duty/assignment, prompts, result, output, status, cost) is recorded.
No event bus, no web UI, no catalog UX. The schema includes all four entities; the slice exercises
exactly one of each.

### 14.2 Proposed package layout
```
fleet/
  cmd/fleet/              main(): CLI entrypoint
  internal/
    config/              YAML config load/validate (HUGINN-style env for secrets)
    db/                  pgx pool, migrations runner, migrations/*.sql
    domain/              Agent, Duty, Assignment, Run, Trigger, types (pure, no I/O)
    plugin/              Plugin interface, registry, capability types
    plugins/gitlab/      GitLab integration plugin (event sources stubbed; actions: post_mr_comment)
    trigger/             Trigger interface; manual + cron implementations; scheduler
    executor/            Executor interface; claude CLI backend; named-backend config + effort resolution; workspace mgmt; LLM result contract
    prompt/              text/template engine + helper library + preview
    state/               per-assignment state store (KV + structured) over Postgres
    run/                 the run pipeline: resolve backend → render → execute → deliver → record
    outputs/             output-action delivery (renders params, calls plugin actions)
  configs/               example fleet.yaml (one agent + one duty + one assignment)
```

### 14.3 Core interfaces (Go sketch)
```go
// plugin
type Plugin interface {
    Name() string
    EventSources() []EventSource          // may be empty in SP1
    Actions() []Action
    ConfigSchema() Schema
    Init(ctx context.Context, cfg map[string]any, secrets SecretLookup) error
    Do(ctx context.Context, action string, params map[string]any) (map[string]any, error)
}

// trigger
type Trigger interface {
    Kind() string                          // "manual" | "cron"
    // manual: Fire(params); cron: Schedule() + Next()
}

// executor — note: LLM*, not Agent* (Agent is a domain entity)
// SP1 ships ONE impl: the claude CLI agentic backend. Endpoint backends + the generic
// agent loop are SP2; the interface is shaped so they slot in without changing callers.
type Executor interface {
    Kind() string                          // "claude" (SP1); "openai-compatible" etc. in SP2
    Run(ctx context.Context, req LLMRequest) (LLMResult, error)
}
type LLMRequest struct {
    SystemPrompt string                    // from Agent.system_prompt (rendered)
    Prompt       string                    // from Duty.prompt (+ assignment add-ons), rendered
    Workspace    string
    Tools        []string                  // CLI names on PATH — sufficient for CLI backends;
                                           // endpoint backends need a richer tool abstraction (SP2)
    Model        string                    // resolved: ref.model ?? backend.model
    Effort       string                    // resolved: ref.effort ?? backend.default_effort
}
type LLMResult struct {
    Status     int                          // 0 ok, nonzero failure classes
    Summary    string
    Output     map[string]any               // structured contract for output templates
    Transcript string
    Tokens     int
    Cost       float64
}

// state — keyed by assignment
type Store interface {
    Get(ctx, assignmentID, key string) ([]byte, bool, error)
    Set(ctx, assignmentID, key string, val []byte) error
    Delete(ctx, assignmentID, key string) error
    AppendNote(ctx, assignmentID string, note any) error
}
```

### 14.4 Data model (Postgres, SP1 subset)
- `agents` — id, name, role, system_prompt, default_backend (json), enabled, timestamps.
- `duties` — id, name, role, description, trigger_kinds, prompt, required_tools, output_actions (json),
  config_schema (json), backend (json, nullable), timestamps.
- `assignments` — id, agent_id (fk), duty_id (fk), enabled, trigger (json), outputs (json),
  config (json), backend (json, nullable), task_prompt_override (text, nullable),
  extra_instructions (text, nullable), timestamps. Unique-ish per (agent, duty, purpose).
- `runs` — id, assignment_id (fk), agent_id, duty_id, trigger_kind, rendered_system_prompt,
  rendered_prompt, llm_result (json), status, tokens, cost, timestamps, error.
- `assignment_state` — (assignment_id, key) → value bytes; plus a notes table for structured/append memory.
- `secrets` — name → encrypted blob (GitLab token in SP1; later, backend API keys).

*Named **backends** are **config**, not a table in SP1: they live in `fleet.yaml` and are referenced
by name from `agents`/`duties`/`assignments`. (A managed-backends table can come with the SP4 UI.)*
Migrations follow huginn's `db/migrations` pattern.

### 14.5 GitLab plugin (SP1 scope)
- **Auth/config**: project, token (from `secrets`, env-seeded in SP1).
- **Actions**: `post_mr_comment(project, mr_iid, body)` (real). `resolve_discussion`, `create_issue`
  stubbed/optional.
- **Event sources**: declared but **not wired** in SP1 (no bus yet); shape proven for SP3.
- Uses `glab` CLI and/or the GitLab REST API (reuse huginn's GitLab knowledge).

### 14.6 Sample agent + duty + assignment (SP1)
- **Backend** `claude-default` (in `fleet.yaml`) — kind `claude` (CLI agentic), `default_effort: high`,
  `auth: { mode: subscription }` (SP1 also supports `auth: { mode: api_key, api_key: ${secret:…} }`
  for the claude backend, since the CLI handles both).
- **Agent** `dev-1` — role `developer`, a system prompt establishing the reviewer persona,
  `default_backend: claude-default` (effort resolved from the backend unless the ref overrides it).
- **Duty** `mr-reviewer` — role category `developer`; supports triggers `manual` + `cron`;
  `required_tools: [glab]`; task prompt template renders from `event.params.mr_iid`, `agent`, and
  `state`; `output_actions: [{ plugin: gitlab, action: post_mr_comment }]`.
- **Assignment** `dev-1 × mr-reviewer` — trigger `manual` (param `mr_iid`) and a `cron` variant
  ("review open MRs needing attention"); `config` supplies the GitLab project; `outputs` routes the
  review to `post_mr_comment`; `extra_instructions` demonstrates per-agent tailoring (e.g. "focus on
  concurrency and error handling; keep comments terse"). Proves the three-layer prompt composition.
- **State** (per assignment) records the reviewed commit sha to skip duplicate reviews on re-run.

### 14.7 CLI (SP1)
```
fleet migrate                              # run DB migrations
fleet config validate                      # validate fleet.yaml (backends, agents, duties, assignments)
fleet backends list
fleet backends login <name>                # subscription backends: run the vendor OAuth/device-auth flow
fleet agents list
fleet duties list
fleet assignments list
fleet run <assignment-id> [--param k=v]    # manual trigger, end-to-end
fleet schedule                             # run the cron scheduler loop (daemon)
```
*(`fleet run` also accepts `--agent <name> --duty <name>` to resolve the assignment by names.)*

### 14.8 Testing strategy
- **Unit**: domain types, backend resolution precedence, three-layer prompt composition (golden
  tests: system + task, plus `task_prompt_override` and `extra_instructions` variants), output param
  rendering, state store, config validation, LLM result parsing.
- **Plugin**: GitLab plugin actions against a mocked GitLab API / fake `glab`.
- **Integration**: full `fleet run <assignment>` against an ephemeral Postgres (testcontainers or a
  CI service) with a stubbed executor returning a canned `LLMResult`, asserting a Run is recorded
  (with agent/duty/assignment ids + both prompts) and the GitLab `post_mr_comment` action is invoked
  with rendered params.
- **Executor**: a fake/mockable backend so the pipeline is testable without real LLM calls; one
  optional live smoke test behind a flag.

### 14.9 SP1 acceptance criteria
1. `fleet migrate` creates the schema (agents, duties, assignments, runs, assignment_state, secrets).
2. A `fleet.yaml` defining the `dev-1` Agent, the `mr-reviewer` Duty, and their Assignment validates
   and lists via `fleet agents/duties/assignments list`.
3. `fleet run <assignment> --param mr_iid=<n>` resolves the backend, renders the system prompt
   (Agent) + task prompt (Duty), runs the (stubbed-in-tests / real in manual smoke) LLM with `glab`
   available, posts an MR comment via the GitLab plugin, and records a complete Run (ids, both
   prompts, result, output delivery, tokens/cost, status).
4. Re-running on an unchanged MR is **skipped** via per-assignment state dedup.
5. The `cron` trigger fires the same pipeline on schedule.
6. Backend resolution precedence (`Assignment ?? Duty ?? Agent`) selects the named backend, and
   effective effort resolves as `ref.effort ?? backend.default_effort`. Backend auth is honored: a
   `subscription` backend uses the logged-in CLI session, an `api_key` backend injects its key, and
   config-validate / run-start flag a missing credential.
7. Per-agent prompt customization is applied and recorded: with `task_prompt_override` set the task
   prompt is replaced, and `extra_instructions` is appended after it; both reflected in the Run's
   `rendered_prompt`.
8. Unit + integration tests pass; lint + vet clean.

---

## 15. Open questions / deferred

- **Inline-comment seam** (§11): structured-output vs sanctioned plugin tool — decided in SP5.
- **Generic agent-loop tool abstraction** (§11): `LLMRequest.Tools` (CLI names on PATH) suffices for
  CLI backends but not endpoint backends; the loop (SP2) needs a richer tool spec + execution bridge
  + iteration/safety limits. Key open design question for SP2.
- **LLM providers as a full plugin SDK** — v1 uses configured backends (a fixed set + a generic
  endpoint backend), not a plugin kind; revisit only if third-party providers need to ship code.
- **Agent-scoped shared memory** — per-assignment state is in v1; a cross-duty memory at the Agent
  level may be added later.
- **Concurrency & rate limiting** across assignments and backends — refined in SP3.
- **Subscription availability / quota handling** — subscription backends hit plan rate limits and
  outages; a status/fail-open or fallback-to-another-backend strategy (cf. huginn's `claude_status`)
  is deferred.
- **Cost controls / budgets** per agent or assignment — likely an SP4 settings concern.
- **Continuous-trigger supervision/restart semantics** — designed when implemented (post-SP3).
- **CLI binary name** — spec uses `fleet`; confirm `fleet` vs `office` before SP1 implementation.
