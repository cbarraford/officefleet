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
