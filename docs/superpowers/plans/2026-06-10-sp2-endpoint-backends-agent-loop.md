# SP2 — Endpoint Backends & Generic Agent Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add openai-compatible endpoint backends driven by OfficeFleet's own generic tool-using agent loop, plus a minimal multi-model voter, without changing the SP1 `Executor` interface.

**Architecture:** A pure `internal/agentloop` package (no I/O) drives chat turns through two injected interfaces — `ChatClient` (HTTP transport, implemented in `agentloop/openai`) and `ToolBridge` (workspace shell/file tools + `submit_result`, implemented in `agentloop/bridge`). `executor.EndpointExecutor` assembles them; `executor.VotingExecutor` fans out a panel; `executor.FromBackend` centralizes kind dispatch for the CLI. One pipeline change: `LLMResult.Status != 0` → run failed, outputs skipped.

**Tech Stack:** Go 1.26 stdlib only (net/http, net/http/httptest, os/exec). No new module dependencies. Existing: cobra, yaml.v3, google/uuid.

**Spec:** `docs/superpowers/specs/2026-06-10-sp2-endpoint-backends-agent-loop-design.md`

**Three spec refinements locked here (serve the spec's intent; deviations from its letter):**
1. The loop returns `(LLMResult{Status: 1, …}, nil)` — a *nil error* — for model-level failures (max-iterations, model refuses to call tools). Rationale: the pipeline's executor-error path records only an error message and would lose the transcript; the new `Status != 0` pipeline path records the full result including the transcript, which the spec requires ("transcript retained"). Non-nil errors are reserved for transport errors (after retry), bridge-internal errors, and context cancellation — those still fail the run via the SP1 error path.
2. Voter panel members carry their own `Model`/`Effort` (captured from each member backend at factory time). The voter backend itself has no `model` (validation rejects it), so `VotingExecutor` must override `req.Model`/`req.Effort` per member before fan-out.
3. A run that fails via `Status != 0` does **not** mark the dedup key processed, so the work can be retried. (Matches the spec's "outputs skipped" plus the §15 retry intent.)

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `internal/agentloop/tool.go` | Create | Shared types: Message, ToolSpec, ToolCall, ChatRequest/Response, Usage, ChatClient, ToolBridge, ToolProtocol, Opts |
| `internal/agentloop/protocol.go` | Create | `Native` ToolProtocol (openai tools/tool_calls encode/decode) |
| `internal/agentloop/loop.go` | Create | `agentloop.Run` — the generic agent loop + harness preamble + transcript |
| `internal/agentloop/loop_test.go` | Create | Loop tests with fake client/bridge |
| `internal/agentloop/protocol_test.go` | Create | Protocol encode/decode tests |
| `internal/agentloop/bridge/bridge.go` | Create | Workspace ToolBridge: run_command/read_file/write_file/list_dir/submit_result + Limits |
| `internal/agentloop/bridge/bridge_test.go` | Create | Bridge tests |
| `internal/agentloop/openai/client.go` | Create | openai-compatible /chat/completions HTTP client + retry |
| `internal/agentloop/openai/client_test.go` | Create | Client tests via httptest |
| `internal/config/config.go` | Modify | Backend fields (loop limits, voter), validation rules |
| `internal/config/config_test.go` | Modify | New validation tests |
| `internal/executor/endpoint.go` | Create | EndpointExecutor |
| `internal/executor/endpoint_test.go` | Create | EndpointExecutor e2e via httptest |
| `internal/executor/voter.go` | Create | VotingExecutor + PanelMember |
| `internal/executor/voter_test.go` | Create | Voter strategy tests |
| `internal/executor/factory.go` | Create | FromBackend kind dispatch (recursive for voter) |
| `internal/executor/factory_test.go` | Create | Factory tests |
| `internal/executor/endpoint_live_test.go` | Create | `-live-ollama` flagged smoke |
| `internal/run/pipeline.go` | Modify | `Status != 0` → failed, outputs + dedup-mark skipped |
| `internal/run/pipeline_test.go` | Modify | Tests for the above |
| `internal/run/pipeline_endpoint_test.go` | Create | Full pipeline through a real EndpointExecutor against httptest |
| `cmd/fleet/main.go` | Modify | Replace 2 inline dispatches with FromBackend; add `fleet backends test` |
| `configs/fleet.yaml` | Modify | Commented endpoint + voter backend examples |

Dependency order: Task 1 → 2 → 3 → 4 are agentloop internals; Task 5 (config) is independent; Task 6 (endpoint+factory) needs 1–5; Task 7 (voter) needs 6; Task 8 (pipeline) is independent; Task 9 (CLI) needs 6–7; Task 10 (integration + live) needs all.

Run all commands from the repo root: `/Users/cbarraford/workshop/office-fleet`.

---

### Task 1: agentloop types + Native protocol

**Files:**
- Create: `internal/agentloop/tool.go`
- Create: `internal/agentloop/protocol.go`
- Test: `internal/agentloop/protocol_test.go`

- [ ] **Step 1: Create the types file** (pure declarations — no test needed for declarations themselves; the protocol test below exercises them)

`internal/agentloop/tool.go`:

```go
// Package agentloop implements OfficeFleet's generic tool-using agent loop for
// endpoint backends (raw chat/completions APIs with no built-in agent loop).
// The package is pure orchestration: all I/O is injected via ChatClient
// (transport) and ToolBridge (effects), so the loop is fully testable with fakes.
package agentloop

import (
	"context"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// Message is one chat message in the provider-agnostic shape the loop works with.
type Message struct {
	Role       string     `json:"role"` // "system" | "user" | "assistant" | "tool"
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant messages carrying tool calls
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool messages: which call this observes
}

// ToolSpec describes one tool offered to the model.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON schema for the arguments object
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
	// ArgsError is set by the transport when the provider's argument payload
	// could not be decoded (e.g. malformed JSON). The loop feeds it back to
	// the model as an observation instead of dispatching to the bridge.
	ArgsError string `json:"args_error,omitempty"`
}

// ChatRequest is one round-trip request to the model.
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    any            // protocol-encoded tool specs (provider wire shape)
	Params   map[string]any // backend params passthrough (e.g. num_ctx)
}

// Usage reports token consumption for one round-trip.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// ChatResponse is the model's reply to one ChatRequest.
type ChatResponse struct {
	Message Message
	Usage   Usage
}

// ChatClient performs one chat round-trip. Implemented by agentloop/openai.
type ChatClient interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// ToolBridge executes tool calls. Implemented by agentloop/bridge.
type ToolBridge interface {
	// Specs lists the tools offered to the model, including submit_result.
	Specs() []ToolSpec
	// Execute runs one tool call. For submit_result it returns done=true and
	// the finished result. Tool-level failures (bad command, missing file,
	// denial) are returned as the observation string with a nil error; a
	// non-nil error means the bridge itself is broken and fails the run.
	Execute(ctx context.Context, call ToolCall) (observation string, done bool, result *domain.LLMResult, err error)
}

// ToolProtocol encodes tool specs into the provider request and decodes tool
// calls out of the response. Only the native openai function-calling
// implementation ships in SP2; a text/ReAct adapter can slot in later.
type ToolProtocol interface {
	Encode(specs []ToolSpec) any
	Decode(resp ChatResponse) []ToolCall
}

// Opts configures one loop run.
type Opts struct {
	Model         string
	Params        map[string]any
	MaxIterations int // <=0 means DefaultMaxIterations
}

// DefaultMaxIterations bounds the loop when the backend config does not set it.
const DefaultMaxIterations = 25
```

- [ ] **Step 2: Write the failing protocol test**

`internal/agentloop/protocol_test.go`:

```go
package agentloop

import (
	"testing"
)

func TestNativeProtocol_Encode(t *testing.T) {
	specs := []ToolSpec{
		{
			Name:        "run_command",
			Description: "Run a shell command",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"cmd": map[string]any{"type": "string"}},
				"required":   []string{"cmd"},
			},
		},
	}
	encoded := Native.Encode(specs)
	list, ok := encoded.([]map[string]any)
	if !ok {
		t.Fatalf("Encode returned %T, want []map[string]any", encoded)
	}
	if len(list) != 1 {
		t.Fatalf("Encode returned %d tools, want 1", len(list))
	}
	if list[0]["type"] != "function" {
		t.Errorf("tool type = %v, want %q", list[0]["type"], "function")
	}
	fn, ok := list[0]["function"].(map[string]any)
	if !ok {
		t.Fatalf("function field is %T, want map[string]any", list[0]["function"])
	}
	if fn["name"] != "run_command" {
		t.Errorf("function name = %v, want run_command", fn["name"])
	}
	if fn["description"] != "Run a shell command" {
		t.Errorf("function description = %v", fn["description"])
	}
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Errorf("function parameters missing or wrong type: %T", fn["parameters"])
	}
}

func TestNativeProtocol_Decode(t *testing.T) {
	resp := ChatResponse{
		Message: Message{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_1", Name: "read_file", Args: map[string]any{"path": "a.txt"}},
			},
		},
	}
	calls := Native.Decode(resp)
	if len(calls) != 1 {
		t.Fatalf("Decode returned %d calls, want 1", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Name != "read_file" {
		t.Errorf("unexpected call: %+v", calls[0])
	}
}

func TestNativeProtocol_DecodeNoCalls(t *testing.T) {
	calls := Native.Decode(ChatResponse{Message: Message{Role: "assistant", Content: "just text"}})
	if len(calls) != 0 {
		t.Fatalf("Decode returned %d calls, want 0", len(calls))
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/agentloop/ -run TestNativeProtocol -v`
Expected: FAIL (compile error: `Native` undefined)

- [ ] **Step 4: Implement the native protocol**

`internal/agentloop/protocol.go`:

```go
package agentloop

// nativeProtocol implements ToolProtocol using the openai-compatible native
// function-calling wire shape: tools as {"type":"function","function":{...}}
// in the request, tool calls decoded by the transport into Message.ToolCalls.
type nativeProtocol struct{}

// Native is the openai-compatible function-calling protocol.
var Native ToolProtocol = nativeProtocol{}

func (nativeProtocol) Encode(specs []ToolSpec) any {
	out := make([]map[string]any, len(specs))
	for i, s := range specs {
		out[i] = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        s.Name,
				"description": s.Description,
				"parameters":  s.Parameters,
			},
		}
	}
	return out
}

func (nativeProtocol) Decode(resp ChatResponse) []ToolCall {
	return resp.Message.ToolCalls
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/agentloop/ -run TestNativeProtocol -v`
Expected: PASS (3 tests)

- [ ] **Step 6: Commit**

```bash
git add internal/agentloop/tool.go internal/agentloop/protocol.go internal/agentloop/protocol_test.go
git commit -m "feat(sp2): agentloop types and native function-calling protocol"
```

---

### Task 2: the agent loop

**Files:**
- Create: `internal/agentloop/loop.go`
- Test: `internal/agentloop/loop_test.go`

- [ ] **Step 1: Write the failing loop tests**

`internal/agentloop/loop_test.go`:

```go
package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// scriptedClient returns canned responses in order and records requests.
type scriptedClient struct {
	responses []ChatResponse
	errs      []error // parallel to responses; nil entries mean no error
	requests  []ChatRequest
}

func (s *scriptedClient) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	s.requests = append(s.requests, req)
	i := len(s.requests) - 1
	if i >= len(s.responses) {
		return ChatResponse{}, errors.New("scriptedClient: out of responses")
	}
	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return s.responses[i], err
}

// recordingBridge executes canned observations and terminates on submit_result.
type recordingBridge struct {
	calls        []ToolCall
	observations map[string]string // tool name -> canned observation
}

func (r *recordingBridge) Specs() []ToolSpec {
	return []ToolSpec{
		{Name: "run_command", Description: "run", Parameters: map[string]any{"type": "object"}},
		{Name: "submit_result", Description: "finish", Parameters: map[string]any{"type": "object"}},
	}
}

func (r *recordingBridge) Execute(_ context.Context, call ToolCall) (string, bool, *domain.LLMResult, error) {
	r.calls = append(r.calls, call)
	if call.Name == "submit_result" {
		summary, _ := call.Args["summary"].(string)
		status := 0
		if v, ok := call.Args["status"].(float64); ok {
			status = int(v)
		}
		output, _ := call.Args["output"].(map[string]any)
		if output == nil {
			output = map[string]any{}
		}
		return "", true, &domain.LLMResult{Status: status, Summary: summary, Output: output}, nil
	}
	obs, ok := r.observations[call.Name]
	if !ok {
		obs = "ok"
	}
	return obs, false, nil, nil
}

func assistantToolCall(id, name string, args map[string]any) ChatResponse {
	return ChatResponse{
		Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: name, Args: args}}},
		Usage:   Usage{PromptTokens: 10, CompletionTokens: 5},
	}
}

func TestRun_TerminatesOnSubmitResult(t *testing.T) {
	client := &scriptedClient{responses: []ChatResponse{
		assistantToolCall("c1", "run_command", map[string]any{"cmd": "echo hi"}),
		assistantToolCall("c2", "submit_result", map[string]any{
			"summary": "did the thing", "status": float64(0),
			"output": map[string]any{"key": "val"},
		}),
	}}
	bridge := &recordingBridge{observations: map[string]string{"run_command": "exit code: 0\nhi"}}

	res, err := Run(context.Background(), client, bridge, Native,
		"system prompt", "user prompt", []string{"glab"}, Opts{Model: "m", MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.Status != 0 {
		t.Errorf("Status = %d, want 0", res.Status)
	}
	if res.Summary != "did the thing" {
		t.Errorf("Summary = %q", res.Summary)
	}
	if res.Output["key"] != "val" {
		t.Errorf("Output = %v", res.Output)
	}
	// Tokens: 2 turns * (10+5)
	if res.Tokens != 30 {
		t.Errorf("Tokens = %d, want 30", res.Tokens)
	}
	if res.Transcript == "" || !strings.Contains(res.Transcript, "run_command") {
		t.Errorf("Transcript missing tool call: %q", res.Transcript)
	}
	if len(bridge.calls) != 2 {
		t.Fatalf("bridge calls = %d, want 2", len(bridge.calls))
	}
	// The tool observation must have been fed back as a role:tool message.
	secondReq := client.requests[1]
	last := secondReq.Messages[len(secondReq.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" {
		t.Errorf("expected trailing tool message for c1, got %+v", last)
	}
	if !strings.Contains(last.Content, "exit code: 0") {
		t.Errorf("tool observation = %q", last.Content)
	}
}

func TestRun_PromptsAndPreamble(t *testing.T) {
	client := &scriptedClient{responses: []ChatResponse{
		assistantToolCall("c1", "submit_result", map[string]any{"summary": "s", "status": float64(0)}),
	}}
	bridge := &recordingBridge{}

	_, err := Run(context.Background(), client, bridge, Native,
		"SYSTEM", "TASK", []string{"glab", "git"}, Opts{Model: "m"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	req := client.requests[0]
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "SYSTEM" {
		t.Errorf("system message = %+v", req.Messages[0])
	}
	user := req.Messages[1]
	if user.Role != "user" || !strings.HasPrefix(user.Content, "TASK") {
		t.Errorf("user message = %+v", user)
	}
	if !strings.Contains(user.Content, "glab, git") {
		t.Errorf("preamble missing required tools: %q", user.Content)
	}
	if !strings.Contains(user.Content, "submit_result") {
		t.Errorf("preamble missing submit_result instruction: %q", user.Content)
	}
	if req.Tools == nil {
		t.Error("request Tools is nil; expected encoded specs")
	}
	if req.Model != "m" {
		t.Errorf("Model = %q", req.Model)
	}
}

func TestRun_MaxIterations(t *testing.T) {
	// Model loops forever on run_command.
	responses := make([]ChatResponse, 10)
	for i := range responses {
		responses[i] = assistantToolCall("c", "run_command", map[string]any{"cmd": "ls"})
	}
	client := &scriptedClient{responses: responses}
	bridge := &recordingBridge{}

	res, err := Run(context.Background(), client, bridge, Native,
		"s", "u", nil, Opts{Model: "m", MaxIterations: 3})
	if err != nil {
		t.Fatalf("expected nil error (model-level failure), got %v", err)
	}
	if res.Status == 0 {
		t.Error("expected nonzero Status on max iterations")
	}
	if !strings.Contains(res.Summary, "max iterations") {
		t.Errorf("Summary = %q", res.Summary)
	}
	if res.Transcript == "" {
		t.Error("expected transcript to be retained")
	}
	if len(client.requests) != 3 {
		t.Errorf("chat calls = %d, want 3", len(client.requests))
	}
}

func TestRun_NudgeThenFinalize(t *testing.T) {
	// Model returns plain text twice -> one nudge, then finalize as failure.
	client := &scriptedClient{responses: []ChatResponse{
		{Message: Message{Role: "assistant", Content: "let me think"}, Usage: Usage{PromptTokens: 1, CompletionTokens: 1}},
		{Message: Message{Role: "assistant", Content: "the answer is 42"}, Usage: Usage{PromptTokens: 1, CompletionTokens: 1}},
	}}
	bridge := &recordingBridge{}

	res, err := Run(context.Background(), client, bridge, Native,
		"s", "u", nil, Opts{Model: "m", MaxIterations: 10})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Status == 0 {
		t.Error("expected nonzero Status when model never calls submit_result")
	}
	if res.Summary != "the answer is 42" {
		t.Errorf("Summary = %q, want the model's final content", res.Summary)
	}
	// The nudge must have been sent between the two turns.
	second := client.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "submit_result") {
		t.Errorf("expected nudge user message, got %+v", last)
	}
}

func TestRun_NudgeThenRecovers(t *testing.T) {
	// Text turn, nudge, then the model calls submit_result.
	client := &scriptedClient{responses: []ChatResponse{
		{Message: Message{Role: "assistant", Content: "thinking..."}},
		assistantToolCall("c1", "submit_result", map[string]any{"summary": "done", "status": float64(0)}),
	}}
	bridge := &recordingBridge{}

	res, err := Run(context.Background(), client, bridge, Native,
		"s", "u", nil, Opts{Model: "m"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.Status != 0 || res.Summary != "done" {
		t.Errorf("result = %+v", res)
	}
}

func TestRun_ArgsErrorFedBack(t *testing.T) {
	client := &scriptedClient{responses: []ChatResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "c1", Name: "run_command", ArgsError: "unexpected end of JSON input"},
		}}},
		assistantToolCall("c2", "submit_result", map[string]any{"summary": "ok", "status": float64(0)}),
	}}
	bridge := &recordingBridge{}

	res, err := Run(context.Background(), client, bridge, Native,
		"s", "u", nil, Opts{Model: "m"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.Status != 0 {
		t.Errorf("Status = %d", res.Status)
	}
	// The bridge must NOT have been called for the malformed tool call.
	for _, c := range bridge.calls {
		if c.ID == "c1" {
			t.Error("bridge was called for a tool call with ArgsError")
		}
	}
	second := client.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != "tool" || !strings.Contains(last.Content, "not valid JSON") {
		t.Errorf("expected args-error observation, got %+v", last)
	}
}

func TestRun_TransportErrorFailsRun(t *testing.T) {
	client := &scriptedClient{
		responses: []ChatResponse{{}},
		errs:      []error{errors.New("connection refused")},
	}
	bridge := &recordingBridge{}

	_, err := Run(context.Background(), client, bridge, Native,
		"s", "u", nil, Opts{Model: "m"})
	if err == nil {
		t.Fatal("expected error on transport failure")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %v", err)
	}
}

func TestRun_BridgeInternalErrorFailsRun(t *testing.T) {
	client := &scriptedClient{responses: []ChatResponse{
		assistantToolCall("c1", "run_command", map[string]any{"cmd": "ls"}),
	}}
	bridge := &failingBridge{}

	_, err := Run(context.Background(), client, bridge, Native,
		"s", "u", nil, Opts{Model: "m"})
	if err == nil {
		t.Fatal("expected error on bridge-internal failure")
	}
}

type failingBridge struct{ recordingBridge }

func (f *failingBridge) Execute(_ context.Context, _ ToolCall) (string, bool, *domain.LLMResult, error) {
	return "", false, nil, errors.New("bridge exploded")
}

func TestRun_DefaultMaxIterations(t *testing.T) {
	// Opts.MaxIterations <= 0 falls back to DefaultMaxIterations.
	responses := make([]ChatResponse, DefaultMaxIterations+5)
	for i := range responses {
		responses[i] = assistantToolCall("c", "run_command", map[string]any{"cmd": "ls"})
	}
	client := &scriptedClient{responses: responses}
	bridge := &recordingBridge{}

	_, err := Run(context.Background(), client, bridge, Native, "s", "u", nil, Opts{Model: "m"})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(client.requests) != DefaultMaxIterations {
		t.Errorf("chat calls = %d, want %d", len(client.requests), DefaultMaxIterations)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/agentloop/ -run TestRun_ -v`
Expected: FAIL (compile error: `Run` undefined)

- [ ] **Step 3: Implement the loop**

`internal/agentloop/loop.go`:

```go
package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// harnessPreamble is appended to the user prompt. It is deliberately minimal:
// persona and task remain the operator's domain (the rendered prompts are
// recorded on the Run verbatim; this preamble is part of loop mechanics).
const harnessPreamble = `You are working in an isolated workspace directory (your current working directory).
Available CLI tools on PATH: %s.
Work step by step using the provided tools to complete the task.
When finished, you MUST call the submit_result tool. Its "output" object is consumed by automation — include any fields the task asks for.`

const nudgeMessage = "You must finish by calling the submit_result tool. Use the provided tools to complete the task."

func preamble(requiredTools []string) string {
	tools := "(none declared)"
	if len(requiredTools) > 0 {
		tools = strings.Join(requiredTools, ", ")
	}
	return fmt.Sprintf(harnessPreamble, tools)
}

// Run drives the generic agent loop: send the conversation, decode tool calls,
// execute them through the bridge, feed observations back, and terminate when
// the model calls submit_result (or limits are hit).
//
// Failure semantics: model-level failures (max iterations, model never calls
// submit_result) return (LLMResult{Status: nonzero, ...}, nil) so the caller
// records the full result including the transcript. A non-nil error is
// reserved for transport errors, bridge-internal errors, and cancellation.
func Run(ctx context.Context, client ChatClient, bridge ToolBridge, proto ToolProtocol,
	systemPrompt, userPrompt string, requiredTools []string, opts Opts) (domain.LLMResult, error) {

	maxIter := opts.MaxIterations
	if maxIter <= 0 {
		maxIter = DefaultMaxIterations
	}

	specs := bridge.Specs()
	encodedTools := proto.Encode(specs)

	var transcript strings.Builder
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt + "\n\n" + preamble(requiredTools)},
	}
	for _, m := range messages {
		writeTranscript(&transcript, m)
	}

	tokens := 0
	nudged := false

	for i := 0; i < maxIter; i++ {
		resp, err := client.Chat(ctx, ChatRequest{
			Model:    opts.Model,
			Messages: messages,
			Tools:    encodedTools,
			Params:   opts.Params,
		})
		if err != nil {
			return domain.LLMResult{
				Status:     1,
				Summary:    "chat transport error: " + err.Error(),
				Output:     map[string]any{},
				Transcript: transcript.String(),
				Tokens:     tokens,
			}, fmt.Errorf("chat: %w", err)
		}
		tokens += resp.Usage.PromptTokens + resp.Usage.CompletionTokens
		writeTranscript(&transcript, resp.Message)

		calls := proto.Decode(resp)
		if len(calls) == 0 {
			if nudged {
				// The model was already told to call submit_result and still
				// produced plain text: finalize as a model-level failure,
				// preserving its last words and the transcript.
				return domain.LLMResult{
					Status:     1,
					Summary:    resp.Message.Content,
					Output:     map[string]any{},
					Transcript: transcript.String(),
					Tokens:     tokens,
				}, nil
			}
			nudged = true
			messages = append(messages, resp.Message)
			nudge := Message{Role: "user", Content: nudgeMessage}
			messages = append(messages, nudge)
			writeTranscript(&transcript, nudge)
			continue
		}

		messages = append(messages, resp.Message)
		for _, call := range calls {
			var obs string
			if call.ArgsError != "" {
				obs = "tool call arguments were not valid JSON: " + call.ArgsError
			} else {
				var done bool
				var result *domain.LLMResult
				var err error
				obs, done, result, err = bridge.Execute(ctx, call)
				if err != nil {
					return domain.LLMResult{
						Status:     1,
						Summary:    "tool bridge error: " + err.Error(),
						Output:     map[string]any{},
						Transcript: transcript.String(),
						Tokens:     tokens,
					}, fmt.Errorf("tool bridge: %w", err)
				}
				if done {
					result.Transcript = transcript.String()
					result.Tokens = tokens
					if result.Output == nil {
						result.Output = map[string]any{}
					}
					return *result, nil
				}
			}
			toolMsg := Message{Role: "tool", ToolCallID: call.ID, Content: obs}
			messages = append(messages, toolMsg)
			writeTranscript(&transcript, toolMsg)
		}
	}

	return domain.LLMResult{
		Status:     1,
		Summary:    fmt.Sprintf("max iterations (%d) reached without submit_result", maxIter),
		Output:     map[string]any{},
		Transcript: transcript.String(),
		Tokens:     tokens,
	}, nil
}

// writeTranscript appends one message as a JSON line.
func writeTranscript(sb *strings.Builder, m Message) {
	b, err := json.Marshal(m)
	if err != nil {
		sb.WriteString(fmt.Sprintf(`{"role":%q,"marshal_error":%q}`, m.Role, err.Error()))
	} else {
		sb.Write(b)
	}
	sb.WriteString("\n")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/agentloop/ -v`
Expected: PASS (all protocol + loop tests)

- [ ] **Step 5: Commit**

```bash
git add internal/agentloop/loop.go internal/agentloop/loop_test.go
git commit -m "feat(sp2): generic agent loop with submit_result termination"
```

---

### Task 3: the workspace tool bridge

**Files:**
- Create: `internal/agentloop/bridge/bridge.go`
- Test: `internal/agentloop/bridge/bridge_test.go`

- [ ] **Step 1: Write the failing bridge tests**

`internal/agentloop/bridge/bridge_test.go`:

```go
package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/agentloop"
	"github.com/cbarraford/office-fleet/internal/domain"
)

func newTestBridge(t *testing.T, limits Limits) (*Bridge, string) {
	t.Helper()
	ws := t.TempDir()
	return New(ws, limits), ws
}

// execTool runs one non-terminal tool call and fails the test on
// bridge-internal errors. (Named execTool, not exec, to avoid clashing with
// the os/exec import used by the implementation file in this package.)
func execTool(t *testing.T, b *Bridge, name string, args map[string]any) (string, bool, *domain.LLMResult) {
	t.Helper()
	obs, done, result, err := b.Execute(context.Background(), agentloop.ToolCall{ID: "t", Name: name, Args: args})
	if err != nil {
		t.Fatalf("Execute(%s) bridge-internal error: %v", name, err)
	}
	return obs, done, result
}

func TestSpecs_IncludesAllTools(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	specs := b.Specs()
	want := map[string]bool{"run_command": false, "read_file": false, "write_file": false, "list_dir": false, "submit_result": false}
	for _, s := range specs {
		if _, ok := want[s.Name]; !ok {
			t.Errorf("unexpected tool %q", s.Name)
		}
		want[s.Name] = true
		if s.Parameters == nil {
			t.Errorf("tool %q has nil Parameters schema", s.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestRunCommand_CwdIsWorkspace(t *testing.T) {
	b, ws := newTestBridge(t, Limits{})
	obs, done, _ := execTool(t, b, "run_command", map[string]any{"cmd": "pwd"})
	if done {
		t.Fatal("run_command must not terminate the loop")
	}
	// macOS tempdirs may be symlinked (/var -> /private/var); resolve before comparing.
	resolved, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(obs, resolved) && !strings.Contains(obs, ws) {
		t.Errorf("pwd observation %q does not contain workspace %q", obs, ws)
	}
	if !strings.Contains(obs, "exit code: 0") {
		t.Errorf("observation missing exit code: %q", obs)
	}
}

func TestRunCommand_NonzeroExit(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "exit 3"})
	if !strings.Contains(obs, "exit code: 3") {
		t.Errorf("observation = %q, want exit code: 3", obs)
	}
}

func TestRunCommand_Timeout(t *testing.T) {
	b, _ := newTestBridge(t, Limits{CommandTimeout: 50 * time.Millisecond})
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "sleep 2"})
	if !strings.Contains(obs, "timed out") {
		t.Errorf("observation = %q, want timeout notice", obs)
	}
}

func TestRunCommand_OutputTruncated(t *testing.T) {
	b, _ := newTestBridge(t, Limits{MaxOutputBytes: 100})
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "yes x | head -c 10000"})
	if len(obs) > 300 { // 100 bytes + exit-code line + truncation marker headroom
		t.Errorf("observation length = %d, want truncated to ~100 bytes", len(obs))
	}
	if !strings.Contains(obs, "[truncated]") {
		t.Errorf("observation missing truncation marker: %q", obs)
	}
}

func TestRunCommand_AllowlistDeny(t *testing.T) {
	b, _ := newTestBridge(t, Limits{CommandAllowlist: []string{"echo"}})
	obs, done, _ := execTool(t, b, "run_command", map[string]any{"cmd": "rm -rf /tmp/x"})
	if done {
		t.Fatal("denial must not terminate the loop")
	}
	if !strings.Contains(obs, "not in the allowlist") {
		t.Errorf("observation = %q, want allowlist denial", obs)
	}
}

func TestRunCommand_AllowlistAllow(t *testing.T) {
	b, _ := newTestBridge(t, Limits{CommandAllowlist: []string{"echo"}})
	obs, _, _ := execTool(t, b, "run_command", map[string]any{"cmd": "echo hello"})
	if !strings.Contains(obs, "hello") {
		t.Errorf("observation = %q, want command output", obs)
	}
}

func TestRunCommand_MissingCmd(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, _ := execTool(t, b, "run_command", map[string]any{})
	if done {
		t.Fatal("must not terminate")
	}
	if !strings.Contains(obs, "cmd") {
		t.Errorf("observation = %q, want missing-arg notice", obs)
	}
}

func TestReadWriteListFile(t *testing.T) {
	b, ws := newTestBridge(t, Limits{})

	obs, done, _ := execTool(t, b, "write_file", map[string]any{"path": "sub/note.txt", "content": "hello world"})
	if done || !strings.Contains(obs, "wrote") {
		t.Fatalf("write_file obs = %q done=%v", obs, done)
	}
	data, err := os.ReadFile(filepath.Join(ws, "sub", "note.txt"))
	if err != nil || string(data) != "hello world" {
		t.Fatalf("file content = %q err=%v", data, err)
	}

	obs, _, _ = execTool(t, b, "read_file", map[string]any{"path": "sub/note.txt"})
	if obs != "hello world" {
		t.Errorf("read_file obs = %q", obs)
	}

	obs, _, _ = execTool(t, b, "list_dir", map[string]any{"path": "sub"})
	if !strings.Contains(obs, "note.txt") {
		t.Errorf("list_dir obs = %q", obs)
	}

	// list_dir with no path defaults to workspace root.
	obs, _, _ = execTool(t, b, "list_dir", map[string]any{})
	if !strings.Contains(obs, "sub/") {
		t.Errorf("list_dir root obs = %q, want sub/ entry", obs)
	}
}

func TestReadFile_Missing(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, _ := execTool(t, b, "read_file", map[string]any{"path": "nope.txt"})
	if done {
		t.Fatal("must not terminate")
	}
	if !strings.Contains(obs, "no such file") && !strings.Contains(obs, "cannot read") {
		t.Errorf("observation = %q", obs)
	}
}

func TestPathEscapeDenied(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	for _, path := range []string{"../outside.txt", "/etc/passwd", "sub/../../outside"} {
		obs, done, _ := execTool(t, b, "read_file", map[string]any{"path": path})
		if done {
			t.Fatalf("path %q: must not terminate", path)
		}
		if !strings.Contains(obs, "escapes the workspace") {
			t.Errorf("path %q: observation = %q, want escape denial", path, obs)
		}
		obs, _, _ = execTool(t, b, "write_file", map[string]any{"path": path, "content": "x"})
		if !strings.Contains(obs, "escapes the workspace") {
			t.Errorf("write path %q: observation = %q", path, obs)
		}
	}
}

func TestSubmitResult(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, result, err := b.Execute(context.Background(), agentloop.ToolCall{
		ID: "t", Name: "submit_result",
		Args: map[string]any{
			"summary": "all done",
			"status":  float64(0),
			"output":  map[string]any{"review_body": "LGTM"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("submit_result must terminate the loop")
	}
	_ = obs
	if result.Summary != "all done" || result.Status != 0 {
		t.Errorf("result = %+v", result)
	}
	if result.Output["review_body"] != "LGTM" {
		t.Errorf("Output = %v", result.Output)
	}
}

func TestSubmitResult_NonzeroStatus(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	_, done, result, err := b.Execute(context.Background(), agentloop.ToolCall{
		ID: "t", Name: "submit_result",
		Args: map[string]any{"summary": "could not finish", "status": float64(2)},
	})
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if result.Status != 2 {
		t.Errorf("Status = %d, want 2", result.Status)
	}
	if result.Output == nil {
		t.Error("Output must default to an empty map")
	}
}

func TestSubmitResult_MissingSummary(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, result, err := b.Execute(context.Background(), agentloop.ToolCall{
		ID: "t", Name: "submit_result", Args: map[string]any{"status": float64(0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if done || result != nil {
		t.Fatal("submit_result without summary must NOT terminate")
	}
	if !strings.Contains(obs, "summary") {
		t.Errorf("observation = %q, want summary-required notice", obs)
	}
}

func TestUnknownTool(t *testing.T) {
	b, _ := newTestBridge(t, Limits{})
	obs, done, _ := execTool(t, b, "frobnicate", map[string]any{})
	if done {
		t.Fatal("must not terminate")
	}
	if !strings.Contains(obs, "unknown tool") {
		t.Errorf("observation = %q", obs)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/agentloop/bridge/ -v`
Expected: FAIL (compile error: package does not exist / `New` undefined)

- [ ] **Step 3: Implement the bridge**

`internal/agentloop/bridge/bridge.go`:

```go
// Package bridge implements the workspace ToolBridge for the generic agent
// loop: a sandboxed shell (cwd-anchored, time-limited, output-capped,
// optionally allowlisted) plus file tools and the submit_result terminator.
package bridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cbarraford/office-fleet/internal/agentloop"
	"github.com/cbarraford/office-fleet/internal/domain"
)

// Limits are the safety rails for tool execution. Zero values get defaults.
type Limits struct {
	CommandTimeout   time.Duration // per run_command; default 120s
	MaxOutputBytes   int           // observation truncation; default 64 KiB
	CommandAllowlist []string      // empty = allow all commands
}

const (
	DefaultCommandTimeout = 120 * time.Second
	DefaultMaxOutputBytes = 64 * 1024
)

// Bridge executes the whole-computer toolset inside one run's workspace.
type Bridge struct {
	workspace string
	limits    Limits
}

// New builds a Bridge for the given workspace, applying limit defaults.
func New(workspace string, limits Limits) *Bridge {
	if limits.CommandTimeout <= 0 {
		limits.CommandTimeout = DefaultCommandTimeout
	}
	if limits.MaxOutputBytes <= 0 {
		limits.MaxOutputBytes = DefaultMaxOutputBytes
	}
	return &Bridge{workspace: workspace, limits: limits}
}

func (b *Bridge) Specs() []agentloop.ToolSpec {
	return []agentloop.ToolSpec{
		{
			Name:        "run_command",
			Description: "Run a shell command in the workspace. Returns the exit code and combined stdout/stderr.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd": map[string]any{"type": "string", "description": "shell command to execute"},
				},
				"required": []string{"cmd"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read a file in the workspace and return its contents.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "file path, relative to the workspace"},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "write_file",
			Description: "Write (create or overwrite) a file in the workspace.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "file path, relative to the workspace"},
					"content": map[string]any{"type": "string", "description": "full file content"},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "list_dir",
			Description: "List a directory in the workspace. Directories have a trailing slash.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "directory path, relative to the workspace; defaults to the workspace root"},
				},
			},
		},
		{
			Name:        "submit_result",
			Description: "Finish the task and report the result. You MUST call this exactly once when done. The output object is consumed by automation.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{"type": "string", "description": "concise summary of what was done"},
					"status":  map[string]any{"type": "integer", "description": "0 on success, nonzero on failure"},
					"output":  map[string]any{"type": "object", "description": "structured result fields the task asked for"},
				},
				"required": []string{"summary", "status"},
			},
		},
	}
}

// Execute dispatches one tool call. Tool-level failures are observations
// (nil error); a non-nil error means the bridge itself is broken.
func (b *Bridge) Execute(ctx context.Context, call agentloop.ToolCall) (string, bool, *domain.LLMResult, error) {
	switch call.Name {
	case "run_command":
		return b.runCommand(ctx, call.Args), false, nil, nil
	case "read_file":
		return b.readFile(call.Args), false, nil, nil
	case "write_file":
		return b.writeFile(call.Args), false, nil, nil
	case "list_dir":
		return b.listDir(call.Args), false, nil, nil
	case "submit_result":
		return b.submitResult(call.Args)
	default:
		return fmt.Sprintf("unknown tool %q", call.Name), false, nil, nil
	}
}

func (b *Bridge) runCommand(ctx context.Context, args map[string]any) string {
	cmdStr, _ := args["cmd"].(string)
	if strings.TrimSpace(cmdStr) == "" {
		return "run_command requires a non-empty 'cmd' string argument"
	}
	if len(b.limits.CommandAllowlist) > 0 {
		first := strings.Fields(cmdStr)[0]
		base := filepath.Base(first)
		if !slices.Contains(b.limits.CommandAllowlist, base) {
			return fmt.Sprintf("command %q is not in the allowlist", base)
		}
	}

	cmdCtx, cancel := context.WithTimeout(ctx, b.limits.CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", cmdStr)
	cmd.Dir = b.workspace
	out, err := cmd.CombinedOutput()

	if cmdCtx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("command timed out after %s", b.limits.CommandTimeout)
	}
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return "command failed to start: " + err.Error()
		}
	}
	return fmt.Sprintf("exit code: %d\n%s", exitCode, b.truncate(string(out)))
}

func (b *Bridge) readFile(args map[string]any) string {
	path, _ := args["path"].(string)
	resolved, deny := b.resolve(path)
	if deny != "" {
		return deny
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "cannot read file: " + err.Error()
	}
	return b.truncate(string(data))
}

func (b *Bridge) writeFile(args map[string]any) string {
	path, _ := args["path"].(string)
	content, ok := args["content"].(string)
	if !ok {
		return "write_file requires a 'content' string argument"
	}
	resolved, deny := b.resolve(path)
	if deny != "" {
		return deny
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return "cannot create parent directory: " + err.Error()
	}
	if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
		return "cannot write file: " + err.Error()
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path)
}

func (b *Bridge) listDir(args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	resolved, deny := b.resolve(path)
	if deny != "" {
		return deny
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return "cannot list directory: " + err.Error()
	}
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(e.Name())
		if e.IsDir() {
			sb.WriteString("/")
		}
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		return "(empty directory)"
	}
	return b.truncate(sb.String())
}

func (b *Bridge) submitResult(args map[string]any) (string, bool, *domain.LLMResult, error) {
	summary, _ := args["summary"].(string)
	if strings.TrimSpace(summary) == "" {
		return "submit_result requires a non-empty 'summary' string argument", false, nil, nil
	}
	status := 0
	if v, ok := args["status"].(float64); ok {
		status = int(v)
	}
	output, _ := args["output"].(map[string]any)
	if output == nil {
		output = map[string]any{}
	}
	return "", true, &domain.LLMResult{Status: status, Summary: summary, Output: output}, nil
}

// resolve maps a tool path argument to an absolute path inside the workspace.
// The second return value is a non-empty denial observation on failure.
func (b *Bridge) resolve(raw string) (string, string) {
	if raw == "" {
		return "", "a non-empty 'path' argument is required"
	}
	p := raw
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.workspace, p)
	}
	wsAbs, err := filepath.Abs(b.workspace)
	if err != nil {
		return "", "cannot resolve workspace path: " + err.Error()
	}
	pAbs, err := filepath.Abs(p)
	if err != nil {
		return "", "cannot resolve path: " + err.Error()
	}
	if pAbs != wsAbs && !strings.HasPrefix(pAbs, wsAbs+string(filepath.Separator)) {
		return "", fmt.Sprintf("path %q escapes the workspace", raw)
	}
	return pAbs, ""
}

func (b *Bridge) truncate(s string) string {
	if len(s) <= b.limits.MaxOutputBytes {
		return s
	}
	return s[:b.limits.MaxOutputBytes] + "\n[truncated]"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/agentloop/bridge/ -v`
Expected: PASS. Note: `TestRunCommand_Timeout` takes ~50ms; the suite should finish in under 5s.

- [ ] **Step 5: Commit**

```bash
git add internal/agentloop/bridge/
git commit -m "feat(sp2): workspace tool bridge with safety limits"
```

---

### Task 4: the openai-compatible client

**Files:**
- Create: `internal/agentloop/openai/client.go`
- Test: `internal/agentloop/openai/client_test.go`

- [ ] **Step 1: Write the failing client tests**

`internal/agentloop/openai/client_test.go`:

```go
package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/agentloop"
)

// chatHandler captures the request body and returns a canned response.
func chatServer(t *testing.T, status int, respBody string, capture *map[string]any, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		if capture != nil {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			*capture = body
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
}

const toolCallResponse = `{
  "choices": [{"message": {
    "role": "assistant",
    "content": "",
    "tool_calls": [{"id": "call_abc", "type": "function",
      "function": {"name": "run_command", "arguments": "{\"cmd\":\"ls\"}"}}]
  }}],
  "usage": {"prompt_tokens": 12, "completion_tokens": 7}
}`

const textResponse = `{
  "choices": [{"message": {"role": "assistant", "content": "hello"}}],
  "usage": {"prompt_tokens": 3, "completion_tokens": 2}
}`

func TestChat_EncodeAndDecode(t *testing.T) {
	var captured map[string]any
	var auth string
	srv := chatServer(t, 200, toolCallResponse, &captured, &auth)
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "sk-test", RetryDelay: time.Millisecond}
	resp, err := c.Chat(context.Background(), agentloop.ChatRequest{
		Model: "llama3.1",
		Messages: []agentloop.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "do it"},
		},
		Tools:  []map[string]any{{"type": "function"}},
		Params: map[string]any{"num_ctx": 8192},
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}

	// Request encoding.
	if captured["model"] != "llama3.1" {
		t.Errorf("model = %v", captured["model"])
	}
	if captured["num_ctx"] != float64(8192) {
		t.Errorf("params passthrough num_ctx = %v", captured["num_ctx"])
	}
	if _, ok := captured["tools"]; !ok {
		t.Error("tools field missing from request")
	}
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", captured["messages"])
	}
	if auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", auth)
	}

	// Response decoding.
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Name != "run_command" {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.Args["cmd"] != "ls" {
		t.Errorf("args = %v", tc.Args)
	}
	if tc.ArgsError != "" {
		t.Errorf("unexpected ArgsError %q", tc.ArgsError)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestChat_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var auth string
	srv := chatServer(t, 200, textResponse, nil, &auth)
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", RetryDelay: time.Millisecond}
	if _, err := c.Chat(context.Background(), agentloop.ChatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Errorf("Authorization = %q, want empty", auth)
	}
}

func TestChat_MalformedToolArgs(t *testing.T) {
	bad := `{
	  "choices": [{"message": {"role": "assistant", "content": "",
	    "tool_calls": [{"id": "c1", "type": "function",
	      "function": {"name": "run_command", "arguments": "{not json"}}]}}],
	  "usage": {"prompt_tokens": 1, "completion_tokens": 1}
	}`
	srv := chatServer(t, 200, bad, nil, nil)
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", RetryDelay: time.Millisecond}
	resp, err := c.Chat(context.Background(), agentloop.ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ArgsError == "" {
		t.Error("expected ArgsError for malformed arguments JSON")
	}
}

func TestChat_RoleToolMessageEncoding(t *testing.T) {
	var captured map[string]any
	srv := chatServer(t, 200, textResponse, &captured, nil)
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", RetryDelay: time.Millisecond}
	_, err := c.Chat(context.Background(), agentloop.ChatRequest{
		Model: "m",
		Messages: []agentloop.Message{
			{Role: "assistant", ToolCalls: []agentloop.ToolCall{{ID: "c1", Name: "run_command", Args: map[string]any{"cmd": "ls"}}}},
			{Role: "tool", ToolCallID: "c1", Content: "exit code: 0"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := captured["messages"].([]any)
	asst := msgs[0].(map[string]any)
	tcs := asst["tool_calls"].([]any)
	tc0 := tcs[0].(map[string]any)
	if tc0["type"] != "function" {
		t.Errorf("tool_call type = %v", tc0["type"])
	}
	fn := tc0["function"].(map[string]any)
	if fn["name"] != "run_command" {
		t.Errorf("function name = %v", fn["name"])
	}
	// arguments must be a JSON-encoded STRING on the wire.
	argsStr, ok := fn["arguments"].(string)
	if !ok || !strings.Contains(argsStr, `"cmd"`) {
		t.Errorf("arguments = %v (%T), want JSON string", fn["arguments"], fn["arguments"])
	}
	toolMsg := msgs[1].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "c1" {
		t.Errorf("tool message = %v", toolMsg)
	}
}

func TestChat_Retry429ThenSuccess(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(textResponse))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", RetryDelay: time.Millisecond}
	resp, err := c.Chat(context.Background(), agentloop.ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Chat error after retries: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if resp.Message.Content != "hello" {
		t.Errorf("content = %q", resp.Message.Content)
	}
}

func TestChat_RetryExhausted(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(503)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", RetryDelay: time.Millisecond}
	_, err := c.Chat(context.Background(), agentloop.ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	if attempts != 3 { // initial + 2 retries
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestChat_NoRetryOn400(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", RetryDelay: time.Millisecond}
	_, err := c.Chat(context.Background(), agentloop.ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx other than 429)", attempts)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestChat_EmptyChoices(t *testing.T) {
	srv := chatServer(t, 200, `{"choices": [], "usage": {}}`, nil, nil)
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", RetryDelay: time.Millisecond}
	_, err := c.Chat(context.Background(), agentloop.ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected error on empty choices")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/agentloop/openai/ -v`
Expected: FAIL (compile error: `Client` undefined)

- [ ] **Step 3: Implement the client**

`internal/agentloop/openai/client.go`:

```go
// Package openai is the HTTP transport for openai-compatible chat/completions
// endpoints (Ollama, vLLM, llama.cpp, hosted APIs). Transport only — the agent
// loop lives in package agentloop.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cbarraford/office-fleet/internal/agentloop"
)

// Client speaks POST {BaseURL}/chat/completions.
type Client struct {
	BaseURL    string        // e.g. http://localhost:11434/v1 (no trailing slash)
	APIKey     string        // empty = no Authorization header
	HTTP       *http.Client  // nil = http.DefaultClient
	RetryDelay time.Duration // base backoff; default 1s (tests set ~1ms)
}

const maxRetries = 2 // retries after the initial attempt, on 429/5xx only

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message wireMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Client) Chat(ctx context.Context, req agentloop.ChatRequest) (agentloop.ChatResponse, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": encodeMessages(req.Messages),
	}
	if req.Tools != nil {
		body["tools"] = req.Tools
	}
	// Backend params passthrough; reserved keys are not overridable.
	for k, v := range req.Params {
		if k == "model" || k == "messages" || k == "tools" {
			continue
		}
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return agentloop.ChatResponse{}, fmt.Errorf("encode request: %w", err)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	retryDelay := c.RetryDelay
	if retryDelay <= 0 {
		retryDelay = time.Second
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return agentloop.ChatResponse{}, ctx.Err()
			case <-time.After(retryDelay * time.Duration(1<<(attempt-1))):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.BaseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return agentloop.ChatResponse{}, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if c.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
		}

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			return agentloop.ChatResponse{}, fmt.Errorf("chat request: %w", err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return agentloop.ChatResponse{}, fmt.Errorf("read response: %w", readErr)
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("chat endpoint returned %d: %s", resp.StatusCode, snippet(respBody))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return agentloop.ChatResponse{}, fmt.Errorf("chat endpoint returned %d: %s", resp.StatusCode, snippet(respBody))
		}
		return decodeResponse(respBody)
	}
	return agentloop.ChatResponse{}, fmt.Errorf("chat failed after %d attempts: %w", maxRetries+1, lastErr)
}

func encodeMessages(messages []agentloop.Message) []wireMessage {
	out := make([]wireMessage, len(messages))
	for i, m := range messages {
		wm := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			args, err := json.Marshal(tc.Args)
			if err != nil {
				args = []byte("{}")
			}
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireFunction{
					Name:      tc.Name,
					Arguments: string(args),
				},
			})
		}
		out[i] = wm
	}
	return out
}

func decodeResponse(body []byte) (agentloop.ChatResponse, error) {
	var wire wireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return agentloop.ChatResponse{}, fmt.Errorf("decode response: %w (body: %s)", err, snippet(body))
	}
	if len(wire.Choices) == 0 {
		return agentloop.ChatResponse{}, fmt.Errorf("chat response has no choices (body: %s)", snippet(body))
	}
	wm := wire.Choices[0].Message
	msg := agentloop.Message{Role: wm.Role, Content: wm.Content}
	for _, wtc := range wm.ToolCalls {
		tc := agentloop.ToolCall{ID: wtc.ID, Name: wtc.Function.Name}
		var args map[string]any
		if err := json.Unmarshal([]byte(wtc.Function.Arguments), &args); err != nil {
			tc.ArgsError = err.Error()
		} else {
			tc.Args = args
		}
		msg.ToolCalls = append(msg.ToolCalls, tc)
	}
	return agentloop.ChatResponse{
		Message: msg,
		Usage: agentloop.Usage{
			PromptTokens:     wire.Usage.PromptTokens,
			CompletionTokens: wire.Usage.CompletionTokens,
		},
	}, nil
}

// snippet bounds error-message bodies.
func snippet(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/agentloop/openai/ -v`
Expected: PASS (8 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/agentloop/openai/
git commit -m "feat(sp2): openai-compatible chat client with bounded retry"
```

---

### Task 5: config — new Backend fields + validation

**Files:**
- Modify: `internal/config/config.go` (Backend struct at lines 19–28; Validate backends loop at lines 109–138; ref checks at lines 140–204)
- Test: `internal/config/config_test.go` (append new tests)

- [ ] **Step 1: Write the failing validation tests** (append to `internal/config/config_test.go`)

```go
// --- SP2 validation tests ---

func validEndpointBackend() Backend {
	return Backend{
		Name:    "local-ollama",
		Kind:    "openai-compatible",
		BaseURI: "http://localhost:11434/v1",
		Model:   "llama3.1:70b",
		Auth:    BackendAuth{Mode: "none"},
	}
}

func validVoterConfig() *Config {
	return &Config{
		Backends: []Backend{
			{Name: "claude-sub", Kind: "claude", Auth: BackendAuth{Mode: "subscription"}},
			validEndpointBackend(),
			{Name: "panel-1", Kind: "voter", Strategy: "first_success", Panel: []string{"claude-sub", "local-ollama"}},
		},
	}
}

func errorsContain(t *testing.T, errs []error, substr string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return
		}
	}
	t.Errorf("expected a validation error containing %q, got %v", substr, errs)
}

func TestValidate_EndpointBackendValid(t *testing.T) {
	cfg := &Config{Backends: []Backend{validEndpointBackend()}}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_EndpointRequiresBaseURI(t *testing.T) {
	b := validEndpointBackend()
	b.BaseURI = ""
	errs := Validate(&Config{Backends: []Backend{b}})
	errorsContain(t, errs, "base_uri")
}

func TestValidate_EndpointRequiresModel(t *testing.T) {
	b := validEndpointBackend()
	b.Model = ""
	errs := Validate(&Config{Backends: []Backend{b}})
	errorsContain(t, errs, "model")
}

func TestValidate_EndpointRejectsSubscription(t *testing.T) {
	b := validEndpointBackend()
	b.Auth = BackendAuth{Mode: "subscription"}
	errs := Validate(&Config{Backends: []Backend{b}})
	errorsContain(t, errs, "subscription")
}

func TestValidate_EndpointBadCommandTimeout(t *testing.T) {
	b := validEndpointBackend()
	b.CommandTimeout = "two minutes"
	errs := Validate(&Config{Backends: []Backend{b}})
	errorsContain(t, errs, "command_timeout")
}

func TestValidate_VoterValid(t *testing.T) {
	if errs := Validate(validVoterConfig()); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_VoterBadStrategy(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends[2].Strategy = "consensus"
	errs := Validate(cfg)
	errorsContain(t, errs, "strategy")
}

func TestValidate_VoterEmptyPanel(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends[2].Panel = nil
	errs := Validate(cfg)
	errorsContain(t, errs, "panel")
}

func TestValidate_VoterUnknownMember(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends[2].Panel = []string{"claude-sub", "ghost"}
	errs := Validate(cfg)
	errorsContain(t, errs, "ghost")
}

func TestValidate_VoterNoNesting(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends = append(cfg.Backends, Backend{
		Name: "panel-2", Kind: "voter", Strategy: "majority", Panel: []string{"panel-1"},
	})
	errs := Validate(cfg)
	errorsContain(t, errs, "voter")
}

func TestValidate_VoterRejectsEndpointFields(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends[2].Model = "llama3.1"
	errs := Validate(cfg)
	errorsContain(t, errs, "must not set")
}

func TestValidate_VoterRefModelOverrideRejected(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Agents = []AgentConfig{
		{Name: "a1", DefaultBackend: domain.BackendRef{Name: "panel-1", Model: "llama3.1"}},
	}
	errs := Validate(cfg)
	errorsContain(t, errs, "override")
}

func TestValidate_VoterRefEffortOverrideRejected(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Duties = []DutyConfig{
		{Name: "d1", Backend: &domain.BackendRef{Name: "panel-1", Effort: "high"}},
	}
	errs := Validate(cfg)
	errorsContain(t, errs, "override")
}
```

Check the existing `config_test.go` first: ensure its import block has `strings` and `github.com/cbarraford/office-fleet/internal/domain` (add them if missing), and confirm none of the new helper names (`validEndpointBackend`, `validVoterConfig`, `errorsContain`) collide with existing helpers — rename the new ones if they do.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestValidate_Endpoint|TestValidate_Voter' -v`
Expected: FAIL (compile error: unknown fields `Strategy`, `Panel`, `CommandTimeout`)

- [ ] **Step 3: Add the Backend fields**

In `internal/config/config.go`, replace the `Backend` struct with:

```go
// Backend is a named, configured LLM provider instance.
type Backend struct {
	Name          string         `yaml:"name"`
	Kind          string         `yaml:"kind"` // claude | openai-compatible | voter
	Auth          BackendAuth    `yaml:"auth"`
	BaseURI       string         `yaml:"base_uri,omitempty"`
	Model         string         `yaml:"model,omitempty"`
	DefaultEffort string         `yaml:"default_effort,omitempty"`
	Params        map[string]any `yaml:"params,omitempty"`

	// Generic agent loop limits (openai-compatible only).
	MaxIterations    int      `yaml:"max_iterations,omitempty"`    // default 25
	CommandTimeout   string   `yaml:"command_timeout,omitempty"`   // Go duration, e.g. "120s"; default 120s
	MaxOutputBytes   int      `yaml:"max_output_bytes,omitempty"`  // default 65536
	CommandAllowlist []string `yaml:"command_allowlist,omitempty"` // empty = allow all

	// Voter (kind: voter only).
	Strategy string   `yaml:"strategy,omitempty"` // first_success | majority
	Panel    []string `yaml:"panel,omitempty"`    // names of non-voter backends
}
```

- [ ] **Step 4: Add the validation rules**

In `Validate` (`internal/config/config.go`), make these changes:

(a) Change `backendNames := map[string]bool{}` to also track kinds — replace the line with:

```go
	backendNames := map[string]bool{}
	backendKind := map[string]string{}
```

and inside the backends loop, after `backendNames[b.Name] = true`, add:

```go
		backendKind[b.Name] = b.Kind
```

(b) Replace the kind switch:

```go
		switch b.Kind {
		case "claude", "openai-compatible", "voter":
		default:
			errs = append(errs, fmt.Errorf("backend %q: unknown kind %q", b.Name, b.Kind))
		}
```

(c) After the existing auth-mode checks (after the `api_key requires api_key` error, still inside the backends loop), add the per-kind rules:

```go
		switch b.Kind {
		case "openai-compatible":
			if b.BaseURI == "" {
				errs = append(errs, fmt.Errorf("backend %q: kind openai-compatible requires base_uri", b.Name))
			}
			if b.Model == "" {
				errs = append(errs, fmt.Errorf("backend %q: kind openai-compatible requires model", b.Name))
			}
			if b.Auth.Mode == "subscription" {
				errs = append(errs, fmt.Errorf("backend %q: subscription auth is not supported for endpoint backends (use api_key or none)", b.Name))
			}
		case "voter":
			if b.Strategy != "first_success" && b.Strategy != "majority" {
				errs = append(errs, fmt.Errorf("backend %q: voter strategy must be first_success or majority, got %q", b.Name, b.Strategy))
			}
			if len(b.Panel) == 0 {
				errs = append(errs, fmt.Errorf("backend %q: voter requires a non-empty panel", b.Name))
			}
			if b.BaseURI != "" || b.Model != "" || b.MaxIterations != 0 ||
				b.CommandTimeout != "" || b.MaxOutputBytes != 0 || len(b.CommandAllowlist) != 0 {
				errs = append(errs, fmt.Errorf("backend %q: voter backends must not set base_uri/model/loop-limit fields", b.Name))
			}
		}
		if b.CommandTimeout != "" {
			if _, err := time.ParseDuration(b.CommandTimeout); err != nil {
				errs = append(errs, fmt.Errorf("backend %q: invalid command_timeout %q: %v", b.Name, b.CommandTimeout, err))
			}
		}
```

Add `"time"` to the config.go import block.

(d) After the backends loop (before the agents loop), add panel-member validation as a second pass (panel members may be declared after the voter):

```go
	// Voter panels: every member must be a defined, non-voter backend.
	for i := range cfg.Backends {
		b := &cfg.Backends[i]
		if b.Kind != "voter" {
			continue
		}
		for _, member := range b.Panel {
			if !backendNames[member] {
				errs = append(errs, fmt.Errorf("backend %q: panel member %q not defined", b.Name, member))
				continue
			}
			if backendKind[member] == "voter" {
				errs = append(errs, fmt.Errorf("backend %q: panel member %q is a voter; voter nesting is not supported", b.Name, member))
			}
		}
	}

	// rejectVoterOverride flags model/effort overrides on refs that point at voters.
	rejectVoterOverride := func(where string, ref domain.BackendRef) {
		if ref.Name == "" || backendKind[ref.Name] != "voter" {
			return
		}
		if ref.Model != "" || ref.Effort != "" {
			errs = append(errs, fmt.Errorf("%s: model/effort override on voter backend %q is not supported (each panel member resolves its own)", where, ref.Name))
		}
	}
```

(e) Wire `rejectVoterOverride` into the three ref sites:
- In the agents loop, after the existing `default_backend not defined` check, add:
  ```go
		rejectVoterOverride(fmt.Sprintf("agent %q", a.Name), a.DefaultBackend)
  ```
- In the duties loop, after the `duty backend not defined` check, add:
  ```go
		if d.Backend != nil {
			rejectVoterOverride(fmt.Sprintf("duty %q", d.Name), *d.Backend)
		}
  ```
  (merge with the existing `d.Backend != nil` check if convenient).
- In the assignments loop, after the `assignment backend not defined` check, add:
  ```go
		if a.Backend != nil {
			rejectVoterOverride(fmt.Sprintf("assignment[%d]", i), *a.Backend)
		}
  ```

Note: `rejectVoterOverride` must be declared before the agents loop (it is — step (d) places it right after the backends loop).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — all new tests AND all pre-existing config tests (no regressions).

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(sp2): endpoint and voter backend config validation"
```

---

### Task 6: EndpointExecutor + factory (claude / openai-compatible)

**Files:**
- Create: `internal/executor/endpoint.go`
- Create: `internal/executor/factory.go`
- Test: `internal/executor/endpoint_test.go`, `internal/executor/factory_test.go`

- [ ] **Step 1: Write the failing EndpointExecutor test**

`internal/executor/endpoint_test.go`:

```go
package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
)

// scriptedChatHandler walks through canned responses, one per request.
func scriptedChatHandler(t *testing.T, responses []string) http.HandlerFunc {
	t.Helper()
	call := 0
	return func(w http.ResponseWriter, r *http.Request) {
		if call >= len(responses) {
			t.Errorf("unexpected extra chat call #%d", call+1)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[call]))
		call++
	}
}

func toolCallTurn(id, name, argsJSON string) string {
	b, _ := json.Marshal(argsJSON)
	return `{
	  "choices": [{"message": {"role": "assistant", "content": "",
	    "tool_calls": [{"id": "` + id + `", "type": "function",
	      "function": {"name": "` + name + `", "arguments": ` + string(b) + `}}]}}],
	  "usage": {"prompt_tokens": 10, "completion_tokens": 5}
	}`
}

func TestEndpointExecutor_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(scriptedChatHandler(t, []string{
		toolCallTurn("c1", "run_command", `{"cmd":"echo workfile > out.txt"}`),
		toolCallTurn("c2", "read_file", `{"path":"out.txt"}`),
		toolCallTurn("c3", "submit_result", `{"summary":"reviewed","status":0,"output":{"review_body":"LGTM"}}`),
	}))
	defer srv.Close()

	exec, err := NewEndpointExecutor(&config.Backend{
		Name:    "test-ep",
		Kind:    "openai-compatible",
		BaseURI: srv.URL,
		Model:   "test-model",
		Auth:    config.BackendAuth{Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exec.Kind() != "openai-compatible" {
		t.Errorf("Kind = %q", exec.Kind())
	}

	ws := t.TempDir()
	result, err := exec.Run(context.Background(), LLMRequest{
		SystemPrompt: "you are a reviewer",
		Prompt:       "review the thing",
		Workspace:    ws,
		Tools:        []string{},
		Model:        "test-model",
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != 0 {
		t.Errorf("Status = %d", result.Status)
	}
	if result.Summary != "reviewed" {
		t.Errorf("Summary = %q", result.Summary)
	}
	if result.Output["review_body"] != "LGTM" {
		t.Errorf("Output = %v", result.Output)
	}
	if result.Tokens != 45 { // 3 turns * 15
		t.Errorf("Tokens = %d, want 45", result.Tokens)
	}
	if !strings.Contains(result.Transcript, "run_command") {
		t.Error("transcript missing tool activity")
	}
}

func TestEndpointExecutor_TransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	exec, err := NewEndpointExecutor(&config.Backend{
		Name: "bad", Kind: "openai-compatible", BaseURI: srv.URL, Model: "m",
		Auth: config.BackendAuth{Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Speed up the retry backoff for the test.
	exec.RetryDelay = 1 // 1ns

	_, err = exec.Run(context.Background(), LLMRequest{Prompt: "x", Workspace: t.TempDir(), Model: "m"})
	if err == nil {
		t.Fatal("expected error on persistent 503")
	}
}

func TestNewEndpointExecutor_BadTimeout(t *testing.T) {
	_, err := NewEndpointExecutor(&config.Backend{
		Name: "bad", Kind: "openai-compatible", BaseURI: "http://x", Model: "m",
		CommandTimeout: "garbage",
	})
	if err == nil {
		t.Fatal("expected error for unparseable command_timeout")
	}
}
```

- [ ] **Step 2: Write the failing factory test**

`internal/executor/factory_test.go`:

```go
package executor

import (
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
)

func TestFromBackend_Claude(t *testing.T) {
	cfg := &config.Config{}
	exec, err := FromBackend(cfg, &config.Backend{
		Name: "c", Kind: "claude",
		Auth: config.BackendAuth{Mode: "api_key", APIKey: "sk-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ce, ok := exec.(*ClaudeExecutor)
	if !ok {
		t.Fatalf("got %T, want *ClaudeExecutor", exec)
	}
	if ce.APIKey != "sk-x" {
		t.Errorf("APIKey = %q", ce.APIKey)
	}
}

func TestFromBackend_ClaudeSubscriptionNoKey(t *testing.T) {
	exec, err := FromBackend(&config.Config{}, &config.Backend{
		Name: "c", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exec.(*ClaudeExecutor).APIKey != "" {
		t.Error("subscription backend must not set an API key")
	}
}

func TestFromBackend_Endpoint(t *testing.T) {
	exec, err := FromBackend(&config.Config{}, &config.Backend{
		Name: "e", Kind: "openai-compatible", BaseURI: "http://localhost:11434/v1",
		Model: "llama3.1", Auth: config.BackendAuth{Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exec.(*EndpointExecutor); !ok {
		t.Fatalf("got %T, want *EndpointExecutor", exec)
	}
}

func TestFromBackend_UnknownKind(t *testing.T) {
	_, err := FromBackend(&config.Config{}, &config.Backend{Name: "x", Kind: "carrier-pigeon"})
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("err = %v", err)
	}
}
```

(Voter factory tests are added in Task 7.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/executor/ -run 'TestEndpointExecutor|TestNewEndpointExecutor|TestFromBackend' -v`
Expected: FAIL (compile errors: `NewEndpointExecutor`, `FromBackend` undefined)

- [ ] **Step 4: Implement EndpointExecutor**

`internal/executor/endpoint.go`:

```go
package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/cbarraford/office-fleet/internal/agentloop"
	"github.com/cbarraford/office-fleet/internal/agentloop/bridge"
	"github.com/cbarraford/office-fleet/internal/agentloop/openai"
	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
)

// EndpointExecutor drives an openai-compatible chat endpoint through
// OfficeFleet's generic agent loop. Cost is always 0 in SP2 (no price table).
type EndpointExecutor struct {
	BaseURI       string
	APIKey        string // empty for auth mode "none"
	Params        map[string]any
	MaxIterations int
	Limits        bridge.Limits
	RetryDelay    time.Duration // 0 = transport default (1s); tests shrink it

	// client overrides the transport for tests; nil builds an openai.Client.
	client agentloop.ChatClient
}

// NewEndpointExecutor builds an EndpointExecutor from a validated backend config.
func NewEndpointExecutor(b *config.Backend) (*EndpointExecutor, error) {
	limits := bridge.Limits{
		MaxOutputBytes:   b.MaxOutputBytes,
		CommandAllowlist: b.CommandAllowlist,
	}
	if b.CommandTimeout != "" {
		d, err := time.ParseDuration(b.CommandTimeout)
		if err != nil {
			return nil, fmt.Errorf("backend %q: invalid command_timeout %q: %w", b.Name, b.CommandTimeout, err)
		}
		limits.CommandTimeout = d
	}
	var apiKey string
	if b.Auth.Mode == "api_key" {
		apiKey = b.Auth.APIKey
	}
	return &EndpointExecutor{
		BaseURI:       b.BaseURI,
		APIKey:        apiKey,
		Params:        b.Params,
		MaxIterations: b.MaxIterations,
		Limits:        limits,
	}, nil
}

func (e *EndpointExecutor) Kind() string { return "openai-compatible" }

// Run executes one agent-loop run in the request's workspace.
// req.Effort is accepted but unused: raw chat APIs have no portable effort
// semantics in SP2 (see the spec's open questions).
func (e *EndpointExecutor) Run(ctx context.Context, req LLMRequest) (domain.LLMResult, error) {
	client := e.client
	if client == nil {
		client = &openai.Client{BaseURL: e.BaseURI, APIKey: e.APIKey, RetryDelay: e.RetryDelay}
	}
	br := bridge.New(req.Workspace, e.Limits)
	return agentloop.Run(ctx, client, br, agentloop.Native,
		req.SystemPrompt, req.Prompt, req.Tools,
		agentloop.Opts{Model: req.Model, Params: e.Params, MaxIterations: e.MaxIterations})
}
```

- [ ] **Step 5: Implement the factory (voter case lands in Task 7)**

`internal/executor/factory.go`:

```go
package executor

import (
	"fmt"

	"github.com/cbarraford/office-fleet/internal/config"
)

// FromBackend builds the Executor for a resolved, validated backend.
// This is the single dispatch point the CLI uses; voter panels recurse.
func FromBackend(cfg *config.Config, b *config.Backend) (Executor, error) {
	switch b.Kind {
	case "claude":
		var apiKey string
		if b.Auth.Mode == "api_key" {
			apiKey = b.Auth.APIKey
		}
		return NewClaudeExecutor(apiKey), nil
	case "openai-compatible":
		return NewEndpointExecutor(b)
	default:
		return nil, fmt.Errorf("unsupported backend kind %q", b.Kind)
	}
}

// findBackend returns the named backend from config, or nil.
func findBackend(cfg *config.Config, name string) *config.Backend {
	for i := range cfg.Backends {
		if cfg.Backends[i].Name == name {
			return &cfg.Backends[i]
		}
	}
	return nil
}
```

(`findBackend` is used by the voter case in Task 7; Go tolerates it being briefly unused only if referenced — if `go vet` complains about the unused function, that's fine, it's not an error; the compiler does not reject unused package-level funcs.)

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/executor/ -v`
Expected: PASS — new tests AND all pre-existing executor tests (fake, claude parse).

- [ ] **Step 7: Commit**

```bash
git add internal/executor/endpoint.go internal/executor/factory.go internal/executor/endpoint_test.go internal/executor/factory_test.go
git commit -m "feat(sp2): EndpointExecutor and executor factory"
```

---

### Task 7: VotingExecutor

**Files:**
- Create: `internal/executor/voter.go`
- Modify: `internal/executor/factory.go` (add the voter case)
- Test: `internal/executor/voter_test.go`; extend `internal/executor/factory_test.go`

- [ ] **Step 1: Write the failing voter tests**

`internal/executor/voter_test.go`:

```go
package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// slowFake is a FakeExecutor with a delay and cancellation awareness.
type slowFake struct {
	result    domain.LLMResult
	err       error
	delay     time.Duration
	gotReq    LLMRequest
	cancelled bool
}

func (s *slowFake) Kind() string { return "fake" }

func (s *slowFake) Run(ctx context.Context, req LLMRequest) (domain.LLMResult, error) {
	s.gotReq = req
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		s.cancelled = true
		return domain.LLMResult{}, ctx.Err()
	}
	return s.result, s.err
}

func TestVoter_FirstSuccess_CompletionOrder(t *testing.T) {
	slow := &slowFake{result: domain.LLMResult{Status: 0, Summary: "slow-winner"}, delay: 500 * time.Millisecond}
	fast := &slowFake{result: domain.LLMResult{Status: 0, Summary: "fast-winner"}, delay: 5 * time.Millisecond}
	v := &VotingExecutor{
		Strategy: "first_success",
		Panel: []PanelMember{
			{Name: "slow", Exec: slow, Model: "m-slow"},
			{Name: "fast", Exec: fast, Model: "m-fast"},
		},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "fast-winner" {
		t.Errorf("Summary = %q, want fast-winner (completion order)", res.Summary)
	}
}

func TestVoter_FirstSuccess_SkipsFailures(t *testing.T) {
	failing := &slowFake{result: domain.LLMResult{Status: 1, Summary: "failed"}, delay: time.Millisecond}
	ok := &slowFake{result: domain.LLMResult{Status: 0, Summary: "ok"}, delay: 20 * time.Millisecond}
	v := &VotingExecutor{
		Strategy: "first_success",
		Panel:    []PanelMember{{Name: "f", Exec: failing}, {Name: "ok", Exec: ok}},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "ok" {
		t.Errorf("Summary = %q, want ok", res.Summary)
	}
}

func TestVoter_FirstSuccess_AllFail(t *testing.T) {
	f1 := &slowFake{result: domain.LLMResult{Status: 1, Summary: "f1"}, delay: time.Millisecond}
	f2 := &slowFake{result: domain.LLMResult{Status: 2, Summary: "f2"}, delay: 5 * time.Millisecond}
	v := &VotingExecutor{
		Strategy: "first_success",
		Panel:    []PanelMember{{Name: "f1", Exec: f1}, {Name: "f2", Exec: f2}},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal("all-failed (non-error) panel should not return an error; pipeline handles Status != 0")
	}
	if res.Status == 0 {
		t.Error("expected nonzero status when no member succeeds")
	}
}

func TestVoter_AllErrored(t *testing.T) {
	e1 := &slowFake{err: errors.New("boom1"), delay: time.Millisecond}
	e2 := &slowFake{err: errors.New("boom2"), delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "first_success",
		Panel:    []PanelMember{{Name: "e1", Exec: e1}, {Name: "e2", Exec: e2}},
	}
	_, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err == nil {
		t.Fatal("expected an error when every panel member errors")
	}
}

func TestVoter_Majority_PluralityOnStatus(t *testing.T) {
	a := &slowFake{result: domain.LLMResult{Status: 0, Summary: "a", Tokens: 10, Cost: 0.1}, delay: 30 * time.Millisecond}
	b := &slowFake{result: domain.LLMResult{Status: 1, Summary: "b", Tokens: 20, Cost: 0.2}, delay: time.Millisecond}
	c := &slowFake{result: domain.LLMResult{Status: 0, Summary: "c", Tokens: 30, Cost: 0.3}, delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "majority",
		Panel:    []PanelMember{{Name: "a", Exec: a}, {Name: "b", Exec: b}, {Name: "c", Exec: c}},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	// Status 0 wins 2-1; representative = lowest panel index in group = "a".
	if res.Summary != "a" {
		t.Errorf("Summary = %q, want a (lowest panel index in winning group)", res.Summary)
	}
	if res.Status != 0 {
		t.Errorf("Status = %d", res.Status)
	}
	// Tokens/cost summed across ALL members.
	if res.Tokens != 60 {
		t.Errorf("Tokens = %d, want 60", res.Tokens)
	}
	if res.Cost < 0.59 || res.Cost > 0.61 {
		t.Errorf("Cost = %v, want 0.6", res.Cost)
	}
}

func TestVoter_Majority_TieLowestPanelIndexGroup(t *testing.T) {
	a := &slowFake{result: domain.LLMResult{Status: 1, Summary: "a"}, delay: time.Millisecond}
	b := &slowFake{result: domain.LLMResult{Status: 0, Summary: "b"}, delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "majority",
		Panel:    []PanelMember{{Name: "a", Exec: a}, {Name: "b", Exec: b}},
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	// 1-1 tie; group containing panel index 0 (status 1) wins.
	if res.Summary != "a" {
		t.Errorf("Summary = %q, want a (tie -> lowest panel index group)", res.Summary)
	}
}

func TestVoter_MemberModelEffortOverride(t *testing.T) {
	m1 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "x"}, delay: time.Millisecond}
	m2 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "y"}, delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "majority",
		Panel: []PanelMember{
			{Name: "m1", Exec: m1, Model: "model-one", Effort: "low"},
			{Name: "m2", Exec: m2, Model: "model-two", Effort: "high"},
		},
	}
	if _, err := v.Run(context.Background(), LLMRequest{Prompt: "p", Model: "voter-level-model"}); err != nil {
		t.Fatal(err)
	}
	if m1.gotReq.Model != "model-one" || m1.gotReq.Effort != "low" {
		t.Errorf("m1 req = model %q effort %q", m1.gotReq.Model, m1.gotReq.Effort)
	}
	if m2.gotReq.Model != "model-two" || m2.gotReq.Effort != "high" {
		t.Errorf("m2 req = model %q effort %q", m2.gotReq.Model, m2.gotReq.Effort)
	}
}

func TestVoter_WorkspaceSubdirs(t *testing.T) {
	m1 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "x"}, delay: time.Millisecond}
	m2 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "y"}, delay: time.Millisecond}
	v := &VotingExecutor{
		Strategy: "majority",
		Panel:    []PanelMember{{Name: "m1", Exec: m1}, {Name: "m2", Exec: m2}},
	}
	ws := t.TempDir()
	if _, err := v.Run(context.Background(), LLMRequest{Prompt: "p", Workspace: ws}); err != nil {
		t.Fatal(err)
	}
	if m1.gotReq.Workspace != filepath.Join(ws, "panel-0") {
		t.Errorf("m1 workspace = %q", m1.gotReq.Workspace)
	}
	if m2.gotReq.Workspace != filepath.Join(ws, "panel-1") {
		t.Errorf("m2 workspace = %q", m2.gotReq.Workspace)
	}
	for _, sub := range []string{"panel-0", "panel-1"} {
		if fi, err := os.Stat(filepath.Join(ws, sub)); err != nil || !fi.IsDir() {
			t.Errorf("workspace subdir %s missing: %v", sub, err)
		}
	}
}

func TestVoter_KindAndTranscriptPrefix(t *testing.T) {
	m1 := &slowFake{result: domain.LLMResult{Status: 0, Summary: "x", Transcript: "INNER", Tokens: 5}, delay: time.Millisecond}
	v := &VotingExecutor{Strategy: "majority", Panel: []PanelMember{{Name: "m1", Exec: m1}}}
	if v.Kind() != "voter" {
		t.Errorf("Kind = %q", v.Kind())
	}
	res, err := v.Run(context.Background(), LLMRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Transcript, "m1") || !strings.Contains(res.Transcript, "INNER") {
		t.Errorf("Transcript = %q, want panel summary + representative transcript", res.Transcript)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/executor/ -run TestVoter -v`
Expected: FAIL (compile error: `VotingExecutor`, `PanelMember` undefined)

- [ ] **Step 3: Implement the voter**

`internal/executor/voter.go`:

```go
package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// PanelMember is one voter panel entry. Model/Effort are captured from the
// member's own backend config at factory time, because the voter backend has
// no model of its own and the pipeline resolves req.Model from the voter.
type PanelMember struct {
	Name   string
	Exec   Executor
	Model  string
	Effort string
}

// VotingExecutor fans one LLMRequest out to a panel of executors and
// aggregates by strategy. Minimal voter (SP2): majority votes on the integer
// Status code; semantic/judge strategies are deferred.
type VotingExecutor struct {
	Panel    []PanelMember
	Strategy string // first_success | majority
}

func NewVotingExecutor(panel []PanelMember, strategy string) *VotingExecutor {
	return &VotingExecutor{Panel: panel, Strategy: strategy}
}

func (v *VotingExecutor) Kind() string { return "voter" }

type memberOutcome struct {
	idx    int
	result domain.LLMResult
	hadErr bool
}

func (v *VotingExecutor) Run(ctx context.Context, req LLMRequest) (domain.LLMResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan memberOutcome, len(v.Panel))
	for i, m := range v.Panel {
		mreq := req
		mreq.Model = m.Model
		mreq.Effort = m.Effort
		if req.Workspace != "" {
			ws := filepath.Join(req.Workspace, fmt.Sprintf("panel-%d", i))
			if err := os.MkdirAll(ws, 0o755); err != nil {
				return domain.LLMResult{}, fmt.Errorf("create panel workspace: %w", err)
			}
			mreq.Workspace = ws
		}
		go func(i int, m PanelMember, mreq LLMRequest) {
			res, err := m.Exec.Run(runCtx, mreq)
			if err != nil {
				// Synthesize a failure result so aggregation is uniform.
				res = domain.LLMResult{
					Status:  1,
					Summary: fmt.Sprintf("panel member %s: %v", m.Name, err),
					Output:  map[string]any{},
				}
			}
			ch <- memberOutcome{idx: i, result: res, hadErr: err != nil}
		}(i, m, mreq)
	}

	switch v.Strategy {
	case "first_success":
		return v.firstSuccess(ch)
	default: // "majority" — config validation guarantees the strategy is valid
		return v.majority(ch)
	}
}

// firstSuccess returns the first completed result with Status == 0, cancelling
// the rest. Tokens/cost are summed over results received up to and including
// the winner (cancelled members never report). If no member succeeds, the
// last-completed failure is returned; if every member also errored, an error.
func (v *VotingExecutor) firstSuccess(ch <-chan memberOutcome) (domain.LLMResult, error) {
	tokens, cost := 0, 0.0
	var last memberOutcome
	allErrored := true
	for range v.Panel {
		out := <-ch
		tokens += out.result.Tokens
		cost += out.result.Cost
		last = out
		if !out.hadErr {
			allErrored = false
		}
		if !out.hadErr && out.result.Status == 0 {
			win := out.result
			win.Tokens = tokens
			win.Cost = cost
			return win, nil
		}
	}
	res := last.result
	res.Tokens = tokens
	res.Cost = cost
	if allErrored {
		return res, fmt.Errorf("all %d panel members failed", len(v.Panel))
	}
	return res, nil
}

// majority waits for all members, then takes a plurality vote on Status. Ties
// go to the group containing the lowest panel index; the representative is the
// lowest panel index within the winning group. Tokens/cost sum over all members.
func (v *VotingExecutor) majority(ch <-chan memberOutcome) (domain.LLMResult, error) {
	outcomes := make([]memberOutcome, 0, len(v.Panel))
	tokens, cost := 0, 0.0
	allErrored := true
	for range v.Panel {
		out := <-ch
		outcomes = append(outcomes, out)
		tokens += out.result.Tokens
		cost += out.result.Cost
		if !out.hadErr {
			allErrored = false
		}
	}

	// Group by status: count and lowest panel index per group.
	counts := map[int]int{}
	lowestIdx := map[int]int{}
	for _, out := range outcomes {
		s := out.result.Status
		counts[s]++
		if cur, ok := lowestIdx[s]; !ok || out.idx < cur {
			lowestIdx[s] = out.idx
		}
	}
	winStatus, winCount, winLowest := 0, -1, len(v.Panel)
	for s, n := range counts {
		if n > winCount || (n == winCount && lowestIdx[s] < winLowest) {
			winStatus, winCount, winLowest = s, n, lowestIdx[s]
		}
	}

	// Representative: lowest panel index within the winning group.
	var rep memberOutcome
	repIdx := len(v.Panel)
	for _, out := range outcomes {
		if out.result.Status == winStatus && out.idx < repIdx {
			rep = out
			repIdx = out.idx
		}
	}

	res := rep.result
	res.Tokens = tokens
	res.Cost = cost
	res.Transcript = v.panelSummary(outcomes) + res.Transcript
	if allErrored {
		return res, fmt.Errorf("all %d panel members failed", len(v.Panel))
	}
	return res, nil
}

// panelSummary renders one line per member: name -> status/tokens.
func (v *VotingExecutor) panelSummary(outcomes []memberOutcome) string {
	lines := make([]string, len(v.Panel))
	for _, out := range outcomes {
		lines[out.idx] = fmt.Sprintf("panel %s: status=%d tokens=%d", v.Panel[out.idx].Name, out.result.Status, out.result.Tokens)
	}
	return "=== voter panel ===\n" + strings.Join(lines, "\n") + "\n=== representative transcript ===\n"
}
```

- [ ] **Step 4: Add the voter case to the factory**

In `internal/executor/factory.go`, add this case to the `switch` in `FromBackend` (before `default`):

```go
	case "voter":
		panel := make([]PanelMember, 0, len(b.Panel))
		for _, name := range b.Panel {
			mb := findBackend(cfg, name)
			if mb == nil {
				return nil, fmt.Errorf("voter %q: panel member %q not found in config", b.Name, name)
			}
			member, err := FromBackend(cfg, mb)
			if err != nil {
				return nil, fmt.Errorf("voter %q: panel member %q: %w", b.Name, name, err)
			}
			panel = append(panel, PanelMember{
				Name:   mb.Name,
				Exec:   member,
				Model:  mb.Model,
				Effort: mb.DefaultEffort,
			})
		}
		return NewVotingExecutor(panel, b.Strategy), nil
```

- [ ] **Step 5: Extend the factory test** (append to `internal/executor/factory_test.go`)

```go
func TestFromBackend_Voter(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "c", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}, Model: "claude-x", DefaultEffort: "high"},
			{Name: "e", Kind: "openai-compatible", BaseURI: "http://x/v1", Model: "llama3.1", Auth: config.BackendAuth{Mode: "none"}},
		},
	}
	exec, err := FromBackend(cfg, &config.Backend{
		Name: "panel", Kind: "voter", Strategy: "majority", Panel: []string{"c", "e"},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := exec.(*VotingExecutor)
	if !ok {
		t.Fatalf("got %T", exec)
	}
	if len(v.Panel) != 2 {
		t.Fatalf("panel size = %d", len(v.Panel))
	}
	if v.Panel[0].Model != "claude-x" || v.Panel[0].Effort != "high" {
		t.Errorf("member 0 model/effort = %q/%q", v.Panel[0].Model, v.Panel[0].Effort)
	}
	if v.Panel[1].Model != "llama3.1" {
		t.Errorf("member 1 model = %q", v.Panel[1].Model)
	}
}

func TestFromBackend_VoterUnknownMember(t *testing.T) {
	_, err := FromBackend(&config.Config{}, &config.Backend{
		Name: "panel", Kind: "voter", Strategy: "majority", Panel: []string{"ghost"},
	})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/executor/ -v`
Expected: PASS (voter, factory, endpoint, and pre-existing tests). The voter tests take ~600ms total (deliberate delays).

- [ ] **Step 7: Commit**

```bash
git add internal/executor/voter.go internal/executor/voter_test.go internal/executor/factory.go internal/executor/factory_test.go
git commit -m "feat(sp2): minimal VotingExecutor with first_success and majority strategies"
```

---

### Task 8: pipeline — model-reported failure (`Status != 0`)

**Files:**
- Modify: `internal/run/pipeline.go` (insert after the executor-error block, currently lines 206–213)
- Test: `internal/run/pipeline_test.go` (append)

- [ ] **Step 1: Write the failing pipeline tests** (append to `internal/run/pipeline_test.go`)

```go
func TestPipelineExecute_ModelReportedFailure(t *testing.T) {
	ctx := context.Background()

	// Register a plugin that must NOT be called.
	mock := &mockPlugin{name: "must-not-deliver-plugin"}
	plugin.Register(mock)

	cannedResult := domain.LLMResult{
		Status:     2,
		Summary:    "could not complete the review",
		Output:     map[string]any{},
		Transcript: "TRANSCRIPT_SENTINEL",
		Tokens:     11,
		Cost:       0.002,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "modelfail-backend"
	backendRef := &domain.BackendRef{Name: backendName}
	cfg := &config.Config{
		Backends: []config.Backend{
			{
				Name:          backendName,
				Kind:          "claude",
				Model:         "claude-3-5-sonnet",
				DefaultEffort: "normal",
				Auth:          config.BackendAuth{Mode: "subscription"},
			},
		},
	}

	rr := newFakeRunRepo()
	pipeline := &Pipeline{
		cfg:     cfg,
		runRepo: rr,
		store:   store,
		plugins: map[string]plugin.Plugin{},
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID: agentID, Name: "modelfail-agent", Role: "tester",
		SystemPrompt: "You are a test agent.", Enabled: true,
	}
	duty := &domain.Duty{
		ID: dutyID, Name: "modelfail-duty", Role: "testing",
		Description: "A duty for model-failure testing.", Prompt: "Do the task.",
	}
	assignment := &domain.Assignment{
		ID: assignmentID, AgentID: agentID, DutyID: dutyID,
		Enabled: true, Backend: backendRef, Config: map[string]any{},
		Outputs: []domain.OutputBinding{
			{Plugin: "must-not-deliver-plugin", Action: "notify", Params: map[string]any{"m": "x"}},
		},
	}

	req := ExecuteRequest{
		Assignment: assignment, Agent: agent, Duty: duty,
		TriggerKind: "event", EventParams: map[string]any{"mr_iid": "99"},
		Executor: fakeExec,
	}

	run, err := pipeline.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute returned error: %v (model-reported failure must not be a pipeline error)", err)
	}
	if run.Status != domain.RunStatusFailed {
		t.Errorf("status = %q, want failed", run.Status)
	}
	if run.Error == nil || !strings.Contains(*run.Error, "status 2") {
		t.Errorf("run.Error = %v, want llm failure message", run.Error)
	}
	// Full result recorded (transcript retained).
	if run.LLMResult == nil || run.LLMResult.Transcript != "TRANSCRIPT_SENTINEL" {
		t.Errorf("LLMResult = %+v, want transcript retained", run.LLMResult)
	}
	if run.Tokens != 11 {
		t.Errorf("Tokens = %d, want 11", run.Tokens)
	}
	// Outputs skipped.
	if mock.called {
		t.Error("output plugin was called for a failed-status result")
	}
	if len(run.OutputsDelivered) != 0 {
		t.Errorf("OutputsDelivered = %v, want none", run.OutputsDelivered)
	}
	// Dedup NOT marked: a retry must be possible.
	already, err := store.HasProcessed(ctx, assignmentID.String(), "mr_iid:99")
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Error("dedup key was marked processed for a failed run; retries are now impossible")
	}
	// Stored run reflects failure.
	stored := rr.runs[run.ID]
	if stored == nil || stored.Status != domain.RunStatusFailed {
		t.Errorf("stored run = %+v", stored)
	}
}

func TestPipelineExecute_ZeroStatusStillSucceeds(t *testing.T) {
	// Guard: the new Status check must not affect the success path.
	ctx := context.Background()
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "fine"})
	store := state.NewMemStore()
	backendName := "zerostatus-backend"
	cfg := &config.Config{Backends: []config.Backend{{
		Name: backendName, Kind: "claude", Model: "claude-3-5-sonnet",
		DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
	}}}
	rr := newFakeRunRepo()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: store, plugins: map[string]plugin.Plugin{}}

	agentID, dutyID := uuid.New(), uuid.New()
	run, err := pipeline.Execute(ctx, ExecuteRequest{
		Assignment: &domain.Assignment{
			ID: uuid.New(), AgentID: agentID, DutyID: dutyID, Enabled: true,
			Backend: &domain.BackendRef{Name: backendName}, Config: map[string]any{},
		},
		Agent: &domain.Agent{ID: agentID, Name: "z-agent", Role: "t", SystemPrompt: "s", Enabled: true},
		Duty:  &domain.Duty{ID: dutyID, Name: "z-duty", Role: "t", Description: "d", Prompt: "p"},
		TriggerKind: "manual", EventParams: map[string]any{}, Executor: fakeExec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("status = %q, want succeeded", run.Status)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/run/ -run 'TestPipelineExecute_ModelReportedFailure|TestPipelineExecute_ZeroStatusStillSucceeds' -v`
Expected: `TestPipelineExecute_ModelReportedFailure` FAILS (status is `succeeded`, plugin called); `ZeroStatusStillSucceeds` passes.

- [ ] **Step 3: Implement the pipeline change**

In `internal/run/pipeline.go`, insert directly after the executor-error block (after the `if llmErr != nil { ... }` block ending around line 213, before `// Deliver outputs.`):

```go
	// Model-reported failure: a nonzero status means the work did not succeed.
	// Record the full result for audit (including the transcript) but skip
	// output delivery — a failed run must not post half-formed outputs — and
	// skip dedup marking so the work can be retried. This also captures the
	// claude path's is_error, which parseClaudeOutput maps to Status 1.
	if llmResult.Status != 0 {
		errMsg := fmt.Sprintf("llm reported failure status %d: %s", llmResult.Status, llmResult.Summary)
		if err := p.runRepo.UpdateResult(ctx, run.ID, &llmResult, nil, domain.RunStatusFailed); err != nil {
			return nil, fmt.Errorf("record run result: %w", err)
		}
		_ = p.runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusFailed, &errMsg)
		run.LLMResult = &llmResult
		run.Tokens = llmResult.Tokens
		run.Cost = llmResult.Cost
		run.Status = domain.RunStatusFailed
		run.Error = &errMsg
		finished := time.Now()
		run.FinishedAt = &finished
		return run, nil
	}
```

- [ ] **Step 4: Run the full run-package tests**

Run: `go test ./internal/run/ -v`
Expected: PASS — both new tests AND all pre-existing pipeline tests (success path, fallbacks, dedup, pause, secrets).

- [ ] **Step 5: Commit**

```bash
git add internal/run/pipeline.go internal/run/pipeline_test.go
git commit -m "feat(sp2): nonzero LLMResult.Status fails the run and skips outputs"
```

---

### Task 9: CLI — factory dispatch + `fleet backends test` + sample config

**Files:**
- Modify: `cmd/fleet/main.go` (runCmd dispatch ~lines 513–541; scheduleCmd dispatch ~lines 685–705; backendsCmd ~line 199)
- Modify: `configs/fleet.yaml`

There is no automated test for `main.go` (SP1 ships none); verification is compile + vet + the `--fake` smoke below. Logic lives in the already-tested factory.

- [ ] **Step 1: Replace the runCmd dispatch**

In `cmd/fleet/main.go` runCmd, replace this block:

```go
				if resolved == nil || resolved.Kind == "claude" {
					var apiKey string
					if resolved != nil && resolved.Auth.Mode == "api_key" {
						apiKey = resolved.Auth.APIKey
					}
					exec = executor.NewClaudeExecutor(apiKey)
				} else {
					return fmt.Errorf("backend kind %q is not yet supported", resolved.Kind)
				}
```

with:

```go
				if resolved == nil {
					// No matching config assignment (e.g. DB-seeded): keep
					// the SP1 default of the subscription claude CLI.
					exec = executor.NewClaudeExecutor("")
				} else {
					var eerr error
					exec, eerr = executor.FromBackend(cfg, resolved)
					if eerr != nil {
						return fmt.Errorf("build executor: %w", eerr)
					}
				}
```

- [ ] **Step 2: Replace the scheduleCmd dispatch**

In scheduleCmd, replace:

```go
				var exec executor.Executor
				if resolvedBackend == nil || resolvedBackend.Kind == "claude" {
					var apiKey string
					if resolvedBackend != nil && resolvedBackend.Auth.Mode == "api_key" {
						apiKey = resolvedBackend.Auth.APIKey
					}
					exec = executor.NewClaudeExecutor(apiKey)
				} else {
					fmt.Fprintf(os.Stderr, "scheduler: backend kind %q is not yet supported for assignment %s\n", resolvedBackend.Kind, assignmentID)
					return
				}
```

with:

```go
				var exec executor.Executor
				if resolvedBackend == nil {
					exec = executor.NewClaudeExecutor("")
				} else {
					var eerr error
					exec, eerr = executor.FromBackend(cfg, resolvedBackend)
					if eerr != nil {
						fmt.Fprintf(os.Stderr, "scheduler: build executor for assignment %s: %v\n", assignmentID, eerr)
						return
					}
				}
```

- [ ] **Step 3: Add `fleet backends test`**

In `backendsCmd()`, add `cmd.AddCommand(backendsTestCmd())` after the existing two AddCommand calls. Then add below `backendsLoginCmd`:

```go
func backendsTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <backend-name>",
		Short: "One-shot connectivity/auth smoke test against a backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backendName := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			var backend *config.Backend
			for i := range cfg.Backends {
				if cfg.Backends[i].Name == backendName {
					backend = &cfg.Backends[i]
					break
				}
			}
			if backend == nil {
				return fmt.Errorf("backend %q not found in config", backendName)
			}
			ex, err := executor.FromBackend(cfg, backend)
			if err != nil {
				return fmt.Errorf("build executor: %w", err)
			}
			ws, err := os.MkdirTemp("", "fleet-backend-test-*")
			if err != nil {
				return fmt.Errorf("create workspace: %w", err)
			}
			defer os.RemoveAll(ws)

			fmt.Printf("testing backend %q (kind %s)...\n", backend.Name, backend.Kind)
			result, err := ex.Run(cmd.Context(), executor.LLMRequest{
				Prompt:    "Reply with only the word: OK",
				Workspace: ws,
				Model:     backend.Model,
				Effort:    backend.DefaultEffort,
			})
			if err != nil {
				return fmt.Errorf("backend test failed: %w", err)
			}
			fmt.Printf("Status:  %d\n", result.Status)
			fmt.Printf("Summary: %s\n", result.Summary)
			fmt.Printf("Tokens:  %d\n", result.Tokens)
			return nil
		},
	}
}
```

- [ ] **Step 4: Add commented sample backends to `configs/fleet.yaml`**

After the existing commented `claude-apikey` example block (line 17), add:

```yaml
# Example: openai-compatible endpoint backend (Ollama/vLLM/llama.cpp; SP2)
# Driven by OfficeFleet's generic agent loop. Requires base_uri + model.
#  - name: local-ollama
#    kind: openai-compatible
#    base_uri: http://localhost:11434/v1
#    model: llama3.1:70b
#    auth:
#      mode: none                  # or: mode: api_key + api_key: ${env:...}
#    default_effort: medium
#    params:
#      num_ctx: 8192               # passed through to the provider request
#    max_iterations: 25            # agent loop cap
#    command_timeout: 120s         # per run_command
#    max_output_bytes: 65536       # tool observation cap
#    command_allowlist: []         # empty = allow all commands

# Example: multi-model voter (SP2) — fans out to a panel, aggregates by strategy
#  - name: review-panel
#    kind: voter
#    strategy: first_success       # or: majority (plurality on status code)
#    panel: [claude-default, local-ollama]
```

- [ ] **Step 5: Build, vet, and smoke**

```bash
go build ./... && go vet ./...
```
Expected: clean.

```bash
go run ./cmd/fleet --config configs/fleet.yaml config validate
```
Expected: validation passes (the new examples are commented out, so config is unchanged semantically).

- [ ] **Step 6: Commit**

```bash
git add cmd/fleet/main.go configs/fleet.yaml
git commit -m "feat(sp2): factory-based CLI dispatch and fleet backends test"
```

---

### Task 10: integration test + live smoke flag

**Files:**
- Create: `internal/run/pipeline_endpoint_test.go`
- Create: `internal/executor/endpoint_live_test.go`

- [ ] **Step 1: Write the endpoint pipeline integration test**

Full pipeline through a **real** `EndpointExecutor` against a scripted httptest server — the SP2 analogue of SP1's stubbed-executor pipeline test, proving the spec's acceptance criteria #2 and #3 (Output map → output template rendering).

`internal/run/pipeline_endpoint_test.go`:

```go
package run

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/google/uuid"
)

// deliveryRecorder is a plugin that records the params it was called with.
type deliveryRecorder struct {
	name   string
	params map[string]any
}

func (d *deliveryRecorder) Name() string                       { return d.name }
func (d *deliveryRecorder) EventSources() []plugin.EventSource { return nil }
func (d *deliveryRecorder) Actions() []plugin.Action           { return nil }
func (d *deliveryRecorder) ConfigSchema() plugin.Schema        { return nil }
func (d *deliveryRecorder) Init(_ context.Context, _ map[string]any, _ plugin.SecretLookup) error {
	return nil
}
func (d *deliveryRecorder) Do(_ context.Context, _ string, params map[string]any) (map[string]any, error) {
	d.params = params
	return map[string]any{}, nil
}

func TestPipelineExecute_EndpointBackendEndToEnd(t *testing.T) {
	ctx := context.Background()

	// Scripted openai-compatible server: one tool turn, then submit_result.
	call := 0
	responses := []string{
		`{
		  "choices": [{"message": {"role": "assistant", "content": "",
		    "tool_calls": [{"id": "c1", "type": "function",
		      "function": {"name": "run_command", "arguments": "{\"cmd\":\"echo inspecting\"}"}}]}}],
		  "usage": {"prompt_tokens": 20, "completion_tokens": 10}
		}`,
		`{
		  "choices": [{"message": {"role": "assistant", "content": "",
		    "tool_calls": [{"id": "c2", "type": "function",
		      "function": {"name": "submit_result",
		        "arguments": "{\"summary\":\"review complete\",\"status\":0,\"output\":{\"verdict\":\"approve\"}}"}}]}}],
		  "usage": {"prompt_tokens": 30, "completion_tokens": 15}
		}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if call >= len(responses) {
			t.Errorf("unexpected chat call #%d", call+1)
			w.WriteHeader(500)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "test-model" {
			t.Errorf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[call]))
		call++
	}))
	defer srv.Close()

	backendName := "ep-backend"
	cfg := &config.Config{
		Backends: []config.Backend{{
			Name:    backendName,
			Kind:    "openai-compatible",
			BaseURI: srv.URL,
			Model:   "test-model",
			Auth:    config.BackendAuth{Mode: "none"},
		}},
	}
	ep, err := executor.NewEndpointExecutor(&cfg.Backends[0])
	if err != nil {
		t.Fatal(err)
	}

	recorder := &deliveryRecorder{name: "ep-recorder-plugin"}
	plugin.Register(recorder)

	rr := newFakeRunRepo()
	pipeline := &Pipeline{
		cfg:     cfg,
		runRepo: rr,
		store:   state.NewMemStore(),
		plugins: map[string]plugin.Plugin{},
	}

	agentID, dutyID, assignmentID := uuid.New(), uuid.New(), uuid.New()
	run, err := pipeline.Execute(ctx, ExecuteRequest{
		Assignment: &domain.Assignment{
			ID: assignmentID, AgentID: agentID, DutyID: dutyID, Enabled: true,
			Backend: &domain.BackendRef{Name: backendName},
			Config:  map[string]any{},
			Outputs: []domain.OutputBinding{{
				Plugin: "ep-recorder-plugin",
				Action: "post",
				// llm_summary comes from the submit_result summary;
				// llm_output is the JSON-encoded Output map.
				Params: map[string]any{
					"body":   "{{.Event.llm_summary}}",
					"output": "{{.Event.llm_output}}",
				},
			}},
		},
		Agent: &domain.Agent{
			ID: agentID, Name: "ep-agent", Role: "developer",
			SystemPrompt: "You are a reviewer.", Enabled: true,
		},
		Duty: &domain.Duty{
			ID: dutyID, Name: "ep-duty", Role: "developer",
			Description: "endpoint duty", Prompt: "Review the code.",
			RequiredTools: []string{},
		},
		TriggerKind: "manual",
		EventParams: map[string]any{},
		Executor:    ep,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("status = %q, want succeeded", run.Status)
	}
	if run.LLMResult == nil || run.LLMResult.Summary != "review complete" {
		t.Fatalf("LLMResult = %+v", run.LLMResult)
	}
	if run.Tokens != 75 { // 30 + 45
		t.Errorf("Tokens = %d, want 75", run.Tokens)
	}
	if run.RenderedSystemPrompt == "" || run.RenderedPrompt == "" {
		t.Error("rendered prompts must be recorded")
	}
	// Output delivery rendered from the submit_result payload.
	if len(run.OutputsDelivered) != 1 || run.OutputsDelivered[0].Status != "delivered" {
		t.Fatalf("OutputsDelivered = %+v", run.OutputsDelivered)
	}
	if recorder.params["body"] != "review complete" {
		t.Errorf("delivered body = %v", recorder.params["body"])
	}
	outJSON, _ := recorder.params["output"].(string)
	if !strings.Contains(outJSON, `"verdict":"approve"`) {
		t.Errorf("delivered output = %q, want submit_result output JSON", outJSON)
	}
	// Transcript persisted on the recorded run.
	if !strings.Contains(run.LLMResult.Transcript, "run_command") {
		t.Error("transcript missing tool activity")
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/run/ -run TestPipelineExecute_EndpointBackendEndToEnd -v`
Expected: PASS. (If it fails, debug with the systematic-debugging skill — the seams are: client encode/decode, loop, bridge, pipeline.)

- [ ] **Step 3: Add the live Ollama smoke (flagged off by default)**

`internal/executor/endpoint_live_test.go`:

```go
package executor

import (
	"context"
	"flag"
	"os"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
)

var liveOllamaFlag = flag.Bool("live-ollama", false, "run live smoke against a local Ollama (needs a tool-capable model)")

// TestEndpointExecutor_LiveOllamaSmoke exercises the real loop against a local
// Ollama. Requires `ollama serve` and a function-calling-capable model.
// Run: go test ./internal/executor/ -run LiveOllama -live-ollama -v
// Override the model with OLLAMA_MODEL (default llama3.1).
func TestEndpointExecutor_LiveOllamaSmoke(t *testing.T) {
	if !*liveOllamaFlag {
		t.Skip("skipping live test; pass -live-ollama to enable")
	}
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "llama3.1"
	}
	ex, err := NewEndpointExecutor(&config.Backend{
		Name:    "live-ollama",
		Kind:    "openai-compatible",
		BaseURI: "http://localhost:11434/v1",
		Model:   model,
		Auth:    config.BackendAuth{Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ex.Run(context.Background(), LLMRequest{
		Prompt:    "Create a file named hello.txt containing the word hello, then submit a result summarizing what you did.",
		Workspace: t.TempDir(),
		Model:     model,
	})
	if err != nil {
		t.Fatalf("live run failed: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	t.Logf("status=%d tokens=%d summary=%s", result.Status, result.Tokens, result.Summary)
}
```

- [ ] **Step 4: Verify the flag plumbing compiles and skips**

Run: `go test ./internal/executor/ -run LiveOllama -v`
Expected: SKIP (flag not set).

- [ ] **Step 5: Commit**

```bash
git add internal/run/pipeline_endpoint_test.go internal/executor/endpoint_live_test.go
git commit -m "test(sp2): endpoint pipeline integration test and live Ollama smoke flag"
```

---

### Task 11: full verification & wrap-up

**Files:** none new.

- [ ] **Step 1: Format, vet, full test suite**

```bash
gofmt -l . && go vet ./... && go test ./...
```
Expected: `gofmt -l` prints nothing; vet clean; **all** packages pass (including every pre-existing SP1 test — acceptance criterion #7).

- [ ] **Step 2: Confirm SP1 interface untouched**

```bash
git diff master --stat -- internal/executor/executor.go internal/domain/types.go
```
Expected: **no output** (zero changes to the SP1 `Executor`/`LLMRequest`/`LLMResult` definitions). Note: run this against the commit where SP2 work started if `master` has moved.

- [ ] **Step 3: Manual smoke (optional but recommended)**

```bash
go run ./cmd/fleet --config configs/fleet.yaml config validate
go run ./cmd/fleet --config configs/fleet.yaml backends list
```
Expected: validate passes; list shows `claude-default`.

If a local Ollama is available:
```bash
ollama serve &   # if not already running
go test ./internal/executor/ -run LiveOllama -live-ollama -v
go run ./cmd/fleet --config configs/fleet.yaml backends test claude-default   # needs claude CLI login
```

- [ ] **Step 4: Final commit (if any stragglers) and push**

```bash
git status --short   # expect clean; commit anything outstanding
git push origin master
```

---

## Acceptance criteria traceability (spec §10)

| Spec AC | Covered by |
|---|---|
| 1. Config validation (endpoint required fields, subscription rejection, voter rules, ref overrides) | Task 5 tests |
| 2. End-to-end endpoint run with tools + submit_result, full Run recorded | Task 10 integration test |
| 3. submit_result.output → LLMResult.Output → output template rendering | Task 10 (`llm_output`/`llm_summary` assertions) |
| 4. run_command cwd/timeout/truncation/allowlist; denials as observations; file containment | Task 3 bridge tests |
| 5. Max-iterations + transport failure handling; Status != 0 → failed + outputs skipped | Task 2 (loop), Task 4 (retry), Task 8 (pipeline) |
| 6. Voter concurrency, per-member workspaces, strategies, token/cost summing | Task 7 tests |
| 7. SP1 types unchanged; SP1 tests pass; --fake unchanged; lint+vet clean | Task 11 steps 1–2 |
| 8. `fleet backends test` | Task 9 step 3 (manual smoke Task 11 step 3) |
