# SP5 — Sample Duties / huginn parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port huginn's three behaviors (MR review, code audit, MR feedback) as sample duties, powered by `for_each` output fan-out, three real GitLab actions, and `mr_note` events.

**Architecture:** Fan-out lives in the delivery layer (`OutputBinding.ForEach` + `prompt.Context.Item`); the claude executor learns to extract a fenced/whole JSON object from the model's final text into `LLMResult.Output` (endpoint backends already produce structured output via `submit_result`); the GitLab plugin gains `post_inline_comment`/`create_issue`/`reply_to_discussion` and `mr_note` webhook ingestion with `bot_username` loop protection; `configs/fleet.yaml` replaces the old mr-reviewer trio with one assignment each for three duties (which also kills the recorded seed-collapse wart — distinct duties now).

**Tech Stack:** Go stdlib only; zero new deps. One-line TS mirror update (`for_each` on OutputBinding).

**Spec:** `docs/superpowers/specs/2026-06-11-sp5-sample-duties-design.md`

---

## Environment notes

1. **`NODE_OPTIONS` quirk (this session):** if `node`/`npm` fails with `Cannot find module ... restore-node-options.cjs`, prefix with `NODE_OPTIONS= ` (empty). Never commit that string.
2. Conventions: commit to `master`; TDD; gofmt + `go vet ./...` clean; zero new go.mod deps; fakes/httptest only (no Postgres, no live GitLab); LSP diagnostics are often stale — trust `go test`.
3. Run Go commands from the repo root.

## File map

| Path | Action | Responsibility |
|---|---|---|
| `internal/executor/claude.go` (+test) | modify | extract fenced/whole JSON object from result text → `LLMResult.Output` (+ summary lift) |
| `internal/domain/types.go` | modify | `OutputBinding.ForEach` |
| `internal/prompt/engine.go` | modify | `Context.Item` |
| `internal/outputs/deliver.go` (+test) | modify | fan-out delivery, 50-item cap, per-item records |
| `web/src/api/types.ts` | modify | `for_each?: string` on OutputBinding mirror |
| `internal/plugins/gitlab/gitlab.go` (+test) | modify | `apiRequest` helper; `post_inline_comment`, `create_issue`, `reply_to_discussion`; `bot_username` config |
| `internal/plugins/gitlab/events.go` (+test) | modify | `mr_note` webhook ingestion + bot drop + `mr_notes` source entry |
| `internal/config/config.go` (+test) | modify | `for_each` key validation (bare key, no template syntax) |
| `configs/fleet.yaml` | modify | developer agent + mr-review / code-audit / mr-feedback duties + 1 assignment each + `bot_username` |
| `internal/config/sample_test.go` (or extend existing) | create/modify | sample parses/validates; the three duty prompts render against a synthetic context |

## Verified contract facts

- `prompt.Context` fields: Event/Agent/Duty/Assignment/State/Now/Secrets; `promptCtx.Assignment` **is** the assignment's `config` map (pipeline.go:123), so `{{.Assignment.project}}` resolves to `config.project`. `{{secret "name"}}` helper exists (helpers(ctx.Secrets)).
- `outputs.Deliver(ctx, outputs, result, promptCtx)` renders only string params; enriches `.Event` with `llm_summary`/`llm_transcript`/`llm_output`; never aborts on individual failures.
- Claude executor (`parseClaudeOutput`) currently sets `Output["raw"]` = result text — NO structured output. Endpoint backends (SP2 agentloop) populate `LLMResult.Output` from `submit_result`'s `output` param. Task 1 closes the claude gap.
- GitLab plugin: `paramToString` coerces string/int/float64; project paths encode `/`→`%2F` literally (NOT url.PathEscape — GitLab wants literal %2F; existing code uses `strings.ReplaceAll`); auth via `PRIVATE-TOKEN`; `Do` currently stubs `create_issue` and `resolve_discussion` ("not yet implemented (SP3)").
- Webhook: `HandleWebhook` checks `X-Gitlab-Token` (constant-time) then parses `object_kind`; non-MR kinds return `(nil, nil)` (ack + ignore).
- Event filter matching (`events.Matches`): filter needs `source` + `event_type`; every other key must equal the same-named top-level `payload_norm` field (string-compared).
- `events.Matches` and dispatcher are UNCHANGED by SP5 (mr_note flows through existing machinery).
- The existing sample `configs/fleet.yaml` has agent `dev-1` + duty `mr-reviewer` + THREE (dev-1, mr-reviewer) assignments — the recorded seed-collapse wart (UNIQUE(agent_id, duty_id) keeps only one). Task 5 replaces this with three distinct duties, one assignment each.
- Check for an existing sample-config test with `grep -rn "fleet.yaml" internal/ --include=*_test.go` before creating one.

## The shared result contract (used by all three duty prompts)

Duty prompts must work on BOTH executor paths, so each prompt ends with this contract paragraph (exact text in Task 5):

> Report your result as a single JSON object with the keys described above.
> If you have a `submit_result` tool, call it with that object as the `output` parameter
> (and a one-paragraph `summary`). Otherwise end your final message with exactly one
> fenced ```json code block containing the object.

- Endpoint backends: `submit_result(output=...)` → `LLMResult.Output` = the object (existing SP2 behavior).
- Claude CLI: Task 1 extracts the fenced (or whole-text) JSON object into `LLMResult.Output` and lifts `output.summary` into `LLMResult.Summary`.

---

### Task 1: Claude executor — structured JSON extraction

**Files:**
- Modify: `internal/executor/claude.go` (parseClaudeOutput)
- Test: `internal/executor/claude_test.go` (extend)

- [ ] **Step 1: Write the failing tests**

Read `internal/executor/claude_test.go` first (existing parse tests — follow their fixture style). Append:

```go
func TestParseClaudeOutputExtractsFencedJSON(t *testing.T) {
	resultText := "I reviewed the MR.\n\n```json\n{\"summary\": \"2 issues found\", \"comments\": [{\"path\": \"a.go\", \"line\": 7, \"body\": \"nil deref\"}]}\n```"
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "2 issues found" {
		t.Errorf("Summary = %q, want the lifted output.summary", got.Summary)
	}
	comments, ok := got.Output["comments"].([]any)
	if !ok || len(comments) != 1 {
		t.Fatalf("Output[comments] = %#v, want 1-element array", got.Output["comments"])
	}
	first, _ := comments[0].(map[string]any)
	if first["path"] != "a.go" {
		t.Errorf("comment path = %v", first["path"])
	}
	if got.Output["raw"] != resultText {
		t.Errorf("raw text must still be preserved alongside the parsed object")
	}
}

func TestParseClaudeOutputWholeTextJSON(t *testing.T) {
	resultText := `{"summary": "all clear", "issues": []}`
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "all clear" {
		t.Errorf("Summary = %q", got.Summary)
	}
	if _, ok := got.Output["issues"]; !ok {
		t.Error("Output missing issues key")
	}
}

func TestParseClaudeOutputPlainTextUnchanged(t *testing.T) {
	resultText := "Just prose, no JSON contract here."
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != resultText {
		t.Errorf("Summary = %q, want the raw text", got.Summary)
	}
	if got.Output["raw"] != resultText {
		t.Errorf("Output[raw] = %v", got.Output["raw"])
	}
	if len(got.Output) != 1 {
		t.Errorf("plain text must not grow Output keys: %#v", got.Output)
	}
}

func TestParseClaudeOutputMalformedFenceFallsBack(t *testing.T) {
	resultText := "Findings:\n```json\n{not valid json]\n```"
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != resultText {
		t.Errorf("malformed fence must leave Summary as raw text")
	}
	if len(got.Output) != 1 {
		t.Errorf("malformed fence must not grow Output: %#v", got.Output)
	}
}

