package executor_test

import (
	"context"
	"flag"
	"fmt"
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
)

var liveFlag = flag.Bool("live", false, "run live tests against real claude CLI")

func TestFakeExecutor_RecordsRequest(t *testing.T) {
	fake := executor.NewFakeExecutor(domain.LLMResult{
		Status: 0, Summary: "LGTM", Output: map[string]any{"approved": true},
		Tokens: 100, Cost: 0.001,
	})
	req := executor.LLMRequest{
		SystemPrompt: "You are a developer.",
		Prompt:       "Review MR #42.",
		Workspace:    "/tmp",
		Tools:        []string{},
		Model:        "claude-opus-4-5",
		Effort:       "high",
	}
	result, err := fake.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "LGTM" {
		t.Fatalf("unexpected summary: %q", result.Summary)
	}
	if fake.LastReq.Prompt != req.Prompt {
		t.Fatalf("expected LastReq.Prompt %q, got %q", req.Prompt, fake.LastReq.Prompt)
	}
	if fake.LastReq.SystemPrompt != req.SystemPrompt {
		t.Fatalf("expected LastReq.SystemPrompt %q, got %q", req.SystemPrompt, fake.LastReq.SystemPrompt)
	}
	if fake.LastReq.Workspace != req.Workspace {
		t.Fatalf("expected LastReq.Workspace %q, got %q", req.Workspace, fake.LastReq.Workspace)
	}
	if len(fake.LastReq.Tools) != len(req.Tools) {
		t.Fatalf("expected LastReq.Tools length %d, got %d", len(req.Tools), len(fake.LastReq.Tools))
	}
	for i, tool := range req.Tools {
		if fake.LastReq.Tools[i] != tool {
			t.Fatalf("expected LastReq.Tools[%d] %q, got %q", i, tool, fake.LastReq.Tools[i])
		}
	}
	if fake.LastReq.Model != req.Model {
		t.Fatalf("expected LastReq.Model %q, got %q", req.Model, fake.LastReq.Model)
	}
	if fake.LastReq.Effort != req.Effort {
		t.Fatalf("expected LastReq.Effort %q, got %q", req.Effort, fake.LastReq.Effort)
	}
}

func TestFakeExecutor_ErrorPropagation(t *testing.T) {
	fake := &executor.FakeExecutor{Err: fmt.Errorf("LLM unavailable")}
	_, err := fake.Run(context.Background(), executor.LLMRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestClaudeExecutor_LiveSmoke(t *testing.T) {
	if !*liveFlag {
		t.Skip("skipping live test; pass -live to enable")
	}
	ex := executor.NewClaudeExecutor("")
	req := executor.LLMRequest{
		Prompt: "Reply with only the word: OK",
	}
	result, err := ex.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("live claude run failed: %v", err)
	}
	if result.Status != 0 {
		t.Errorf("unexpected non-zero status: %d", result.Status)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary from live run")
	}
}
