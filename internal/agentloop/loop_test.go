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

func TestRun_MultiCallTurnSubmitResultShortCircuits(t *testing.T) {
	// One assistant turn carries [run_command, submit_result, run_command]:
	// the first call executes, submit_result terminates, the third is dropped.
	client := &scriptedClient{responses: []ChatResponse{
		{
			Message: Message{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "c1", Name: "run_command", Args: map[string]any{"cmd": "ls"}},
				{ID: "c2", Name: "submit_result", Args: map[string]any{"summary": "done early", "status": float64(0)}},
				{ID: "c3", Name: "run_command", Args: map[string]any{"cmd": "never"}},
			}},
			Usage: Usage{PromptTokens: 10, CompletionTokens: 5},
		},
	}}
	bridge := &recordingBridge{}

	res, err := Run(context.Background(), client, bridge, Native,
		"s", "u", nil, Opts{Model: "m"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.Summary != "done early" || res.Status != 0 {
		t.Errorf("result = %+v", res)
	}
	if len(client.requests) != 1 {
		t.Errorf("chat calls = %d, want 1 (terminated mid-turn)", len(client.requests))
	}
	// c1 executed, c2 terminated, c3 never dispatched.
	if len(bridge.calls) != 2 {
		t.Fatalf("bridge calls = %d, want 2 (c1 + c2)", len(bridge.calls))
	}
	if bridge.calls[0].ID != "c1" || bridge.calls[1].ID != "c2" {
		t.Errorf("bridge call order = %+v", bridge.calls)
	}
}

func TestRun_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: the loop must fail before any chat call
	client := &scriptedClient{}
	bridge := &recordingBridge{}

	res, err := Run(ctx, client, bridge, Native, "s", "u", nil, Opts{Model: "m"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if res.Status == 0 {
		t.Error("expected nonzero Status")
	}
	if len(client.requests) != 0 {
		t.Errorf("chat calls = %d, want 0", len(client.requests))
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
