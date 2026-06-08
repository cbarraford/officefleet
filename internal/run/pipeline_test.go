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

// mockPlugin is a test plugin that records Do calls.
type mockPlugin struct {
	name   string
	called bool
}

func (m *mockPlugin) Name() string                  { return m.name }
func (m *mockPlugin) EventSources() []plugin.EventSource { return nil }
func (m *mockPlugin) Actions() []plugin.Action      { return nil }
func (m *mockPlugin) ConfigSchema() plugin.Schema    { return nil }
func (m *mockPlugin) Init(_ context.Context, _ map[string]any, _ plugin.SecretLookup) error {
	return nil
}
func (m *mockPlugin) Do(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
	m.called = true
	return map[string]any{}, nil
}

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

	// M5: rendered prompts must be populated and IDs non-zero.
	if run.RenderedSystemPrompt == "" {
		t.Error("expected RenderedSystemPrompt to be non-empty")
	}
	if run.RenderedPrompt == "" {
		t.Error("expected RenderedPrompt to be non-empty")
	}
	if run.AgentID == (uuid.UUID{}) {
		t.Error("expected AgentID to be non-zero UUID")
	}
	if run.DutyID == (uuid.UUID{}) {
		t.Error("expected DutyID to be non-zero UUID")
	}
}

func TestPipelineExecute_WithOutputDelivery(t *testing.T) {
	ctx := context.Background()

	// Register the mock plugin in the global registry.
	mock := &mockPlugin{name: "test-delivery-plugin"}
	plugin.Register(mock)

	cannedResult := domain.LLMResult{
		Status:  0,
		Summary: "delivery test summary",
		Output:  map[string]any{},
		Tokens:  10,
		Cost:    0.0,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "delivery-backend"
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

	rr := newFakeRunRepo()
	pipeline := &Pipeline{
		cfg:     cfg,
		runRepo: rr,
		store:   store,
		plugins: map[string]plugin.Plugin{},
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID:           agentID,
		Name:         "delivery-agent",
		Role:         "tester",
		SystemPrompt: "You are a delivery test agent.",
		Enabled:      true,
	}
	duty := &domain.Duty{
		ID:          dutyID,
		Name:        "delivery-duty",
		Role:        "testing",
		Description: "A duty for delivery testing.",
		Prompt:      "Perform the delivery test task.",
	}
	assignment := &domain.Assignment{
		ID:      assignmentID,
		AgentID: agentID,
		DutyID:  dutyID,
		Enabled: true,
		Backend: backendRef,
		Config:  map[string]any{},
		Outputs: []domain.OutputBinding{
			{
				Plugin: "test-delivery-plugin",
				Action: "notify",
				Params: map[string]any{"message": "done"},
			},
		},
	}

	req := ExecuteRequest{
		Assignment:  assignment,
		Agent:       agent,
		Duty:        duty,
		TriggerKind: "manual",
		EventParams: map[string]any{},
		Executor:    fakeExec,
	}

	run, err := pipeline.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if run == nil {
		t.Fatal("Execute returned nil run")
	}

	// M6: outputs delivered.
	if len(run.OutputsDelivered) != 1 {
		t.Fatalf("expected 1 output delivery, got %d", len(run.OutputsDelivered))
	}
	if run.OutputsDelivered[0].Status != "delivered" {
		t.Errorf("expected delivery status %q, got %q", "delivered", run.OutputsDelivered[0].Status)
	}
	if !mock.called {
		t.Error("expected mock plugin Do to be called")
	}
}

func TestPipelineExecute_DedupSkip(t *testing.T) {
	ctx := context.Background()

	cannedResult := domain.LLMResult{
		Status:  0,
		Summary: "dedup test summary",
		Output:  map[string]any{},
		Tokens:  5,
		Cost:    0.0,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "dedup-backend"
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

	rr := newFakeRunRepo()
	pipeline := &Pipeline{
		cfg:     cfg,
		runRepo: rr,
		store:   store,
		plugins: map[string]plugin.Plugin{},
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID:           agentID,
		Name:         "dedup-agent",
		Role:         "tester",
		SystemPrompt: "You are a dedup test agent.",
		Enabled:      true,
	}
	duty := &domain.Duty{
		ID:          dutyID,
		Name:        "dedup-duty",
		Role:        "testing",
		Description: "A duty for dedup testing.",
		Prompt:      "Perform the dedup test task.",
	}
	assignment := &domain.Assignment{
		ID:      assignmentID,
		AgentID: agentID,
		DutyID:  dutyID,
		Enabled: true,
		Backend: backendRef,
		Config:  map[string]any{},
	}

	// Pre-mark the dedup key as processed so the pipeline skips.
	if err := store.MarkProcessed(ctx, assignmentID.String(), "mr_iid:42"); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}

	req := ExecuteRequest{
		Assignment:  assignment,
		Agent:       agent,
		Duty:        duty,
		TriggerKind: "event",
		EventParams: map[string]any{"mr_iid": "42"},
		Executor:    fakeExec,
	}

	run, err := pipeline.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if run == nil {
		t.Fatal("Execute returned nil run")
	}

	// C5/C6: run must be skipped and executor must not have been called.
	if run.Status != domain.RunStatusSkipped {
		t.Errorf("expected status %q, got %q", domain.RunStatusSkipped, run.Status)
	}
	if fakeExec.LastReq.Prompt != "" {
		t.Error("expected executor not to be called (LastReq.Prompt should be empty)")
	}
}