func TestParseClaudeOutputJSONWithoutSummaryKeepsTextSummary(t *testing.T) {
	resultText := "Done.\n```json\n{\"comments\": []}\n```"
	wrapper, _ := json.Marshal(map[string]any{"type": "result", "result": resultText})

	got, err := parseClaudeOutput(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != resultText {
		t.Errorf("without output.summary the full text stays the summary, got %q", got.Summary)
	}
	if _, ok := got.Output["comments"]; !ok {
		t.Error("parsed object must still land in Output")
	}
}
```

(Add imports as needed; `encoding/json` is already imported in the test file or add it.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/executor/ -run TestParseClaudeOutput -v`
Expected: the new tests FAIL (Output has only "raw"; Summary not lifted).

- [ ] **Step 3: Implement**

In `internal/executor/claude.go`, inside `parseClaudeOutput`, replace:

```go
	if v, ok := raw["result"].(string); ok {
		result.Summary = v
		result.Output["raw"] = v
	}
```

with:

```go
	if v, ok := raw["result"].(string); ok {
		result.Summary = v
		result.Output["raw"] = v
		// SP5 structured-result contract: when the final text carries a JSON
		// object (whole text or the last fenced ```json block), expose it as
		// the structured Output so output fan-out can iterate its lists —
		// parity with what submit_result gives endpoint backends.
		if obj := extractJSONObject(v); obj != nil {
			for k, val := range obj {
				result.Output[k] = val
			}
			if s, ok := obj["summary"].(string); ok && s != "" {
				result.Summary = s
			}
		}
	}
```

And add at the bottom of the file:

```go
// jsonFenceRe captures the contents of ```json ... ``` blocks.
var jsonFenceRe = regexp.MustCompile("(?s)```json\\s*(.*?)```")

// extractJSONObject pulls a structured result object from the model's final
// text: the whole text when it is a JSON object, else the LAST fenced
// ```json block that parses to an object. Returns nil when there is none.
func extractJSONObject(text string) map[string]any {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			return obj
		}
	}
	matches := jsonFenceRe.FindAllStringSubmatch(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		var obj map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(matches[i][1])), &obj); err == nil {
			return obj
		}
	}
	return nil
}
```

Add `"regexp"` and `"strings"` to imports if missing.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/executor/ -count=1`
Expected: PASS (new + all existing parse tests — verify none of the existing ones assumed `Output` stays raw-only for JSON-bearing fixtures; adjust ONLY if an existing fixture text genuinely contains a fenced JSON object, and note it in your report).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/executor
git commit -m "feat(sp5): claude executor extracts structured JSON result objects"
```

---

### Task 2: Output fan-out (`for_each`)

**Files:**
- Modify: `internal/domain/types.go` (OutputBinding)
- Modify: `internal/prompt/engine.go` (Context)
- Modify: `internal/outputs/deliver.go`
- Test: `internal/outputs/deliver_test.go` (extend)
- Modify: `web/src/api/types.ts` (OutputBinding mirror)

- [ ] **Step 1: Add the fields**

`internal/domain/types.go` — extend OutputBinding:

```go
// OutputBinding routes an output action to a specific target. When ForEach
// names a key of LLMResult.Output holding a JSON array, the action is
// delivered once per element (the element renders as {{.Item.*}}).
type OutputBinding struct {
	Plugin  string         `json:"plugin"`
	Action  string         `json:"action"`
	Params  map[string]any `json:"params"`
	ForEach string         `json:"for_each,omitempty"`
}
```

(Keep/merge the existing doc comment style; the yaml side decodes via the same json-ish field names — check whether OutputBinding has yaml tags today: it does NOT (config uses `domain.OutputBinding` directly in `AssignmentConfig.Outputs`), and yaml.v3 lowercases field names by default — `ForEach` would decode from `foreach`, NOT `for_each`. **Verify how the existing fields decode:** `Plugin`/`Action`/`Params` decode from lowercase single words so it was never visible. Add EXPLICIT yaml tags to the whole struct to make `for_each` work:

```go
type OutputBinding struct {
	Plugin  string         `json:"plugin" yaml:"plugin"`
	Action  string         `json:"action" yaml:"action"`
	Params  map[string]any `json:"params" yaml:"params"`
	ForEach string         `json:"for_each,omitempty" yaml:"for_each,omitempty"`
}
```

Check whether other domain structs embedded in config (TriggerConfig, BackendRef, OutputActionType) already carry yaml tags — BackendRef does (`yaml:"name"`). Follow that precedent. TriggerConfig has NO yaml tags but its fields are single lowercase words (kind, schedule, filter) so it works implicitly; leave it alone.)

`internal/prompt/engine.go` — add to Context:

```go
	// Item is the current fan-out element during for_each output delivery
	// (nil outside fan-out rendering).
	Item map[string]any
```

- [ ] **Step 2: Write the failing tests**

Read `internal/outputs/deliver_test.go` first (it has a fake plugin registration pattern — reuse it; if the fake records calls, keep using it). Append tests (adapt the fake-plugin helper names to what exists — the assertions below are the contract):

```go
func TestDeliverForEachFansOut(t *testing.T) {
	fake := registerFakePlugin(t) // adapt: however the existing tests register/reset a recording fake plugin
	result := domain.LLMResult{Output: map[string]any{
		"comments": []any{
			map[string]any{"path": "a.go", "line": float64(7), "body": "nil deref"},
			map[string]any{"path": "b.go", "line": float64(12), "body": "unchecked err"},
		},
	}}
	bindings := []domain.OutputBinding{{
		Plugin: fake.name, Action: "post", ForEach: "comments",
		Params: map[string]any{"path": "{{.Item.path}}", "line": "{{.Item.line}}", "note": "static"},
	}}

	deliveries := Deliver(context.Background(), bindings, result, prompt.Context{})
	if len(deliveries) != 2 {
		t.Fatalf("deliveries = %d, want 2 (one per item)", len(deliveries))
	}
	for i, want := range []string{"a.go", "b.go"} {
		if deliveries[i].Status != "delivered" {
			t.Errorf("delivery %d status = %s (%s)", i, deliveries[i].Status, deliveries[i].Error)
		}
		if deliveries[i].Params["path"] != want {
			t.Errorf("delivery %d path = %v, want %s", i, deliveries[i].Params["path"], want)
		}
	}
	if got := fake.callCount(); got != 2 {
		t.Errorf("plugin invoked %d times, want 2", got)
	}
}

func TestDeliverForEachEmptyOrMissingIsZeroDeliveries(t *testing.T) {
	registerFakePlugin(t)
	for name, output := range map[string]map[string]any{
		"missing key": {},
		"nil value":   {"comments": nil},
		"empty array": {"comments": []any{}},
	} {
		t.Run(name, func(t *testing.T) {
			bindings := []domain.OutputBinding{{Plugin: "fake", Action: "post", ForEach: "comments", Params: map[string]any{}}}
			deliveries := Deliver(context.Background(), bindings, domain.LLMResult{Output: output}, prompt.Context{})
			if len(deliveries) != 0 {
				t.Errorf("deliveries = %d, want 0", len(deliveries))
			}
		})
	}
}

