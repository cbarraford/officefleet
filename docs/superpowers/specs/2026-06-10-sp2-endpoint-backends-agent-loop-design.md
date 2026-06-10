# SP2 — Endpoint Backends & the Generic Agent Loop — Design Spec

**Status:** Draft for review
**Date:** 2026-06-10
**Author:** brainstormed with Claude Code
**Parent:** `spec.md` §11 (Execution), §13 (SP2 entry), §15 (open questions). SP1 (core engine)
is complete; this spec resolves SP2's open design question — the tool abstraction for endpoint
backends — and details the implementation.

---

## 1. Summary

SP1 ships one execution path: CLI agentic backends (`claude`), where the vendor CLI runs its own
tool-use loop. SP2 adds the second path promised in §11: **endpoint backends**
(`openai-compatible` — Ollama, vLLM, llama.cpp, hosted OpenAI-compatible APIs), which expose a raw
chat/completions API with no agent loop. OfficeFleet therefore ships **its own generic agent
loop**: it drives the model turn by turn, brokers tool calls (a sandboxed shell + file tools in
the run's isolated workspace), enforces safety limits (max iterations, per-command timeout,
output caps, optional command allowlist), captures a transcript, and extracts the structured
`LLMResult` via a mandatory `submit_result` tool. SP2 also ships a **minimal multi-model voter**
(`kind: voter`): a panel of named backends run concurrently and aggregated by a simple strategy.

The SP1 `Executor` interface, `LLMRequest`, and `LLMResult` are **unchanged**. SP2 is additive:
two new `Executor` implementations (`EndpointExecutor`, `VotingExecutor`), three new supporting
packages, new `Backend` config fields, and a centralized executor factory replacing the CLI's
inline kind dispatch.

### Decisions locked during brainstorming

1. **Whole-computer toolset.** The loop exposes a small fixed toolset — `run_command`,
   `read_file`, `write_file`, `list_dir` — operating in the run's isolated workspace, with the
   duty's `required_tools` available on PATH. Plugin actions remain **post-run outputs** exactly
   as in SP1; plugin-actions-as-in-loop-tools stays deferred to the SP5 inline-comment seam
   (spec.md §11). Maximum parity with the claude CLI path; one thin execution bridge.
2. **Native function-calling, pluggable.** The loop speaks the openai-compatible
   `tools` / `tool_calls` API. A `ToolProtocol` interface isolates encoding/decoding so a
   text/ReAct adapter for models without native tool support can be added later without
   reshaping the loop. Only the native implementation ships in SP2.
3. **Mandatory `submit_result` tool.** The loop registers a synthetic
   `submit_result(summary, status, output)` tool alongside the work tools. Calling it terminates
   the loop and becomes the `LLMResult` — a strong, explicit result contract whose `output`
   object feeds output-action param templates directly.
4. **Minimal voter included.** `kind: voter` with a panel of named backends and a
   `first_success` | `majority` strategy. Majority votes on the integer status code (see §6 for
   the honest limitation); semantic/judge-based voting is deferred.
5. **Permissive shell with hard rails.** `run_command` always enforces cwd=workspace, a
   per-command timeout, and output truncation; an optional `command_allowlist` on the backend
   locks it down. The loop itself is bounded by `max_iterations`.

---

## 2. Goals & non-goals

### Goals
- Endpoint backends configured by `base_uri` / `model` / auth / `default_effort` / custom
  `params` (spec.md §11 example: `local-ollama`).
- A generic, provider-agnostic **agent loop**: tool-call protocol, iteration control, a
  **tool-execution bridge** (workspace shell + files), safety limits, transcript capture.
- The structured-result contract (`submit_result`) so endpoint runs feed output templates with
  the same fidelity as the claude path.
- A **minimal voter** executor composing existing backends.
- Centralized executor construction (`FromBackend`) replacing duplicated CLI dispatch.
- Zero changes to the SP1 `Executor` / `LLMRequest` / `LLMResult` types (one small, explicit
  pipeline behavior change — §8 — is the only shared-code edit).

