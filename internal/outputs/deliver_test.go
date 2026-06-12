package outputs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/outputs"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/prompt"
)

// mockPlugin is a test plugin that records Do calls.
type mockPlugin struct {
	calls []map[string]any
	err   error
}

func (m *mockPlugin) Name() string                       { return "mock" }
func (m *mockPlugin) EventSources() []plugin.EventSource { return nil }
func (m *mockPlugin) Actions() []plugin.Action           { return nil }
func (m *mockPlugin) ConfigSchema() plugin.Schema        { return nil }
func (m *mockPlugin) Init(_ context.Context, _ map[string]any, _ plugin.SecretLookup) error {
	return nil
}
func (m *mockPlugin) Do(_ context.Context, _ string, params map[string]any) (map[string]any, error) {
	m.calls = append(m.calls, params)
	return map[string]any{"ok": true}, m.err
}

func TestDeliver_Success(t *testing.T) {
	mock := &mockPlugin{}
	plugin.Register(mock)

	result := domain.LLMResult{Summary: "LGTM", Output: map[string]any{}}
	outputBindings := []domain.OutputBinding{
		{Plugin: "mock", Action: "post", Params: map[string]any{"body": "{{.Event.llm_summary}}"}},
	}
	ctx := prompt.Context{Event: map[string]any{}}

	deliveries := outputs.Deliver(context.Background(), outputBindings, result, ctx)
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(deliveries))
	}
	if deliveries[0].Status != "delivered" {
		t.Fatalf("expected delivered, got %q: %s", deliveries[0].Status, deliveries[0].Error)
	}
	if len(mock.calls) != 1 || mock.calls[0]["body"] != "LGTM" {
		t.Fatalf("plugin not called with rendered params: %v", mock.calls)
	}
	if deliveries[0].Params["body"] != "LGTM" {
		t.Fatalf("expected delivery.Params[\"body\"] == \"LGTM\", got %v", deliveries[0].Params["body"])
	}
}

func TestDeliver_PluginDoError(t *testing.T) {
	doErr := errors.New("plugin exploded")
	mock := &mockPlugin{err: doErr}
	plugin.Register(mock)

	result := domain.LLMResult{Summary: "ok", Output: map[string]any{}}
	outputBindings := []domain.OutputBinding{
		{Plugin: "mock", Action: "post", Params: map[string]any{}},
	}
	deliveries := outputs.Deliver(context.Background(), outputBindings, result, prompt.Context{})
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(deliveries))
	}
	if deliveries[0].Status != "failed" {
		t.Fatalf("expected status=failed, got %q", deliveries[0].Status)
	}
	if !strings.Contains(deliveries[0].Error, "plugin exploded") {
		t.Fatalf("expected error to contain %q, got %q", "plugin exploded", deliveries[0].Error)
	}
}

func TestDeliver_UnknownPlugin(t *testing.T) {
	result := domain.LLMResult{Summary: "ok"}
	outputBindings := []domain.OutputBinding{
		{Plugin: "nonexistent", Action: "post", Params: map[string]any{}},
	}
	deliveries := outputs.Deliver(context.Background(), outputBindings, result, prompt.Context{})
	if len(deliveries) != 1 || deliveries[0].Status != "failed" {
		t.Fatalf("expected failed delivery for unknown plugin: %+v", deliveries)
	}
}

func TestDeliver_RenderParamsError(t *testing.T) {
	result := domain.LLMResult{Summary: "ok"}
	outputBindings := []domain.OutputBinding{
		{Plugin: "mock", Action: "post", Params: map[string]any{"body": "{{.Event.unclosed"}},
	}
	deliveries := outputs.Deliver(context.Background(), outputBindings, result, prompt.Context{Event: map[string]any{}})
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(deliveries))
	}
	d := deliveries[0]
	if d.Status != "failed" {
		t.Fatalf("expected status=failed, got %q", d.Status)
	}
	if !strings.HasPrefix(d.Error, "render params:") {
		t.Fatalf("expected error to start with \"render params:\", got %q", d.Error)
	}
}

func TestDeliver_DeniesSecretHelperInParams(t *testing.T) {
	result := domain.LLMResult{Summary: "ok"}
	outputBindings := []domain.OutputBinding{
		{Plugin: "mock", Action: "post", Params: map[string]any{"body": `{{secret "api_token"}}`}},
	}
	ctx := prompt.WithSecrets(prompt.Context{Event: map[string]any{}}, map[string]string{"api_token": "tok-abc123"})

	deliveries := outputs.Deliver(context.Background(), outputBindings, result, ctx)
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(deliveries))
	}
	if deliveries[0].Status != "failed" {
		t.Fatalf("expected secret helper delivery to fail, got %+v", deliveries[0])
	}
	if !strings.Contains(deliveries[0].Error, "secrets not available") {
		t.Fatalf("error = %q, want secrets-denied message", deliveries[0].Error)
	}
}

