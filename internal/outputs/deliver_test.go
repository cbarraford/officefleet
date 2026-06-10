package outputs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
