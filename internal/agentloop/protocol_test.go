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
