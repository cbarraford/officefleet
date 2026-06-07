package executor_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
)

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
		t.Fatalf("request not recorded")
	}
}

func TestFakeExecutor_ErrorPropagation(t *testing.T) {
	fake := &executor.FakeExecutor{Err: fmt.Errorf("LLM unavailable")}
	_, err := fake.Run(context.Background(), executor.LLMRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}
