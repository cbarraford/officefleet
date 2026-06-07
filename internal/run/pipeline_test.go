package run

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/state"
)

// fakeRunRepo is an in-memory implementation of runRepo for tests.
type fakeRunRepo struct {
	runs map[uuid.UUID]*domain.Run
}

func newFakeRunRepo() *fakeRunRepo {
	return &fakeRunRepo{runs: map[uuid.UUID]*domain.Run{}}
}

func (f *fakeRunRepo) Insert(_ context.Context, run *domain.Run) error {
	f.runs[run.ID] = run
	return nil
}

func (f *fakeRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.RunStatus, errMsg *string) error {
	if r, ok := f.runs[id]; ok {
		r.Status = status
		r.Error = errMsg
	}
	return nil
}

func (f *fakeRunRepo) UpdateResult(_ context.Context, id uuid.UUID, result *domain.LLMResult, outputs []domain.OutputDelivery, status domain.RunStatus) error {
	if r, ok := f.runs[id]; ok {
		r.LLMResult = result
		r.OutputsDelivered = outputs
		r.Status = status
		finished := time.Now()
		r.FinishedAt = &finished
	}
	return nil
}

func TestPipelineExecute(t *testing.T) {
	ctx := context.Background()

	// Canned LLM result.
	cannedResult := domain.LLMResult{
		Status:  0,
		Summary: "test summary",
		Output:  map[string]any{"key": "value"},
		Tokens:  42,
		Cost:    0.001,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)

	// In-memory state store.
	store := state.NewMemStore()

	// Config with a named backend so ResolveBackend succeeds.
	backendName := "test-backend"
	backendRef := &domain.BackendRef{Name: backendName}
	cfg := &config.Config{
		Backends: []config.Backend{
			{
				Name:          backendName,
				Kind:          "claude",
				Model:         "claude-3-5-sonnet",
				DefaultEffort: "normal",
				Auth:          config.BackendAuth{Mode: "subscription"},
			},
		},
	}

	// In-memory run repo.
	rr := newFakeRunRepo()

	// Build pipeline directly (same-package test allows access to unexported fields).
	pipeline := &Pipeline{
		cfg:     cfg,
		runRepo: rr,
		store:   store,
		plugins: map[string]plugin.Plugin{},
	}

	// Domain objects.
	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID:           agentID,
		Name:         "test-agent",
		Role:         "tester",
		SystemPrompt: "You are a test agent.",
		Enabled:      true,
	}

	duty := &domain.Duty{
		ID:          dutyID,
		Name:        "test-duty",
		Role:        "testing",
		Description: "A duty for testing.",
		Prompt:      "Perform the test task.",
	}

	assignment := &domain.Assignment{
		ID:      assignmentID,
		AgentID: agentID,
		DutyID:  dutyID,
		Enabled: true,
		Backend: backendRef,
		Config:  map[string]any{},
		// Outputs is deliberately empty to avoid plugin-not-registered failures.
	}

	req := ExecuteRequest{
		Assignment:  assignment,
		Agent:       agent,
		Duty:        duty,
		TriggerKind: "manual",
		EventParams: map[string]any{"reason": "test"},
		Executor:    fakeExec,
	}

	run, err := pipeline.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if run == nil {
		t.Fatal("Execute returned nil run")
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("expected status %q, got %q", domain.RunStatusSucceeded, run.Status)
	}
	if run.LLMResult == nil {
		t.Fatal("expected LLMResult to be populated, got nil")
	}
	if run.LLMResult.Summary != cannedResult.Summary {
		t.Errorf("expected Summary %q, got %q", cannedResult.Summary, run.LLMResult.Summary)
	}
	if run.LLMResult.Tokens != cannedResult.Tokens {
		t.Errorf("expected Tokens %d, got %d", cannedResult.Tokens, run.LLMResult.Tokens)
	}
	if run.AssignmentID != assignmentID {
		t.Errorf("expected AssignmentID %v, got %v", assignmentID, run.AssignmentID)
	}
	if run.AgentID != agentID {
		t.Errorf("expected AgentID %v, got %v", agentID, run.AgentID)
	}
	if run.DutyID != dutyID {
		t.Errorf("expected DutyID %v, got %v", dutyID, run.DutyID)
	}
	if run.FinishedAt == nil {
		t.Error("expected FinishedAt to be set")
	}
}