func TestDeliverForEachNonArrayFails(t *testing.T) {
	registerFakePlugin(t)
	bindings := []domain.OutputBinding{{Plugin: "fake", Action: "post", ForEach: "comments", Params: map[string]any{}}}
	result := domain.LLMResult{Output: map[string]any{"comments": "not-a-list"}}

	deliveries := Deliver(context.Background(), bindings, result, prompt.Context{})
	if len(deliveries) != 1 || deliveries[0].Status != "failed" {
		t.Fatalf("want exactly one failed delivery, got %#v", deliveries)
	}
	if !strings.Contains(deliveries[0].Error, "not an array") {
		t.Errorf("error = %q", deliveries[0].Error)
	}
}

func TestDeliverForEachNonObjectItemFailsThatItemOnly(t *testing.T) {
	fake := registerFakePlugin(t)
	result := domain.LLMResult{Output: map[string]any{
		"comments": []any{"just-a-string", map[string]any{"path": "ok.go"}},
	}}
	bindings := []domain.OutputBinding{{Plugin: fake.name, Action: "post", ForEach: "comments", Params: map[string]any{"path": "{{.Item.path}}"}}}

	deliveries := Deliver(context.Background(), bindings, result, prompt.Context{})
	if len(deliveries) != 2 {
		t.Fatalf("deliveries = %d, want 2", len(deliveries))
	}
	if deliveries[0].Status != "failed" || deliveries[1].Status != "delivered" {
		t.Errorf("statuses = %s,%s — non-object item must fail alone", deliveries[0].Status, deliveries[1].Status)
	}
}

func TestDeliverForEachCapsAtFifty(t *testing.T) {
	fake := registerFakePlugin(t)
	items := make([]any, 73)
	for i := range items {
		items[i] = map[string]any{"n": float64(i)}
	}
	bindings := []domain.OutputBinding{{Plugin: fake.name, Action: "post", ForEach: "comments", Params: map[string]any{}}}

	deliveries := Deliver(context.Background(), bindings, domain.LLMResult{Output: map[string]any{"comments": items}}, prompt.Context{})
	if len(deliveries) != 51 {
		t.Fatalf("deliveries = %d, want 50 delivered + 1 truncation record", len(deliveries))
	}
	last := deliveries[50]
	if last.Status != "failed" || !strings.Contains(last.Error, "truncated") || !strings.Contains(last.Error, "73") {
		t.Errorf("truncation record = %#v", last)
	}
	if got := fake.callCount(); got != 50 {
		t.Errorf("plugin invoked %d times, want exactly 50", got)
	}
}

func TestDeliverWithoutForEachUnchanged(t *testing.T) {
	fake := registerFakePlugin(t)
	bindings := []domain.OutputBinding{{Plugin: fake.name, Action: "post", Params: map[string]any{"body": "{{.Event.llm_summary}}"}}}
	deliveries := Deliver(context.Background(), bindings, domain.LLMResult{Summary: "hi"}, prompt.Context{})
	if len(deliveries) != 1 || deliveries[0].Status != "delivered" || deliveries[0].Params["body"] != "hi" {
		t.Fatalf("plain binding regression: %#v", deliveries)
	}
}
```

Add `"strings"` / `"github.com/cbarraford/office-fleet/internal/prompt"` imports as needed. If the existing fake-plugin helper differs (name, reset semantics), adapt the calls but keep every assertion.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/outputs/ -v 2>&1 | head -30`
Expected: compile FAIL (`ForEach` undefined) or assertion failures.

- [ ] **Step 4: Implement in `internal/outputs/deliver.go`**

Replace the body of `Deliver` with a per-binding dispatch and add the fan-out path:

```go
// maxFanOutItems caps a single for_each binding so a hallucinating model
// cannot file thousands of issues; the truncation is recorded, not silent.
const maxFanOutItems = 50

// Deliver executes each configured output binding: renders params, calls plugin.Do.
// Returns OutputDelivery records (one per plain binding; one PER ITEM for
// for_each bindings); never aborts early on individual failures.
func Deliver(
	ctx context.Context,
	outputs []domain.OutputBinding,
	result domain.LLMResult,
	promptCtx prompt.Context,
) []domain.OutputDelivery {
	deliveries := make([]domain.OutputDelivery, 0, len(outputs))
	for _, out := range outputs {
		if out.ForEach != "" {
			deliveries = append(deliveries, deliverFanOut(ctx, out, result, promptCtx)...)
			continue
		}
		deliveries = append(deliveries, deliverOne(ctx, out, result, promptCtx, nil))
	}
	return deliveries
}

// deliverFanOut delivers out once per element of result.Output[out.ForEach].
func deliverFanOut(
	ctx context.Context,
	out domain.OutputBinding,
	result domain.LLMResult,
	promptCtx prompt.Context,
) []domain.OutputDelivery {
	raw, ok := result.Output[out.ForEach]
	if !ok || raw == nil {
		return nil // healthy "no findings": zero deliveries
	}
	list, ok := raw.([]any)
	if !ok {
		return []domain.OutputDelivery{{
			Plugin: out.Plugin, Action: out.Action, Status: "failed",
			Error: fmt.Sprintf("for_each key %q is not an array", out.ForEach),
		}}
	}
	if len(list) == 0 {
		return nil
	}
	n := len(list)
	truncated := n > maxFanOutItems
	if truncated {
		n = maxFanOutItems
	}
	deliveries := make([]domain.OutputDelivery, 0, n+1)
	for i := 0; i < n; i++ {
		item, ok := list[i].(map[string]any)
		if !ok {
			deliveries = append(deliveries, domain.OutputDelivery{
				Plugin: out.Plugin, Action: out.Action, Status: "failed",
				Error: fmt.Sprintf("for_each item %d is not an object", i),
			})
			continue
		}
		deliveries = append(deliveries, deliverOne(ctx, out, result, promptCtx, item))
	}
	if truncated {
		deliveries = append(deliveries, domain.OutputDelivery{
			Plugin: out.Plugin, Action: out.Action, Status: "failed",
			Error: fmt.Sprintf("for_each list truncated: %d items exceeds the cap of %d", len(list), maxFanOutItems),
		})
	}
	return deliveries
}

// deliverOne renders params (item non-nil during fan-out) and calls plugin.Do.
func deliverOne(
	ctx context.Context,
	out domain.OutputBinding,
	result domain.LLMResult,
	promptCtx prompt.Context,
	item map[string]any,
) domain.OutputDelivery {
	d := domain.OutputDelivery{Plugin: out.Plugin, Action: out.Action}
	rendered, err := renderParams(out.Params, result, promptCtx, item)
	if err != nil {
		d.Status = "failed"
		d.Error = fmt.Sprintf("render params: %v", err)
		return d
	}
	d.Params = rendered
	p, ok := plugin.Get(out.Plugin)
	if !ok {
		d.Status = "failed"
		d.Error = fmt.Sprintf("plugin %q not registered", out.Plugin)
		return d
	}
	if _, err = p.Do(ctx, out.Action, rendered); err != nil {
		d.Status = "failed"
		d.Error = err.Error()
	} else {
		d.Status = "delivered"
	}
	return d
}
```

And extend `renderParams` to take and expose the item:

```go
// renderParams resolves each string param value as a Go template. item, when
// non-nil, is exposed as {{.Item.*}} (fan-out element).
func renderParams(params map[string]any, result domain.LLMResult, promptCtx prompt.Context, item map[string]any) (map[string]any, error) {
	// Enrich the context with LLM result fields so templates can reference {{.Event.llm_summary}}.
	enriched := promptCtx
	enriched.Event = make(map[string]any, len(promptCtx.Event)+3)
	for k, v := range promptCtx.Event {
		enriched.Event[k] = v
	}
	enriched.Event["llm_summary"] = result.Summary
	enriched.Event["llm_transcript"] = result.Transcript
	enriched.Event["llm_output"] = mustJSON(result.Output)
	enriched.Item = item

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
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/outputs/ -count=1 && go build ./... && go test ./... -count=1`
Expected: PASS everywhere (existing Deliver tests keep passing — `deliverOne` with nil item is behavior-identical).