// TestDeliver_LLMOutputRenderedAsJSON asserts that {{.Event.llm_output}} produces
// valid JSON rather than Go's map literal syntax (e.g. "map[key:value]").
func TestDeliver_LLMOutputRenderedAsJSON(t *testing.T) {
	mock := &mockPlugin{}
	plugin.Register(mock)

	result := domain.LLMResult{
		Summary: "ok",
		Output:  map[string]any{"key": "value", "num": float64(42)},
	}
	outputBindings := []domain.OutputBinding{
		{Plugin: "mock", Action: "post", Params: map[string]any{"body": "{{.Event.llm_output}}"}},
	}
	ctx := prompt.Context{Event: map[string]any{}}

	deliveries := outputs.Deliver(context.Background(), outputBindings, result, ctx)
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(deliveries))
	}
	if deliveries[0].Status != "delivered" {
		t.Fatalf("expected delivered, got %q: %s", deliveries[0].Status, deliveries[0].Error)
	}
	body, ok := mock.calls[len(mock.calls)-1]["body"].(string)
	if !ok {
		t.Fatalf("body param is not a string: %T", mock.calls[len(mock.calls)-1]["body"])
	}
	// Must be valid JSON, not Go map syntax like "map[key:value]".
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("llm_output rendered as non-JSON %q: %v", body, err)
	}
}

// fakePlugin is a recording plugin for fan-out tests. It is registered
// under the name "fake" and tracks call count atomically.
type fakePlugin struct {
	mu    sync.Mutex
	calls []map[string]any
}

func (f *fakePlugin) Name() string                       { return "fake" }
func (f *fakePlugin) EventSources() []plugin.EventSource { return nil }
func (f *fakePlugin) Actions() []plugin.Action           { return nil }
func (f *fakePlugin) ConfigSchema() plugin.Schema        { return nil }
func (f *fakePlugin) Init(_ context.Context, _ map[string]any, _ plugin.SecretLookup) error {
	return nil
}
func (f *fakePlugin) Do(_ context.Context, _ string, params map[string]any) (map[string]any, error) {
	f.mu.Lock()
	f.calls = append(f.calls, params)
	f.mu.Unlock()
	return map[string]any{"ok": true}, nil
}
func (f *fakePlugin) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakePluginHandle is returned by registerFakePlugin so tests can inspect the plugin.
type fakePluginHandle struct {
	name   string
	plugin *fakePlugin
}

func (h *fakePluginHandle) callCount() int { return h.plugin.callCount() }

// registerFakePlugin creates a fresh fakePlugin, registers it, and returns a handle.
// The plugin is registered globally; t.Cleanup is NOT needed since plugin.Register
// overwrites the previous entry under the same name.
func registerFakePlugin(_ *testing.T) *fakePluginHandle {
	fp := &fakePlugin{}
	plugin.Register(fp)
	return &fakePluginHandle{name: "fake", plugin: fp}
}

// mockPlugin2 is a second named mock for multi-binding tests.
type mockPlugin2 struct {
	calls []map[string]any
	err   error
}

func (m *mockPlugin2) Name() string                       { return "mock2" }
func (m *mockPlugin2) EventSources() []plugin.EventSource { return nil }
func (m *mockPlugin2) Actions() []plugin.Action           { return nil }
func (m *mockPlugin2) ConfigSchema() plugin.Schema        { return nil }
func (m *mockPlugin2) Init(_ context.Context, _ map[string]any, _ plugin.SecretLookup) error {
	return nil
}
func (m *mockPlugin2) Do(_ context.Context, _ string, params map[string]any) (map[string]any, error) {
	m.calls = append(m.calls, params)
	return map[string]any{"ok": true}, m.err
}

// TestDeliver_PartialFailure verifies requirement 4: Deliver never aborts early.
// When the first binding's plugin.Do returns an error, the second binding must
// still be executed and recorded as "delivered".
func TestDeliver_PartialFailure(t *testing.T) {
	first := &mockPlugin{err: errors.New("first plugin failed")}
	second := &mockPlugin2{}

	plugin.Register(first)
	plugin.Register(second)

	result := domain.LLMResult{Summary: "hello", Output: map[string]any{}}
	outputBindings := []domain.OutputBinding{
		{Plugin: "mock", Action: "post", Params: map[string]any{}},
		{Plugin: "mock2", Action: "post", Params: map[string]any{}},
	}

	deliveries := outputs.Deliver(context.Background(), outputBindings, result, prompt.Context{})
	if len(deliveries) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(deliveries))
	}
	if deliveries[0].Status != "failed" {
		t.Fatalf("expected first delivery status=failed, got %q", deliveries[0].Status)
	}
	if deliveries[1].Status != "delivered" {
		t.Fatalf("expected second delivery status=delivered, got %q: %s", deliveries[1].Status, deliveries[1].Error)
	}
}

func TestDeliverForEachFansOut(t *testing.T) {
	fake := registerFakePlugin(t)
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

	deliveries := outputs.Deliver(context.Background(), bindings, result, prompt.Context{})
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
			deliveries := outputs.Deliver(context.Background(), bindings, domain.LLMResult{Output: output}, prompt.Context{})
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

	deliveries := outputs.Deliver(context.Background(), bindings, result, prompt.Context{})
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

	deliveries := outputs.Deliver(context.Background(), bindings, result, prompt.Context{})
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

	deliveries := outputs.Deliver(context.Background(), bindings, domain.LLMResult{Output: map[string]any{"comments": items}}, prompt.Context{})
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
	deliveries := outputs.Deliver(context.Background(), bindings, domain.LLMResult{Summary: "hi"}, prompt.Context{})
	if len(deliveries) != 1 || deliveries[0].Status != "delivered" || deliveries[0].Params["body"] != "hi" {
		t.Fatalf("plain binding regression: %#v", deliveries)
	}
}