### Non-goals (SP2)
- Plugin actions as in-loop model tools (SP5 inline-comment seam decides this).
- Text/ReAct protocol adapter (interface ships; adapter later, when a no-native-tools model
  needs it).
- Semantic/judge-based vote aggregation.
- USD cost computation for endpoint backends (no price table; `Cost = 0`, ties into §15 cost
  controls).
- Streaming responses, parallel tool-call batching guarantees beyond sequential execution,
  multi-turn memory across runs.
- Container/jail sandboxing of `run_command` beyond cwd+timeout+allowlist (single-org,
  trusted-operator posture per spec.md §2 non-goals).

---

## 3. Architecture

### 3.1 The structural claim

`Executor` (`Kind()` + `Run(ctx, LLMRequest) → LLMResult`) stays byte-for-byte as SP1 shipped
it. Loop and safety limits ride on **backend config** (reaching the executor at construction,
the same way `ClaudeExecutor` receives its API key), not on `LLMRequest`. `required_tools`
already flows via `LLMRequest.Tools`; the loop surfaces it to the model as "CLIs available to
you on PATH" in the user prompt preamble. Proving the SP1 interface holds is an acceptance
criterion (§10 #7).

### 3.2 Package layout (new code)

```
internal/
  agentloop/                 PURE orchestration — no HTTP, no os/exec
    loop.go                  the agent loop: drive chat → decode tool calls → execute → repeat
    protocol.go              ToolProtocol interface + nativeProtocol (openai tools/tool_calls)
    tool.go                  ToolSpec, ToolCall, ChatClient/ToolBridge ifaces, Message types
    openai/
      client.go              openai-compatible /chat/completions HTTP client (transport only)
    bridge/
      bridge.go              workspace ToolBridge: run_command/read_file/write_file/list_dir/
                             submit_result; owns os/exec + fs + safety limits
  executor/
    endpoint.go              EndpointExecutor: assembles client+bridge+loop; implements Executor
    voter.go                 VotingExecutor: fan-out panel, aggregate; implements Executor
    factory.go               FromBackend(cfg, backend) (Executor, error) — kind dispatch,
                             recursive for voter panels
```

`agentloop` depends only on two injected interfaces — `ChatClient` (transport) and `ToolBridge`
(effects) — so the loop is fully unit-testable with fakes and contains zero I/O. `openai/` and
`bridge/` are the only packages that touch the network and the OS respectively.

### 3.3 Core interfaces (Go sketch)

```go
// agentloop — provider-agnostic
type Message struct {
    Role       string     // "system" | "user" | "assistant" | "tool"
    Content    string
    ToolCalls  []ToolCall // assistant messages carrying tool calls
    ToolCallID string     // tool messages: which call this observes
}

type ToolSpec struct {
    Name        string
    Description string
    Parameters  map[string]any // JSON schema
}

type ToolCall struct {
    ID   string
    Name string
    Args map[string]any
}

type ChatRequest struct {
    Model    string
    Messages []Message
    Tools    any            // protocol-encoded tool specs
    Params   map[string]any // backend.params passthrough (e.g. num_ctx)
}

type ChatResponse struct {
    Message Message
    Usage   Usage // input/output token counts
}

type ChatClient interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

type ToolBridge interface {
    Specs() []ToolSpec // run_command, read_file, write_file, list_dir, submit_result
    // Execute runs one tool call. For submit_result it returns done=true + the finished result.
    Execute(ctx context.Context, call ToolCall) (observation string, done bool,
        result *domain.LLMResult, err error)
}

type ToolProtocol interface {
    Encode(specs []ToolSpec) any           // -> request "tools" field shape
    Decode(resp ChatResponse) []ToolCall   // <- response "tool_calls"
}

type Opts struct {
    Model         string
    Params        map[string]any
    MaxIterations int
}

func Run(ctx context.Context, client ChatClient, bridge ToolBridge, proto ToolProtocol,
    sysPrompt, userPrompt string, requiredTools []string, opts Opts) (domain.LLMResult, error)
```

### 3.4 The loop algorithm

```
messages = [ system(sysPrompt),
             user(userPrompt + availableToolsHint(requiredTools)) ]
specs    = bridge.Specs()                          // includes submit_result
nudged   = false
for i := 0; i < opts.MaxIterations; i++ {
    resp = client.Chat(ctx, {messages, tools: proto.Encode(specs), model, params})
    accumulate resp.Usage tokens; append exchange to transcript
    calls = proto.Decode(resp)
    if len(calls) == 0 {                           // model "talked" instead of acting
        if nudged { return finalize(failure, summary = resp.Message.Content) }
        nudged = true
        append assistant(resp.Message); append user("call submit_result to finish")
        continue
    }
    append assistant(resp.Message)                 // carries the tool calls
    for _, c := range calls {
        obs, done, result, err := bridge.Execute(ctx, c)
        if err != nil { return failure }           // bridge-internal error only (rare)
        if done {                                  // submit_result
            result.Transcript = transcript; result.Tokens = tokens
            return *result, nil
        }
        // tool errors are NOT loop failures — obs carries the error text so the
        // model can self-correct
        append tool(c.ID, obs)
    }
}
return LLMResult{Status: 1, Summary: "max iterations reached without submit_result",
                 Transcript: transcript, Tokens: tokens}, ErrMaxIterations
```

Key behaviors:
- **Tool failures feed back to the model.** A failed command, missing file, or allowlist denial
  becomes the observation text. Only transport errors (after retry, §8), bridge-internal
  errors, max-iterations, and context cancellation fail the run.
- **`submit_result` is the single clean exit.** Its arguments become the `LLMResult`; the loop
  stamps `Transcript` and `Tokens` on top.
- **Transcript** = the full serialized message exchange (JSON lines of every request/response
  turn), stored on `LLMResult.Transcript` exactly as the claude path stores raw CLI output.
- **Tokens** are summed from each response's `usage`. **`Cost` = 0** for endpoint backends in
  SP2 (no price table — §11 open questions).
- The model is told about `required_tools` (e.g. `glab`) in the prompt preamble; it reaches
  them through `run_command`.

### 3.5 The tool bridge (workspace)

`bridge.New(workspace string, limits Limits)` where `Limits{CommandTimeout, MaxOutputBytes,
CommandAllowlist}` comes from backend config. Tools exposed:

| Tool | Schema (JSON) | Behavior |
|---|---|---|
| `run_command` | `{cmd: string}` | `sh -c cmd` with `Dir=workspace`, per-command timeout, stdout+stderr captured, truncated to `MaxOutputBytes` with a `[truncated]` marker; exit code appended to the observation. If `CommandAllowlist` is non-empty, the first whitespace-separated token of `cmd` (basename-matched) must be in it — denial is an observation, not an error. |
| `read_file` | `{path: string}` | Read file; relative paths resolve under workspace; paths escaping the workspace are denied (observation). Truncated to `MaxOutputBytes`. |
| `write_file` | `{path, content: string}` | Write/overwrite file under workspace; same path containment. |
| `list_dir` | `{path: string}` (optional, default `.`) | Directory listing under workspace. |
| `submit_result` | `{summary: string, status: integer, output: object}` | Terminates the loop; becomes `LLMResult{Status, Summary, Output}`. `status` 0 = success, nonzero = the model reporting failure. `output` is the free-form object output templates render against. |

Path containment applies to the file tools; `run_command` is bounded by cwd + allowlist +
timeout (the trusted-operator posture carries the rest — §2 non-goals).

### 3.6 EndpointExecutor

`Kind() == "openai-compatible"`. Constructed by the factory with the resolved `config.Backend`.
On each `Run(ctx, req)`:

1. Build the `openai.Client` — `base_uri`, resolved `req.Model`, auth (`api_key` →
   `Authorization: Bearer …`; `none` → no header), `params` passthrough.
2. Build a workspace bridge — `req.Workspace` + the backend's `Limits`.
3. `agentloop.Run(ctx, client, bridge, nativeProtocol, req.SystemPrompt, req.Prompt,
   req.Tools, opts)`.

`auth.mode: subscription` on an endpoint kind is a **config-validation error** (§7).
`req.Effort` is accepted but unused by endpoint backends in SP2 (no portable effort semantics on
raw chat APIs); it remains resolved and recorded for forward compatibility.

---

## 4. The openai-compatible client

`internal/agentloop/openai/client.go` — transport only.

- `POST {base_uri}/chat/completions` with `{model, messages, tools, ...params}`.
- Message mapping: `Message` ↔ openai wire shape (`role`, `content`, `tool_calls` with
  `function.name` / `function.arguments` JSON-string, `tool_call_id`).
- Tool-call arguments arrive as a JSON string; the client decodes into `map[string]any`. A
  malformed-JSON argument string becomes a decode error surfaced to the loop, which feeds it
  back to the model as an observation ("your tool call arguments were not valid JSON").
- `usage` (`prompt_tokens`, `completion_tokens`) mapped to `Usage`.
- **Retry:** 2 retries with exponential backoff on HTTP 429 and 5xx, honoring context
  cancellation. Other non-2xx and transport failures return immediately.
- No streaming in SP2.

---

## 5. Prompting the loop

The endpoint path reuses the SP1 three-layer composition untouched (Agent system prompt →
task prompt → extra instructions, rendered by `internal/prompt`). The loop adds a short,
fixed **harness preamble** to the user message (not the rendered prompts, which are recorded on
the Run verbatim):

- the working directory is an isolated workspace;
- these CLIs are available on PATH: `{required_tools}`;
- work step by step using the provided tools;
- you MUST finish by calling `submit_result` (its `output` object is consumed by automation —
  include the fields the task asks for).

The preamble is a constant in `agentloop`, covered by golden tests, and deliberately minimal —
persona and task stay the operator's domain.

---

## 6. VotingExecutor (minimal voter)

`Kind() == "voter"`. Wraps `panel []executor.Executor` (built recursively by the factory from
the named backends) + `strategy`.

`Run` fans the **same `LLMRequest`** out to every panel member concurrently. Each member gets
its **own workspace subdirectory** (`{req.Workspace}/panel-{i}`) so concurrent shells and file
writes don't collide; the voter creates these before launch.

Aggregation:
- **`first_success`** — return the first result *by completion order* with `Status == 0`;
  cancel the remaining members via context. If none succeed, return the last failure observed.
- **`majority`** — wait for all members; **plurality vote on the integer `Status` code**. If
  two status groups tie in size, the winning group is the one containing the lowest-panel-index
  member. The representative result is the lowest-panel-index member within the winning group.

Aggregated accounting: `Tokens` and `Cost` are **summed across all members that ran** (the
operator paid for them all); `Transcript` is the representative member's transcript prefixed
with a one-line panel summary (member → status/tokens).

**Honest limitation (recorded, deliberate):** majority votes on the status code, not semantic
content. For duties where every member returns `status: 0`, majority collapses to "earliest
panel member that succeeded." That is the defensible *minimal* voter; judge-based/semantic
strategies are deferred (§11).

Guards (config-validate, §7): panel non-empty; every member resolves to a defined backend;
**members may not themselves be voters** (no nesting in v1).

---

## 7. Config additions

`config.Backend` gains:

```yaml
backends:
  - name: local-ollama
    kind: openai-compatible
    base_uri: http://localhost:11434/v1     # REQUIRED for openai-compatible
    model: llama3.1:70b                      # REQUIRED for openai-compatible
    auth: { mode: none }                     # none | api_key; subscription => error
    default_effort: medium
    params: { num_ctx: 8192 }                # passthrough to provider request body
    max_iterations: 25                       # loop cap            (default 25)
    command_timeout: 120s                    # per run_command     (default 120s)
    max_output_bytes: 65536                  # observation cap     (default 64 KiB)
    command_allowlist: []                    # empty => allow all

  - name: review-panel
    kind: voter
    strategy: first_success                  # first_success | majority (REQUIRED)
    panel: [claude-sub, local-ollama]        # >=1 names of non-voter backends (REQUIRED)
```

New struct fields: `MaxIterations int`, `CommandTimeout time.Duration` (yaml duration string),
`MaxOutputBytes int`, `CommandAllowlist []string`, `Strategy string`, `Panel []string`.

Validation added to `config.Validate`:
- `kind: openai-compatible` → `base_uri` and `model` required; `auth.mode` must be `api_key` or
  `none` (`subscription` rejected with a pointed message).
- `kind: voter` → `strategy` ∈ {`first_success`, `majority`}; `panel` non-empty; each member
  defined; no member of `kind: voter`; `base_uri`/loop fields rejected (meaningless on a voter).
- `kind: claude` validation unchanged.
- Existing `${env:…}` / secret expansion covers `api_key` exactly as SP1 (managed-secrets UI is
  SP4).

`BackendRef` overrides (`model`, `effort`) keep SP1 semantics for `claude` and
`openai-compatible` refs. On a `voter` ref, `model` and `effort` overrides are **validation
errors** — each panel member resolves its own model/effort, so an override on the voter is
ambiguous.

---

## 8. Run pipeline & CLI integration

### Executor factory

`executor.FromBackend(cfg *config.Config, b *config.Backend) (Executor, error)`:
`claude` → `NewClaudeExecutor(apiKeyIfAny)`; `openai-compatible` → `NewEndpointExecutor(b)`;
`voter` → resolve panel names → recurse → `NewVotingExecutor(panel, strategy)`. The two CLI
dispatch sites (`run`, `schedule` in `cmd/fleet/main.go`) replace their inline
`if kind=="claude" … else "not supported"` blocks with one `FromBackend` call each. `--fake`
behavior unchanged.

New CLI subcommand: **`fleet backends test <name>`** — one-shot connectivity/auth smoke
("reply with only the word OK" through the real executor, no pipeline, no DB write).

### The one shared-code change (explicit)

`run.Pipeline.Execute` currently treats any non-error `LLMResult` as success. SP2 maps
**`llmResult.Status != 0` → `RunStatusFailed`** (with `run.Error` set from the summary). This
makes the `submit_result` status contract meaningful and also correctly captures the claude
path's `is_error` (which already sets `Status: 1` but is currently ignored). Output delivery is
**skipped** for a failed-status result — a failed run must not post half-formed comments.
This is the only behavioral edit to SP1 code, called out so it gets its own tests and review
attention.

### Error handling summary

| Failure | Handling |
|---|---|
| Transport non-2xx / dial failure | 2 retries w/ backoff on 429 & 5xx; then `Run` returns error → pipeline records run **failed** (SP1 path) |
| Tool error (bad cmd, missing file, allowlist denial, malformed args) | Observation fed back to the model; never fails the run |
| `submit_result` with nonzero `status` | Run recorded **failed**, summary preserved, outputs skipped |
| Max iterations reached | `LLMResult{Status:1, Summary:"max iterations…"}` + error → run **failed**, transcript retained |
| Context cancelled / deadline | Propagates out of `Run` → run **failed** |
| Voter: no member succeeds (`first_success`) | Last failure returned → run **failed** |

---

## 9. Testing strategy

- **Unit — loop (pure):** fake `ChatClient` + fake `ToolBridge`: terminates on
  `submit_result`; max-iterations failure; tool-error observation fed back; no-tool-call nudge
  then finalize-as-failure; token accumulation; transcript accrual; context cancellation.
- **Unit — openai client:** `httptest.Server` with canned `/chat/completions` exchanges (a
  `tool_calls` turn, then a `submit_result` turn): request encoding (`tools` field, `model`,
  `params` passthrough, bearer header present/absent by auth mode), response decoding,
  malformed tool-arg JSON, `usage` mapping, 429/5xx retry then success, retry exhaustion.
- **Unit — bridge:** `run_command` cwd anchoring, timeout kill, output truncation marker,
  allowlist allow/deny-as-observation; `read_file`/`write_file`/`list_dir` incl. workspace
  escape denial; `submit_result` arg parsing → `LLMResult`; missing/invalid args as
  observations.
- **Unit — voter:** `first_success` returns first 0-status by completion order and cancels the
  rest; `majority` plurality + tie → panel order; all-fail; token/cost summing; per-member
  workspace isolation (fakes recording their workspace).
- **Unit — config:** all §7 validation rules, defaults applied when fields unset, duration
  parsing.
- **Unit — pipeline change:** `Status != 0` → failed + outputs skipped; claude `is_error`
  parity; `Status == 0` unchanged.
- **Integration:** `fleet run` with an endpoint backend pointed at an `httptest` server
  end-to-end against ephemeral Postgres (SP1 harness): Run recorded with both prompts, summary,
  Output map, tokens, transcript; output delivered via the fake plugin path.
- **Live (flagged):** `-live-ollama` smoke against a local Ollama with native tool support,
  mirroring the existing `-live` claude flag.

---

## 10. Acceptance criteria

1. A `fleet.yaml` defining an `openai-compatible` backend (base_uri, model, auth, params, loop
   limits) validates; `subscription` auth on it is rejected; `voter` validates strategy + panel
   and rejects nesting, empty panels, unknown members, and model overrides on voter refs.
2. `fleet run <assignment>` on an endpoint-backed assignment drives the generic loop against a
   stub server: the model uses `run_command`/file tools and finishes with `submit_result`; a
   complete Run is recorded (both rendered prompts, summary, Output map, tokens, transcript,
   outputs delivered).
3. `submit_result.output` populates `LLMResult.Output` and renders an output action's params
   (e.g. `post_mr_comment` body) — parity with the claude path.
4. `run_command` enforces cwd=workspace, per-command timeout, output truncation, and the
   allowlist when set; a denied or failed command is fed back to the model as an observation
   rather than failing the run; file tools cannot escape the workspace.
5. The loop fails cleanly on max-iterations and on transport error after bounded retry;
   `llmResult.Status != 0` marks the Run failed and skips output delivery (claude `is_error`
   included).
6. `voter` runs its panel concurrently in isolated workspace subdirs and aggregates per
   `first_success` / `majority` as specified, summing tokens/cost across members.
7. SP1's `Executor` / `LLMRequest` / `LLMResult` types are unchanged; all SP1 tests still pass;
   `fleet run --fake` unchanged; lint + vet clean.
8. `fleet backends test <name>` performs a one-shot smoke run against the named backend.

---

## 11. Open questions / deferred

- **Semantic/judge-based voting** — the minimal voter votes on status code only; an
  LLM-as-judge or content-similarity strategy is a later, separate piece.
- **USD cost for endpoint backends** — `Cost = 0` until a price table/config exists (ties into
  spec.md §15 cost controls).
- **Duty-level `command_allowlist`** — SP2 scopes the allowlist to the backend; a per-duty
  override (intersection semantics) is a later refinement.
- **Text/ReAct `ToolProtocol` adapter** — the interface ships in SP2; the adapter lands when a
  no-native-tools model needs it.
- **Effort semantics on endpoint backends** — `effort` is resolved and recorded but unused;
  mapping it to sampling params (or reasoning-effort fields on servers that support them) is
  deferred.
- **Streaming / parallel tool-call execution** — sequential, non-streaming in SP2.
- **Plugin actions as in-loop tools** — unchanged from spec.md §11: decided at SP5 with the
  inline-comment seam.