- [ ] **Step 6: TS mirror + gate**

In `web/src/api/types.ts`, extend the OutputBinding interface:

```ts
export interface OutputBinding {
  plugin: string
  action: string
  params: Record<string, unknown> | null
  for_each?: string
}
```

Run: `cd web && NODE_OPTIONS= npm run check`
Expected: tsc + 14 vitest tests pass.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/domain internal/prompt internal/outputs web/src/api/types.ts
git commit -m "feat(sp5): for_each output fan-out with per-item delivery records"
```

---

### Task 3: GitLab actions — post_inline_comment, create_issue, reply_to_discussion

**Files:**
- Modify: `internal/plugins/gitlab/gitlab.go`
- Test: `internal/plugins/gitlab/gitlab_test.go` (extend; check existing httptest patterns first)

- [ ] **Step 1: Write the failing tests**

Read the existing gitlab test file(s) for the httptest + plugin-init pattern (the plugin needs `token`/`baseURL` set — existing tests either call `Init` with a fake secret lookup or set fields directly; follow suit). Append:

```go
// newTestPlugin returns a GitLabPlugin pointed at srv with a test token.
// (Adapt to the existing test helper if one exists.)
func newTestPlugin(srvURL string) *GitLabPlugin {
	return &GitLabPlugin{token: "tok", baseURL: srvURL}
}

func TestPostInlineComment(t *testing.T) {
	var discussionBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{proj}/merge_requests/42/versions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"head_committed_sha": "h", "base_commit_sha": "b", "start_commit_sha": "s",
		}})
	})
	mux.HandleFunc("POST /api/v4/projects/{proj}/merge_requests/42/discussions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&discussionBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": "d1"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	out, err := g.Do(context.Background(), "post_inline_comment", map[string]any{
		"project": "org/repo", "mr_iid": "42", "path": "a.go", "line": float64(7), "body": "nil deref",
	})
	if err != nil {
		t.Fatal(err)
	}
	pos, _ := discussionBody["position"].(map[string]any)
	if pos == nil {
		t.Fatalf("no position in discussion payload: %#v", discussionBody)
	}
	if pos["new_path"] != "a.go" || pos["new_line"] != "7" && pos["new_line"] != float64(7) {
		t.Errorf("position = %#v", pos)
	}
	if pos["head_sha"] != "h" || pos["base_sha"] != "b" || pos["start_sha"] != "s" {
		t.Errorf("position SHAs = %#v", pos)
	}
	if discussionBody["body"] != "nil deref" {
		t.Errorf("body = %v", discussionBody["body"])
	}
	if out["fallback"] != nil {
		t.Errorf("happy path must not record a fallback: %#v", out)
	}
}

func TestPostInlineCommentFallsBackToNote(t *testing.T) {
	var noteBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{proj}/merge_requests/42/versions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"head_committed_sha": "h", "base_commit_sha": "b", "start_commit_sha": "s"}})
	})
	mux.HandleFunc("POST /api/v4/projects/{proj}/merge_requests/42/discussions", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message": "line_code not found"}`, http.StatusBadRequest) // stale position
	})
	mux.HandleFunc("POST /api/v4/projects/{proj}/merge_requests/42/notes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&noteBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 9}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	out, err := g.Do(context.Background(), "post_inline_comment", map[string]any{
		"project": "org/repo", "mr_iid": "42", "path": "a.go", "line": "7", "body": "nil deref",
	})
	if err != nil {
		t.Fatalf("fallback path must succeed: %v", err)
	}
	body, _ := noteBody["body"].(string)
	if !strings.Contains(body, "a.go:7") || !strings.Contains(body, "nil deref") {
		t.Errorf("fallback note body = %q, want path:line prefix + original body", body)
	}
	if out["fallback"] != "note" {
		t.Errorf("result must record the fallback: %#v", out)
	}
}

func TestPostInlineCommentBothFail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{proj}/merge_requests/42/versions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"head_committed_sha": "h", "base_commit_sha": "b", "start_commit_sha": "s"}})
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	if _, err := g.Do(context.Background(), "post_inline_comment", map[string]any{
		"project": "org/repo", "mr_iid": "42", "path": "a.go", "line": "7", "body": "x",
	}); err == nil {
		t.Fatal("expected error when both inline and fallback fail")
	}
}

func TestCreateIssue(t *testing.T) {
	var issueBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v4/projects/{proj}/issues", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&issueBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"iid": 17}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	out, err := g.Do(context.Background(), "create_issue", map[string]any{
		"project": "org/repo", "title": "SQL injection in search", "description": "evidence...", "labels": "code-audit,security",
	})
	if err != nil {
		t.Fatal(err)
	}
	if issueBody["title"] != "SQL injection in search" || issueBody["labels"] != "code-audit,security" {
		t.Errorf("issue payload = %#v", issueBody)
	}
	if out["iid"] != float64(17) {
		t.Errorf("result = %#v", out)
	}
}

