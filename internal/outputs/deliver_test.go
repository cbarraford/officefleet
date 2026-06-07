package outputs_test

import (
	"context"
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

func (m *mockPlugin) Name() string                  { return "mock" }
func (m *mockPlugin) EventSources() []plugin.EventSource { return nil }
func (m *mockPlugin) Actions() []plugin.Action      { return nil }
func (m *mockPlugin) ConfigSchema() plugin.Schema   { return nil }
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
