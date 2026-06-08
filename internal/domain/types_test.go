package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cbarraford/office-fleet/internal/domain"
)

func TestBackendRef_JSONRoundtrip(t *testing.T) {
	orig := domain.BackendRef{
		Name:   "anthropic",
		Model:  "claude-3-5-sonnet",
		Effort: "high",
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got domain.BackendRef
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Name != orig.Name {
		t.Errorf("Name: got %q, want %q", got.Name, orig.Name)
	}
	if got.Model != orig.Model {
		t.Errorf("Model: got %q, want %q", got.Model, orig.Model)
	}
	if got.Effort != orig.Effort {
		t.Errorf("Effort: got %q, want %q", got.Effort, orig.Effort)
	}
}

func TestLLMResult_JSONRoundtrip(t *testing.T) {
	orig := domain.LLMResult{
		Summary: "task completed successfully",
		Tokens:  1234,
		Cost:    0.0056,
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got domain.LLMResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Summary != orig.Summary {
		t.Errorf("Summary: got %q, want %q", got.Summary, orig.Summary)
	}
	if got.Tokens != orig.Tokens {
		t.Errorf("Tokens: got %d, want %d", got.Tokens, orig.Tokens)
	}
	if got.Cost != orig.Cost {
		t.Errorf("Cost: got %v, want %v", got.Cost, orig.Cost)
	}
}

func TestRunStatus_Constants(t *testing.T) {
	statuses := []domain.RunStatus{
		domain.RunStatusQueued,
		domain.RunStatusRunning,
		domain.RunStatusSucceeded,
		domain.RunStatusFailed,
		domain.RunStatusSkipped,
	}

	// All 5 constants must be non-empty.
	for _, s := range statuses {
		if string(s) == "" {
			t.Errorf("RunStatus constant is empty string")
		}
	}

	// All 5 constants must be distinct.
	seen := make(map[domain.RunStatus]bool)
	for _, s := range statuses {
		if seen[s] {
			t.Errorf("duplicate RunStatus value: %q", s)
		}
		seen[s] = true
	}
}

func TestTriggerConfig_JSONRoundtrip(t *testing.T) {
	orig := domain.TriggerConfig{
		Kind:     "cron",
		Schedule: "0 9 * * 1-5",
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got domain.TriggerConfig
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Kind != orig.Kind {
		t.Errorf("Kind: got %q, want %q", got.Kind, orig.Kind)
	}
	if got.Schedule != orig.Schedule {
		t.Errorf("Schedule: got %q, want %q", got.Schedule, orig.Schedule)
	}
}

func TestAgent_NonZeroID(t *testing.T) {
	now := time.Now()
	agent := domain.Agent{
		ID:        uuid.New(),
		Name:      "test-agent",
		CreatedAt: now,
	}

	if agent.ID == (uuid.UUID{}) {
		t.Error("Agent.ID must not be zero UUID")
	}
	if agent.CreatedAt.IsZero() {
		t.Error("Agent.CreatedAt must not be zero time")
	}
}