func TestCreateIssueRequiresFields(t *testing.T) {
	g := newTestPlugin("http://unused")
	if _, err := g.Do(context.Background(), "create_issue", map[string]any{"project": "p"}); err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestReplyToDiscussion(t *testing.T) {
	var replyBody map[string]any
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&replyBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 3}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	if _, err := g.Do(context.Background(), "reply_to_discussion", map[string]any{
		"project": "org/repo", "mr_iid": "42", "discussion_id": "abc123", "body": "fixed in rev 2",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotPath, "/merge_requests/42/discussions/abc123/notes") {
		t.Errorf("path = %s", gotPath)
	}
	if replyBody["body"] != "fixed in rev 2" {
		t.Errorf("reply body = %#v", replyBody)
	}
}
```

NOTE on the `{proj}` pattern: the plugin URL-encodes `/`→`%2F` in project paths. Go's ServeMux decodes `%2F` within a segment, so `{proj}` matches `org%2Frepo` as one segment. If a route fails to match in practice, fall back to a single `mux.HandleFunc("/", ...)` dispatcher switching on `r.URL.EscapedPath()` — keep the same assertions.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/plugins/gitlab/ -run 'TestPostInline|TestCreateIssue|TestReply' -v`
Expected: FAIL — create_issue is a stub; post_inline_comment/reply_to_discussion unknown actions.

- [ ] **Step 3: Implement in `internal/plugins/gitlab/gitlab.go`**

1. Add a shared request helper (and refactor `postMRComment` to use it — targeted cleanup, do not change its behavior):

```go
// apiJSON sends a JSON request to the GitLab API and decodes the JSON response.
func (g *GitLabPlugin) apiJSON(ctx context.Context, method, path string, payload any) (map[string]any, int, error) {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("gitlab: marshal payload: %w", err)
		}
		bodyReader = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("gitlab: create request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("gitlab: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBody))
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("gitlab: %s %s returned %d: %s", method, path, resp.StatusCode, truncateForErr(respBody))
	}
	var result map[string]any
	_ = json.Unmarshal(respBody, &result)
	return result, resp.StatusCode, nil
}

func encodeProject(project string) string {
	return strings.ReplaceAll(project, "/", "%2F")
}
```

2. Action implementations:

```go
func (g *GitLabPlugin) postInlineComment(ctx context.Context, params map[string]any) (map[string]any, error) {
	project := paramToString(params["project"])
	mrIID := paramToString(params["mr_iid"])
	path := paramToString(params["path"])
	line := paramToString(params["line"])
	body := paramToString(params["body"])
	if project == "" || mrIID == "" || path == "" || line == "" || body == "" {
		return nil, fmt.Errorf("gitlab post_inline_comment: project, mr_iid, path, line, and body are required")
	}

	// Latest diff version supplies the position SHAs.
	versionsPath := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/versions", encodeProject(project), mrIID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+versionsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("gitlab: create request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: fetch versions: %w", err)
	}
	defer resp.Body.Close()
	vBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBody))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gitlab: versions returned %d: %s", resp.StatusCode, truncateForErr(vBody))
	}
	var versions []struct {
		HeadSHA  string `json:"head_committed_sha"`
		BaseSHA  string `json:"base_commit_sha"`
		StartSHA string `json:"start_commit_sha"`
	}
	if err := json.Unmarshal(vBody, &versions); err != nil || len(versions) == 0 {
		return nil, fmt.Errorf("gitlab: no diff versions for MR %s", mrIID)
	}
	v := versions[0] // GitLab returns newest first

	discussionsPath := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/discussions", encodeProject(project), mrIID)
	// GitLab's JSON API expects new_line as an integer; params arrive as
	// strings from templates ({{.Item.line}}) — coerce when numeric.
	var newLine any = line
	if n, err := strconv.Atoi(line); err == nil {
		newLine = n
	}
	payload := map[string]any{
		"body": body,
		"position": map[string]any{
			"position_type": "text",
			"head_sha":      v.HeadSHA,
			"base_sha":      v.BaseSHA,
			"start_sha":     v.StartSHA,
			"new_path":      path,
			"new_line":      newLine,
		},
	}
	result, status, err := g.apiJSON(ctx, http.MethodPost, discussionsPath, payload)
	if err == nil {
		return result, nil
	}
	// Stale line numbers are routine (the diff moved): fall back to a plain
	// note carrying the location so the finding is never lost.
	if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
		note, nErr := g.postMRComment(ctx, map[string]any{
			"project": project, "mr_iid": mrIID,
			"body": fmt.Sprintf("**%s:%s** — %s", path, line, body),
		})
		if nErr != nil {
			return nil, fmt.Errorf("gitlab: inline position rejected (%v) and note fallback failed: %w", err, nErr)
		}
		if note == nil {
			note = map[string]any{}
		}
		note["fallback"] = "note"
		return note, nil
	}
	return nil, err
}

func (g *GitLabPlugin) createIssue(ctx context.Context, params map[string]any) (map[string]any, error) {
	project := paramToString(params["project"])
	title := paramToString(params["title"])
	description := paramToString(params["description"])
	labels := paramToString(params["labels"]) // optional, comma-separated
	if project == "" || title == "" {
		return nil, fmt.Errorf("gitlab create_issue: project and title are required")
	}
	payload := map[string]any{"title": title, "description": description}
	if labels != "" {
		payload["labels"] = labels
	}
	result, _, err := g.apiJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%s/issues", encodeProject(project)), payload)
	return result, err
}

func (g *GitLabPlugin) replyToDiscussion(ctx context.Context, params map[string]any) (map[string]any, error) {
	project := paramToString(params["project"])
	mrIID := paramToString(params["mr_iid"])
	discussionID := paramToString(params["discussion_id"])
	body := paramToString(params["body"])
	if project == "" || mrIID == "" || discussionID == "" || body == "" {
		return nil, fmt.Errorf("gitlab reply_to_discussion: project, mr_iid, discussion_id, and body are required")
	}
	result, _, err := g.apiJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/discussions/%s/notes", encodeProject(project), mrIID, url.PathEscape(discussionID)),
		map[string]any{"body": body})
	return result, err
}
```

3. Wire `Do` and `Actions()`:

```go
	switch action {
	case "post_mr_comment":
		return g.postMRComment(ctx, params)
	case "post_inline_comment":
		return g.postInlineComment(ctx, params)
	case "create_issue":
		return g.createIssue(ctx, params)
	case "reply_to_discussion":
		return g.replyToDiscussion(ctx, params)
	case "resolve_discussion":
		return nil, fmt.Errorf("gitlab: %s not yet implemented", action)
	default:
		return nil, fmt.Errorf("gitlab: unknown action %q", action)
	}
```

```go
func (g *GitLabPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "post_mr_comment", Description: "Post a comment on a merge request"},
		{Name: "post_inline_comment", Description: "Post a positioned diff comment (falls back to a plain note on stale positions)"},
		{Name: "create_issue", Description: "Create a GitLab issue"},
		{Name: "reply_to_discussion", Description: "Reply to a merge request discussion thread"},
		{Name: "resolve_discussion", Description: "Resolve a discussion thread (stub)"},
	}
}
```

4. Refactor `postMRComment` to use `apiJSON` (same params/validation/behavior, body becomes two lines). Add `"net/url"` to imports.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/plugins/gitlab/ -count=1`
Expected: PASS (new + all existing — postMRComment refactor must not break its tests).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/plugins/gitlab
git commit -m "feat(sp5): gitlab post_inline_comment (with note fallback), create_issue, reply_to_discussion"
```

---

### Task 4: GitLab `mr_note` webhook events + bot_username

**Files:**
- Modify: `internal/plugins/gitlab/gitlab.go` (config: bot_username; EventSources)
- Modify: `internal/plugins/gitlab/events.go` (note payload handling)
- Test: `internal/plugins/gitlab/events_test.go` (extend; follow the existing webhook fixture pattern)

- [ ] **Step 1: Write the failing tests**

Read the existing webhook tests first (they post fixture JSON with the X-Gitlab-Token header — reuse the helper). Append:

```go
const noteWebhookFixture = `{
  "object_kind": "note",
  "user": {"username": "alice"},
  "project": {"path_with_namespace": "org/repo"},
  "merge_request": {"iid": 42, "title": "Add rate limiter", "author_id": 7, "source_branch": "feat/limiter"},
  "object_attributes": {
    "id": 9001,
    "discussion_id": "abc123",
    "note": "Should this handle burst traffic?",
    "noteable_type": "MergeRequest",
    "url": "https://gitlab.com/org/repo/-/merge_requests/42#note_9001"
  }
}`

func TestHandleWebhookMRNote(t *testing.T) {
	g := &GitLabPlugin{webhookSecret: "s3cret"}
	req := webhookRequest(t, noteWebhookFixture, "s3cret") // adapt: existing fixture-post helper
	events, err := g.HandleWebhook(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != "mr_note" {
		t.Errorf("event_type = %s", ev.EventType)
	}
	if ev.DedupKey != "note:org/repo:9001" {
		t.Errorf("dedup key = %s", ev.DedupKey)
	}
	if ev.Identity != "alice" {
		t.Errorf("identity = %s", ev.Identity)
	}
	for k, want := range map[string]any{
		"project": "org/repo", "mr_iid": 42, "note_id": 9001,
		"discussion_id": "abc123", "note_body": "Should this handle burst traffic?",
		"author": "alice", "mr_title": "Add rate limiter", "mr_source_branch": "feat/limiter",
	} {
		if fmt.Sprint(ev.PayloadNorm[k]) != fmt.Sprint(want) {
			t.Errorf("payload_norm[%s] = %v, want %v", k, ev.PayloadNorm[k], want)
		}
	}
}

func TestHandleWebhookNoteFromBotDropped(t *testing.T) {
	g := &GitLabPlugin{webhookSecret: "s3cret", botUsername: "fleet-bot"}
	fixture := strings.Replace(noteWebhookFixture, `"username": "alice"`, `"username": "fleet-bot"`, 1)
	req := webhookRequest(t, fixture, "s3cret")
	events, err := g.HandleWebhook(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("bot's own note must be dropped, got %d events", len(events))
	}
}

func TestHandleWebhookNonMRNoteIgnored(t *testing.T) {
	g := &GitLabPlugin{webhookSecret: "s3cret"}
	fixture := strings.Replace(noteWebhookFixture, `"noteable_type": "MergeRequest"`, `"noteable_type": "Issue"`, 1)
	req := webhookRequest(t, fixture, "s3cret")
	events, err := g.HandleWebhook(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("issue notes are out of scope, got %d events", len(events))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/plugins/gitlab/ -run TestHandleWebhook -v`
Expected: new tests FAIL (note payloads currently ignored as non-MR object_kind; botUsername undefined).

- [ ] **Step 3: Implement**

`gitlab.go`: add `botUsername string` to the struct; in `Init`, after poll config:

```go
	if v, ok := cfg["bot_username"].(string); ok {
		g.botUsername = v
	}
```

Add `"bot_username"` to `ConfigSchema()` properties (`{"type": "string"}`, description: the fleet's own GitLab username; its notes are dropped at ingestion to prevent reply loops). Extend `EventSources()`:

```go
		{Name: "mr_notes", Description: "Merge request comment (note) events (webhook only)"},
```

`events.go`: add the payload type and handling. After the existing `if payload.ObjectKind != "merge_request"` check becomes a switch — restructure `HandleWebhook`'s tail:

```go
	var kindProbe struct {
		ObjectKind string `json:"object_kind"`
	}
	if err := json.Unmarshal(body, &kindProbe); err != nil {
		return nil, fmt.Errorf("gitlab: parse webhook: %w", err)
	}
	switch kindProbe.ObjectKind {
	case "merge_request":
		var payload webhookMRPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("gitlab: parse webhook: %w", err)
		}
		eventType, ok := actionToEventType(payload.ObjectAttributes.Action)
		if !ok {
			return nil, nil // unhandled action; acknowledged and ignored
		}
		a := payload.ObjectAttributes
		ev := normalizeMR(eventType, payload.Project.PathWithNamespace, a.IID, a.Title, a.Action,
			a.SourceBranch, a.TargetBranch, a.LastCommit.ID, payload.User.Username, a.URL, body)
		return []domain.Event{ev}, nil
	case "note":
		return g.handleNoteWebhook(body)
	default:
		return nil, nil // not an event kind we ingest; acknowledged and ignored
	}
```

(Adjust the original function so the existing MR behavior is byte-for-byte preserved; only the dispatch is new.) Then:

```go
type webhookNotePayload struct {
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	MergeRequest struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		SourceBranch string `json:"source_branch"`
	} `json:"merge_request"`
	ObjectAttributes struct {
		ID           int    `json:"id"`
		DiscussionID string `json:"discussion_id"`
		Note         string `json:"note"`
		NoteableType string `json:"noteable_type"`
		URL          string `json:"url"`
	} `json:"object_attributes"`
}

// handleNoteWebhook ingests MR comments as mr_note events. Notes by the
// configured bot_username are dropped here so the mr-feedback duty can never
// be triggered by its own replies (reply-loop protection).
func (g *GitLabPlugin) handleNoteWebhook(body []byte) ([]domain.Event, error) {
	var payload webhookNotePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("gitlab: parse note webhook: %w", err)
	}
	if payload.ObjectAttributes.NoteableType != "MergeRequest" {
		return nil, nil // issue/commit/snippet notes are out of scope
	}
	if g.botUsername != "" && payload.User.Username == g.botUsername {
		return nil, nil // our own note: drop to prevent reply loops
	}
	a := payload.ObjectAttributes
	return []domain.Event{{
		SourcePlugin: "gitlab",
		EventType:    "mr_note",
		PayloadRaw:   json.RawMessage(body),
		PayloadNorm: map[string]any{
			"project":          payload.Project.PathWithNamespace,
			"mr_iid":           payload.MergeRequest.IID,
			"mr_title":         payload.MergeRequest.Title,
			"mr_source_branch": payload.MergeRequest.SourceBranch,
			"note_id":          a.ID,
			"discussion_id":    a.DiscussionID,
			"note_body":        a.Note,
			"author":           payload.User.Username,
			"url":              a.URL,
		},
		Identity: payload.User.Username,
		DedupKey: fmt.Sprintf("note:%s:%d", payload.Project.PathWithNamespace, a.ID),
	}}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/plugins/gitlab/ -count=1 && go test ./... -count=1`
Expected: PASS (every pre-existing webhook test must still pass — the MR path is unchanged).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/plugins/gitlab
git commit -m "feat(sp5): gitlab mr_note webhook events with bot-loop protection"
```

---

### Task 5: Config validation — `for_each` is a bare key

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (extend)

- [ ] **Step 1: Write the failing test**

```go
func TestValidateForEach(t *testing.T) {
	base := func(forEach string) *Config {
		return &Config{
			Agents: []AgentConfig{{Name: "a", Enabled: true}},
			Duties: []DutyConfig{{Name: "d"}},
			Assignments: []AssignmentConfig{{
				Agent: "a", Duty: "d",
				Trigger: domain.TriggerConfig{Kind: "manual"},
				Outputs: []domain.OutputBinding{{Plugin: "gitlab", Action: "create_issue", ForEach: forEach}},
			}},
		}
	}
	if errs := Validate(base("issues")); len(errs) != 0 {
		t.Fatalf("bare key must validate: %v", errs)
	}
	for _, bad := range []string{"{{.Event.x}}", "issues[0]", "a b"} {
		errs := Validate(base(bad))
		found := false
		for _, e := range errs {
			if strings.Contains(e.Error(), "for_each") {
				found = true
			}
		}
		if !found {
			t.Errorf("for_each %q: expected a validation error, got %v", bad, errs)
		}
	}
}
```

(Check the existing assignment-validation tests for the minimal valid Assignment shape — `Trigger.Kind` values are validated; adapt the base if "manual" requires more fields. The point under test is only the for_each format.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run TestValidateForEach -v`
Expected: FAIL (no for_each validation yet; possibly compile error if ForEach not yet merged — Task 2 added it).

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add near the top-level vars:

```go
// forEachKeyRe: for_each names a key of the LLM result's output object — a
// bare identifier, never a template or path expression.
var forEachKeyRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
```

Inside the existing per-assignment validation loop (find where each assignment's fields are checked), add:

```go
		for _, out := range a.Outputs {
			if out.ForEach != "" && !forEachKeyRe.MatchString(out.ForEach) {
				errs = append(errs, fmt.Errorf("assignment (%s, %s): for_each %q must be a bare output key (letters, digits, underscore)", a.Agent, a.Duty, out.ForEach))
			}
		}
```

(`regexp` is already imported for envRefRe.)

- [ ] **Step 4: Run + commit**

Run: `go test ./internal/config/ -count=1` — PASS.

```bash
gofmt -l . && go vet ./...
git add internal/config
git commit -m "feat(sp5): validate for_each as a bare output key"
```

---

### Task 6: Sample config — developer agent, three duties, three assignments

**Files:**
- Modify: `configs/fleet.yaml` (replace the agents/duties/assignments sections; add bot_username)
- Test: sample-config test — `grep -rn "fleet.yaml" internal/ --include=*_test.go` first; extend the existing test if one loads the sample, else create `internal/config/sample_test.go`

- [ ] **Step 1: Rewrite the sample sections**

In `configs/fleet.yaml`, REPLACE everything from `agents:` through the end of the `assignments:` section (keep the trailing commented GitHub/Slack example block if convenient, or drop it — it's illustrative; KEEP the `plugins:` section and the commented plugin examples). The new content:

```yaml
agents:
  - name: dev-1
    role: developer
    enabled: true
    default_backend:
      name: claude-default
    system_prompt: |
      You are dev-1, a senior software engineer. You review code and triage
      findings with the discipline of a staff engineer:
      - Real bugs, security holes, and correctness gaps over style nits.
      - Every claim verified against the actual code before you report it —
        if you cannot point at the line that proves the problem, drop it.
      - Terse, actionable comments: one concern per comment, no filler,
        no "Great work!" padding.
      - When unsure, say so briefly rather than inventing certainty.

duties:
  # ── 1. MR review: inline comments + a verdict summary ──────────────────
  - name: mr-review
    role: developer
    description: Review a merge request; post inline comments and a verdict summary.
    trigger_kinds:
      - manual
      - event-subscription
    required_tools:
      - git
      - glab
    prompt: |
      Review merge request !{{.Event.mr_iid}} ({{.Event.title}}) in
      {{.Assignment.project}}.

      Workspace setup:
        git clone --depth 50 https://oauth2:{{secret "gitlab_token"}}@gitlab.com/{{.Assignment.project}}.git repo
        cd repo
        git fetch origin {{.Event.source_branch}} {{.Event.target_branch}}
        git checkout {{.Event.source_branch}}
      Review the change: `git diff origin/{{.Event.target_branch}}...HEAD`.
      Read surrounding code wherever the diff alone is ambiguous.

      Review discipline:
      - Look for correctness bugs, unhandled errors, race conditions,
        security problems (injection, credential leaks, unsafe deserialization),
        and resource leaks. Style nits only when they hide a real risk.
      - Verify every finding against the code. Re-read the exact lines before
        reporting; drop anything speculative.
      - At most 10 comments; severity one of high|medium|low; `line` is the
        line number in the NEW file version.
      - If the change is clean, return zero comments and say so in the verdict.

      Report your result as a single JSON object:
        {"summary": "<one-paragraph verdict posted as the review summary>",
         "comments": [{"path": "<file>", "line": <int>, "severity": "high|medium|low",
                       "body": "<severity-tagged, actionable comment>"}]}
      If you have a submit_result tool, call it with this object as the
      `output` parameter (and the verdict as `summary`). Otherwise end your
      final message with exactly one fenced ```json code block containing it.
    output_actions:
      - plugin: gitlab
        action: post_inline_comment
      - plugin: gitlab
        action: post_mr_comment

  # ── 2. Code audit: scheduled category scan → GitLab issues ─────────────
  - name: code-audit
    role: developer
    description: Audit the codebase for one category of defects; file an issue per verified finding.
    trigger_kinds:
      - manual
      - cron
    required_tools:
      - git
      - glab
    config_schema:
      type: object
      properties:
        project:
          type: string
          description: GitLab project path (namespace/repo)
        category:
          type: string
          enum: [security, correctness, performance]
      required: [project, category]
    prompt: |
      Audit {{.Assignment.project}} for {{.Assignment.category}} defects.

      Workspace setup:
        git clone --depth 1 https://oauth2:{{secret "gitlab_token"}}@gitlab.com/{{.Assignment.project}}.git repo
        cd repo
      Load existing findings first so you never duplicate them:
        GITLAB_TOKEN={{secret "gitlab_token"}} glab issue list --label code-audit --repo {{.Assignment.project}} --per-page 100

      Audit discipline (category: {{.Assignment.category}}):
      - security: injection, authn/authz gaps, secret handling, unsafe
        deserialization, SSRF, path traversal.
      - correctness: logic errors, unhandled errors, race conditions,
        off-by-one and boundary bugs, broken invariants.
      - performance: O(n^2) hot paths, unbounded memory growth, missing
        indexes/caps, chatty I/O in loops.
      - Verify every candidate against the code: re-read the lines, trace the
        callers, and drop anything you cannot prove. Report only findings a
        staff engineer would file. Skip anything already covered by an open
        code-audit issue.

      Report your result as a single JSON object:
        {"summary": "<one-paragraph audit summary>",
         "issues": [{"title": "<specific, file-scoped title>",
                     "description": "<evidence: file:line refs, why it's real, suggested fix>",
                     "labels": "code-audit,{{.Assignment.category}}"}]}
      If you have a submit_result tool, call it with this object as the
      `output` parameter (and the summary as `summary`). Otherwise end your
      final message with exactly one fenced ```json code block containing it.
    output_actions:
      - plugin: gitlab
        action: create_issue

  # ── 3. MR feedback: reply to review comments ───────────────────────────
  - name: mr-feedback
    role: developer
    description: Reply to a comment on a merge request (zero or one reply).
    trigger_kinds:
      - event-subscription
    required_tools:
      - git
    prompt: |
      A comment arrived on merge request !{{.Event.mr_iid}}
      ({{.Event.mr_title}}) in {{.Assignment.project}}:

        {{.Event.author}} wrote: {{.Event.note_body}}

      Decide whether a reply from you is warranted:
      - Questions about the code or the review: answer concretely. If the
        answer needs code context, clone and inspect:
          git clone --depth 50 https://oauth2:{{secret "gitlab_token"}}@gitlab.com/{{.Assignment.project}}.git repo
          cd repo && git fetch origin {{.Event.mr_source_branch}} && git checkout {{.Event.mr_source_branch}}
      - Requests for changes: describe precisely what should change and where
        (you cannot push commits).
      - Thanks, acknowledgements, bot chatter, or anything not addressed to
        the reviewer: do NOT reply.
      Never reply to your own comments.

      Report your result as a single JSON object:
        {"summary": "<one line: replied or skipped, and why>",
         "replies": []}            ← empty when no reply is warranted
        or
        {"summary": "...", "replies": [{"body": "<the reply>"}]}
      If you have a submit_result tool, call it with this object as the
      `output` parameter. Otherwise end your final message with exactly one
      fenced ```json code block containing it.
    output_actions:
      - plugin: gitlab
        action: reply_to_discussion

assignments:
  # MR review on every newly-opened MR. (Add a second assignment with
  # event_type: mr_updated to also re-review on new pushes.)
  - agent: dev-1
    duty: mr-review
    enabled: true
    trigger:
      kind: event-subscription
      filter:
        source: gitlab
        event_type: mr_opened
        project: "myorg/myrepo"
    config:
      project: "myorg/myrepo"
    outputs:
      - plugin: gitlab
        action: post_inline_comment
        for_each: comments
        params:
          project: "{{.Assignment.project}}"
          mr_iid: "{{.Event.mr_iid}}"
          path: "{{.Item.path}}"
          line: "{{.Item.line}}"
          body: "**{{.Item.severity}}** — {{.Item.body}}"
      - plugin: gitlab
        action: post_mr_comment
        params:
          project: "{{.Assignment.project}}"
          mr_iid: "{{.Event.mr_iid}}"
          body: "{{.Event.llm_summary}}"

  # Weekly security audit (Mondays 06:00). Duplicate this assignment with
  # category: correctness / performance for broader coverage.
  - agent: dev-1
    duty: code-audit
    enabled: true
    trigger:
      kind: cron
      schedule: "0 6 * * 1"
    config:
      project: "myorg/myrepo"
      category: "security"
    outputs:
      - plugin: gitlab
        action: create_issue
        for_each: issues
        params:
          project: "{{.Assignment.project}}"
          title: "{{.Item.title}}"
          description: "{{.Item.description}}"
          labels: "{{.Item.labels}}"

  # Reply to MR comments (the bot's own notes are dropped at ingestion via
  # plugins.gitlab.bot_username — set it to the fleet's GitLab username).
  - agent: dev-1
    duty: mr-feedback
    enabled: true
    trigger:
      kind: event-subscription
      filter:
        source: gitlab
        event_type: mr_note
        project: "myorg/myrepo"
    config:
      project: "myorg/myrepo"
    outputs:
      - plugin: gitlab
        action: reply_to_discussion
        for_each: replies
        params:
          project: "{{.Assignment.project}}"
          mr_iid: "{{.Event.mr_iid}}"
          discussion_id: "{{.Event.discussion_id}}"
          body: "{{.Item.body}}"
```

And in the `plugins:` section, extend the gitlab entry:

```yaml
plugins:
  - name: gitlab
    config:
      base_url: "https://gitlab.com"
      bot_username: "fleet-bot"   # the fleet's own GitLab user; its MR notes are dropped (reply-loop protection)
#      poll_interval: 60s
#      poll_projects: ["myorg/myrepo"]
```

NOTE: keep `${env:...}` OUT of any comments you add (Load expands env refs on raw bytes including comments).

- [ ] **Step 2: Write/extend the sample-config test**

`grep -rn "fleet.yaml" internal/ --include=*_test.go`. If a test already loads the sample, extend it; otherwise create `internal/config/sample_test.go`:

```go
package config

import (
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/prompt"
)

// TestSampleConfig pins the shipped sample: it must parse, validate, and its
// duty prompts must render against a realistic context (catches {{.Event.x}}
// typos and template syntax errors at test time, not at first live run).
func TestSampleConfig(t *testing.T) {
	t.Setenv("FLEET_DATABASE_DSN", "postgres://test")
	cfg, err := Load("../../configs/fleet.yaml")
	if err != nil {
		t.Fatalf("sample config must load: %v", err)
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("sample config must validate: %v", errs)
	}

	wantDuties := map[string]bool{"mr-review": false, "code-audit": false, "mr-feedback": false}
	syntheticCtx := prompt.Context{
		Event: map[string]any{
			"mr_iid": 42, "title": "Add limiter", "mr_title": "Add limiter",
			"source_branch": "feat/x", "target_branch": "main", "mr_source_branch": "feat/x",
			"note_body": "why this approach?", "author": "alice", "discussion_id": "abc",
		},
		Agent:      map[string]any{"name": "dev-1", "role": "developer"},
		Duty:       map[string]any{},
		Assignment: map[string]any{"project": "org/repo", "category": "security"},
		State:      map[string]any{},
		Now:        time.Now(),
		Secrets:    map[string]string{"gitlab_token": "tok"},
	}
	for _, d := range cfg.Duties {
		if _, tracked := wantDuties[d.Name]; tracked {
			wantDuties[d.Name] = true
		}
		rendered, err := prompt.Render(d.Prompt, syntheticCtx)
		if err != nil {
			t.Errorf("duty %q prompt does not render: %v", d.Name, err)
			continue
		}
		if strings.Contains(rendered, "<no value>") {
			t.Errorf("duty %q prompt rendered a missing field (<no value>):\n%s", d.Name, rendered)
		}
	}
	for name, seen := range wantDuties {
		if !seen {
			t.Errorf("sample config missing duty %q", name)
		}
	}

	// Every assignment output param template must render too.
	for _, a := range cfg.Assignments {
		for _, out := range a.Outputs {
			for key, v := range out.Params {
				s, ok := v.(string)
				if !ok {
					continue
				}
				itemCtx := syntheticCtx
				itemCtx.Item = map[string]any{
					"path": "a.go", "line": 7, "severity": "high", "body": "x",
					"title": "t", "description": "d", "labels": "l",
				}
				if _, err := prompt.Render(s, itemCtx); err != nil {
					t.Errorf("assignment (%s,%s) output param %q does not render: %v", a.Agent, a.Duty, key, err)
				}
			}
		}
	}
}
```

NOTE: `{{.Event.llm_summary}}` renders empty against this context (key absent → `<no value>`)? Go templates render missing MAP keys as `<no value>` — the prompt-render check above tolerates that ONLY in duty prompts; for output params the test only checks render errors (missing keys are fine — delivery enriches the real context). If the duty-prompt `<no value>` check trips on a key the synthetic context lacks, ADD the key to the synthetic context rather than weakening the check.

- [ ] **Step 3: Run**

Run: `go test ./internal/config/ -run TestSampleConfig -v -count=1`
Expected: PASS (fix any template typo the test catches — that is its job).

Also run the full suite: `go test ./... -count=1`. The seed path consumes the same structs — if a seed test references the old `mr-reviewer` duty by name, update it to the new duty names and note it in your report.

- [ ] **Step 4: Commit**

```bash
gofmt -l . && go vet ./...
git add configs/fleet.yaml internal/config
git commit -m "feat(sp5): sample developer agent with mr-review, code-audit, mr-feedback duties"
```

---

### Task 7: Final gate

**Files:** none new — verification only (plus fixes it surfaces).

- [ ] **Step 1: Full automated gate**

```bash
NODE_OPTIONS= make test            # go test ./... + tsc + vitest
gofmt -l .                          # nothing
go vet ./...                        # clean
git diff --stat c491d29 -- go.mod go.sum   # c491d29 = SP5 spec commit (pre-implementation); must be EMPTY
NODE_OPTIONS= make build && git status --short   # clean worktree
```

- [ ] **Step 2: Acceptance criteria map (spec §8)**

1. Fan-out per-item deliveries + `{{.Item.*}}` + 50-cap + empty-list-zero: `internal/outputs` tests (Task 2).
2. Inline comment + fallback; create_issue real; reply_to_discussion: gitlab httptest suite (Task 3).
3. `mr_note` envelope + dedup key + bot drop: webhook tests (Task 4).
4. Sample config parses/validates/renders; three duties + developer agent present: TestSampleConfig (Task 6).
5. mr-feedback zero-or-one replies semantics: fan-out empty-list test (Task 2) + the sample's `for_each: replies` binding (Task 6).
6. Suites green, gofmt/vet clean, zero new deps, make test end-to-end: Step 1.

Claude-executor JSON extraction (Task 1) is the enabling work for ACs 1/5 on the default backend — confirm its tests pass in the suite run.

- [ ] **Step 3: Commit any fixes**

```bash
git add -A && git status --short   # review carefully
git commit -m "test(sp5): final gate fixes"   # only if there were fixes
```

---

## Self-review checklist

- **Spec coverage:** §3 fan-out → Task 2 (+ Task 5 validation); §4 actions/events → Tasks 3–4; §5 duties → Task 6; §6 error table → Tasks 2–4 tests; §7 testing → mirrored per task; §8 ACs → Task 7 map. Spec-silent but required: claude executor structured-output extraction → Task 1 (recorded as an SP5 addition; the spec's structured-result contract presumes it).
- **Type consistency:** `OutputBinding.ForEach` (Task 2) used by Tasks 5–6 yaml as `for_each`; `prompt.Context.Item` (Task 2) consumed by renderParams + sample test; `deliverOne(ctx, out, result, promptCtx, item)` signature consistent; gitlab `apiJSON` returns `(map[string]any, int, error)` used by all three actions; `botUsername` field (Task 4) matches the test's struct literal.
- **No placeholders:** all code complete; test helpers that must adapt to existing names are explicitly marked with the contract to preserve.
- **Known simplification (recorded):** the sample's mr-review subscribes to `mr_opened` only (mr_updated variant is a comment); `resolve_discussion` remains a stub per spec §9.

