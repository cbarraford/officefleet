package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if call == 0 {
			if _, ok := body["tools"]; !ok {
				t.Error("first request missing tools field")
			}
		}
		if msgs, ok := body["messages"].([]any); ok && call == len(responses)-1 && len(msgs) <= 2 {
			t.Errorf("last request has %d messages; expected conversation growth", len(msgs))
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
	data, rerr := os.ReadFile(filepath.Join(ws, "out.txt"))
	if rerr != nil || !strings.Contains(string(data), "workfile") {
		t.Errorf("workspace write missing: content=%q err=%v", data, rerr)
	}
	if !strings.Contains(result.Transcript, "workfile") {
		t.Error("transcript missing read_file observation content")
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
