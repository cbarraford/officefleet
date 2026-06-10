package run

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/google/uuid"
)

// fakeSecretsProvider is an in-memory SecretsProvider for tests.
type fakeSecretsProvider struct {
	data map[string]string
}

func (f *fakeSecretsProvider) Load(_ context.Context) (map[string]string, error) {
	return f.data, nil
}

// mockPlugin is a test plugin that records Do calls.
type mockPlugin struct {
	name   string
	called bool
}

func (m *mockPlugin) Name() string                       { return m.name }
func (m *mockPlugin) EventSources() []plugin.EventSource { return nil }
func (m *mockPlugin) Actions() []plugin.Action           { return nil }
func (m *mockPlugin) ConfigSchema() plugin.Schema        { return nil }
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
	if run.Tokens != cannedResult.Tokens {
		t.Errorf("expected run.Tokens %d, got %d", cannedResult.Tokens, run.Tokens)
	}
	if run.Cost != cannedResult.Cost {
		t.Errorf("expected run.Cost %v, got %v", cannedResult.Cost, run.Cost)
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

func TestPipelineExecute_DutyBackendFallback(t *testing.T) {
	ctx := context.Background()

	cannedResult := domain.LLMResult{
		Status:  0,
		Summary: "duty-backend summary",
		Output:  map[string]any{},
		Tokens:  7,
		Cost:    0.0,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "duty-backend"
	backendRef := domain.BackendRef{Name: backendName}

	agentName := "duty-fallback-agent"
	dutyName := "duty-fallback-duty"

	// Config has the backend defined and the Duty references it; Assignment has no backend.
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
		Agents: []config.AgentConfig{
			{Name: agentName},
		},
		Duties: []config.DutyConfig{
			{Name: dutyName, Backend: &backendRef},
		},
	}

	rr := newFakeRunRepo()
	pipeline := &Pipeline{
		cfg:     cfg,
		runRepo: rr,
		store:   store,
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID:           agentID,
		Name:         agentName,
		Role:         "tester",
		SystemPrompt: "You are a duty-fallback test agent.",
		Enabled:      true,
	}
	duty := &domain.Duty{
		ID:          dutyID,
		Name:        dutyName,
		Role:        "testing",
		Description: "A duty for duty-backend fallback testing.",
		Prompt:      "Perform the duty-backend fallback test task.",
		Backend:     &backendRef,
	}
	assignment := &domain.Assignment{
		ID:      assignmentID,
		AgentID: agentID,
		DutyID:  dutyID,
		Enabled: true,
		Backend: nil, // deliberately nil: must fall through to Duty backend
		Config:  map[string]any{},
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
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("expected status %q, got %q", domain.RunStatusSucceeded, run.Status)
	}
	if run.LLMResult == nil {
		t.Fatal("expected LLMResult to be populated, got nil")
	}
	if run.LLMResult.Summary != cannedResult.Summary {
		t.Errorf("expected Summary %q, got %q", cannedResult.Summary, run.LLMResult.Summary)
	}
}

func TestPipelineExecute_AgentBackendFallback(t *testing.T) {
	ctx := context.Background()

	cannedResult := domain.LLMResult{
		Status:  0,
		Summary: "agent-backend summary",
		Output:  map[string]any{},
		Tokens:  3,
		Cost:    0.0,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "agent-backend"
	backendRef := domain.BackendRef{Name: backendName}

	agentName := "agent-fallback-agent"
	dutyName := "agent-fallback-duty"

	// Config has the backend defined and the Agent references it as default_backend;
	// neither Assignment nor Duty has a backend set.
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
		Agents: []config.AgentConfig{
			{Name: agentName, DefaultBackend: backendRef},
		},
		Duties: []config.DutyConfig{
			{Name: dutyName}, // no Backend
		},
	}

	rr := newFakeRunRepo()
	pipeline := &Pipeline{
		cfg:     cfg,
		runRepo: rr,
		store:   store,
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID:             agentID,
		Name:           agentName,
		Role:           "tester",
		SystemPrompt:   "You are an agent-fallback test agent.",
		DefaultBackend: backendRef,
		Enabled:        true,
	}
	duty := &domain.Duty{
		ID:          dutyID,
		Name:        dutyName,
		Role:        "testing",
		Description: "A duty for agent-backend fallback testing.",
		Prompt:      "Perform the agent-backend fallback test task.",
		Backend:     nil, // deliberately nil: must fall through to Agent default backend
	}
	assignment := &domain.Assignment{
		ID:      assignmentID,
		AgentID: agentID,
		DutyID:  dutyID,
		Enabled: true,
		Backend: nil, // deliberately nil
		Config:  map[string]any{},
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
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("expected status %q, got %q", domain.RunStatusSucceeded, run.Status)
	}
	if run.LLMResult == nil {
		t.Fatal("expected LLMResult to be populated, got nil")
	}
	if run.LLMResult.Summary != cannedResult.Summary {
		t.Errorf("expected Summary %q, got %q", cannedResult.Summary, run.LLMResult.Summary)
	}
}

func TestPipelineExecute_TaskPromptOverride(t *testing.T) {
	ctx := context.Background()

	cannedResult := domain.LLMResult{
		Status:  0,
		Summary: "override test summary",
		Output:  map[string]any{},
		Tokens:  7,
		Cost:    0.0,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "override-backend"
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
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID:           agentID,
		Name:         "override-agent",
		Role:         "tester",
		SystemPrompt: "You are an override test agent.",
		Enabled:      true,
	}
	dutyPromptText := "Original duty prompt text."
	duty := &domain.Duty{
		ID:          dutyID,
		Name:        "override-duty",
		Role:        "testing",
		Description: "A duty for override testing.",
		Prompt:      dutyPromptText,
	}
	overrideText := "This is the task prompt override text."
	assignment := &domain.Assignment{
		ID:                 assignmentID,
		AgentID:            agentID,
		DutyID:             dutyID,
		Enabled:            true,
		Backend:            backendRef,
		Config:             map[string]any{},
		TaskPromptOverride: &overrideText,
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
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("expected status %q, got %q", domain.RunStatusSucceeded, run.Status)
	}

	// AC#7: RenderedPrompt must contain the override text, not the duty original prompt.
	if run.RenderedPrompt == "" {
		t.Fatal("expected RenderedPrompt to be non-empty")
	}
	if !strings.Contains(run.RenderedPrompt, overrideText) {
		t.Errorf("expected RenderedPrompt to contain override text %q, got %q", overrideText, run.RenderedPrompt)
	}
	if strings.Contains(run.RenderedPrompt, dutyPromptText) {
		t.Errorf("expected RenderedPrompt NOT to contain duty prompt %q, but it did; got %q", dutyPromptText, run.RenderedPrompt)
	}
}

func TestPipelineExecute_ExtraInstructions(t *testing.T) {
	ctx := context.Background()

	cannedResult := domain.LLMResult{
		Status:  0,
		Summary: "extra instructions test summary",
		Output:  map[string]any{},
		Tokens:  7,
		Cost:    0.0,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "extra-backend"
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
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID:           agentID,
		Name:         "extra-agent",
		Role:         "tester",
		SystemPrompt: "You are an extra instructions test agent.",
		Enabled:      true,
	}
	duty := &domain.Duty{
		ID:          dutyID,
		Name:        "extra-duty",
		Role:        "testing",
		Description: "A duty for extra instructions testing.",
		Prompt:      "Perform the base task.",
	}
	extraText := "EXTRA_INSTRUCTIONS_SENTINEL"
	assignment := &domain.Assignment{
		ID:                assignmentID,
		AgentID:           agentID,
		DutyID:            dutyID,
		Enabled:           true,
		Backend:           backendRef,
		Config:            map[string]any{},
		ExtraInstructions: &extraText,
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

	// AC#7: RenderedPrompt must contain both the base duty prompt and the extra instructions.
	if !strings.Contains(run.RenderedPrompt, duty.Prompt) {
		t.Errorf("expected RenderedPrompt to contain base duty prompt %q, got %q", duty.Prompt, run.RenderedPrompt)
	}
	if !strings.Contains(run.RenderedPrompt, extraText) {
		t.Errorf("expected RenderedPrompt to contain extra instructions %q, got %q", extraText, run.RenderedPrompt)
	}
	// Extra instructions must appear after the base prompt.
	baseIdx := strings.Index(run.RenderedPrompt, duty.Prompt)
	extraIdx := strings.Index(run.RenderedPrompt, extraText)
	if extraIdx <= baseIdx {
		t.Errorf("expected extra instructions to appear after base duty prompt in RenderedPrompt")
	}
}

func TestPipelineExecute_StatePopulatedInPrompt(t *testing.T) {
	ctx := context.Background()

	cannedResult := domain.LLMResult{
		Status:  0,
		Summary: "state test summary",
		Output:  map[string]any{},
		Tokens:  7,
		Cost:    0.0,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "state-backend"
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
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID:           agentID,
		Name:         "state-agent",
		Role:         "tester",
		SystemPrompt: "You are a state test agent.",
		Enabled:      true,
	}

	// Duty prompt references a State key (raw string value).
	duty := &domain.Duty{
		ID:          dutyID,
		Name:        "state-duty",
		Role:        "testing",
		Description: "A duty for state testing.",
		Prompt:      "Last reviewed: {{.State.last_reviewed_sha}}",
	}

	assignment := &domain.Assignment{
		ID:      assignmentID,
		AgentID: agentID,
		DutyID:  dutyID,
		Enabled: true,
		Backend: backendRef,
		Config:  map[string]any{},
	}

	// Pre-populate state store with a raw-string value (the common SHA case).
	if err := store.Set(ctx, assignmentID.String(), "last_reviewed_sha", []byte("deadbeef")); err != nil {
		t.Fatalf("Set: %v", err)
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

	// The rendered prompt must contain the actual state value, not empty or <no value>.
	if run.RenderedPrompt != "Last reviewed: deadbeef" {
		t.Errorf("expected RenderedPrompt %q, got %q", "Last reviewed: deadbeef", run.RenderedPrompt)
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

	// Insert-then-skip design: the run record must exist in the repo even when
	// skipped, confirming the audit-trail intent documented in pipeline.go.
	stored, ok := rr.runs[run.ID]
	if !ok {
		t.Fatal("expected skipped run to be inserted into the repo, but it was not found")
	}
	if stored.Status != domain.RunStatusSkipped {
		t.Errorf("expected stored run status %q, got %q", domain.RunStatusSkipped, stored.Status)
	}
}

func TestPipelineExecute_SecretInTemplate(t *testing.T) {
	ctx := context.Background()

	cannedResult := domain.LLMResult{
		Status:  0,
		Summary: "secret test summary",
		Output:  map[string]any{},
		Tokens:  5,
		Cost:    0.0,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "secret-backend"
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
		secrets: &fakeSecretsProvider{data: map[string]string{"api_token": "tok-abc123"}},
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID:           agentID,
		Name:         "secret-agent",
		Role:         "tester",
		SystemPrompt: "You are a secret test agent.",
		Enabled:      true,
	}
	duty := &domain.Duty{
		ID:          dutyID,
		Name:        "secret-duty",
		Role:        "testing",
		Description: "A duty for secret testing.",
		Prompt:      `Use token: {{secret "api_token"}}`,
	}
	assignment := &domain.Assignment{
		ID:      assignmentID,
		AgentID: agentID,
		DutyID:  dutyID,
		Enabled: true,
		Backend: backendRef,
		Config:  map[string]any{},
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
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("expected status %q, got %q", domain.RunStatusSucceeded, run.Status)
	}
	// The rendered prompt must contain the resolved secret value, proving the gap is closed.
	if run.RenderedPrompt != "Use token: tok-abc123" {
		t.Errorf("expected RenderedPrompt %q, got %q", "Use token: tok-abc123", run.RenderedPrompt)
	}
}

// pausedTestFixture builds the common pipeline/request setup for pause tests.
// agentEnabled / assignmentEnabled control the respective Enabled flags.
func pausedTestFixture(agentEnabled, assignmentEnabled bool) (*Pipeline, ExecuteRequest, *fakeRunRepo, *executor.FakeExecutor) {
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "should not run"})
	store := state.NewMemStore()

	backendName := "pause-backend"
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
	}

	agentID := uuid.New()
	dutyID := uuid.New()

	agent := &domain.Agent{
		ID:           agentID,
		Name:         "pause-agent",
		Role:         "tester",
		SystemPrompt: "You are a pause test agent.",
		Enabled:      agentEnabled,
	}
	duty := &domain.Duty{
		ID:          dutyID,
		Name:        "pause-duty",
		Role:        "testing",
		Description: "A duty for pause testing.",
		Prompt:      "Perform the pause test task.",
	}
	assignment := &domain.Assignment{
		ID:      uuid.New(),
		AgentID: agentID,
		DutyID:  dutyID,
		Enabled: assignmentEnabled,
		Backend: &domain.BackendRef{Name: backendName},
		Config:  map[string]any{},
	}

	req := ExecuteRequest{
		Assignment:  assignment,
		Agent:       agent,
		Duty:        duty,
		TriggerKind: "manual",
		EventParams: map[string]any{"mr_iid": "7"},
		Executor:    fakeExec,
	}
	return pipeline, req, rr, fakeExec
}

// assertPausedSkip verifies a paused run was skipped, recorded for audit with
// the expected reason, and never reached the executor.
func assertPausedSkip(t *testing.T, run *domain.Run, rr *fakeRunRepo, fakeExec *executor.FakeExecutor, wantReason string) {
	t.Helper()
	if run == nil {
		t.Fatal("Execute returned nil run")
	}
	if run.Status != domain.RunStatusSkipped {
		t.Errorf("expected status %q, got %q", domain.RunStatusSkipped, run.Status)
	}
	if run.Error == nil || *run.Error != wantReason {
		got := "<nil>"
		if run.Error != nil {
			got = *run.Error
		}
		t.Errorf("expected skip reason %q, got %q", wantReason, got)
	}
	if fakeExec.LastReq.Prompt != "" {
		t.Error("expected executor not to be called (LastReq.Prompt should be empty)")
	}
	stored, ok := rr.runs[run.ID]
	if !ok {
		t.Fatal("expected skipped run to be inserted into the repo for audit, but it was not found")
	}
	if stored.Status != domain.RunStatusSkipped {
		t.Errorf("expected stored run status %q, got %q", domain.RunStatusSkipped, stored.Status)
	}
}

func TestPipelineExecute_AgentPausedSkip(t *testing.T) {
	pipeline, req, rr, fakeExec := pausedTestFixture(false, true)

	run, err := pipeline.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	assertPausedSkip(t, run, rr, fakeExec, "agent_paused")
}

func TestPipelineExecute_AssignmentPausedSkip(t *testing.T) {
	pipeline, req, rr, fakeExec := pausedTestFixture(true, false)

	run, err := pipeline.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	assertPausedSkip(t, run, rr, fakeExec, "assignment_paused")
}

func TestPipelineExecute_ModelReportedFailure(t *testing.T) {
	ctx := context.Background()

	// Register a plugin that must NOT be called.
	mock := &mockPlugin{name: "must-not-deliver-plugin"}
	plugin.Register(mock)

	cannedResult := domain.LLMResult{
		Status:     2,
		Summary:    "could not complete the review",
		Output:     map[string]any{},
		Transcript: "TRANSCRIPT_SENTINEL",
		Tokens:     11,
		Cost:       0.002,
	}
	fakeExec := executor.NewFakeExecutor(cannedResult)
	store := state.NewMemStore()

	backendName := "modelfail-backend"
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
	}

	agentID := uuid.New()
	dutyID := uuid.New()
	assignmentID := uuid.New()

	agent := &domain.Agent{
		ID: agentID, Name: "modelfail-agent", Role: "tester",
		SystemPrompt: "You are a test agent.", Enabled: true,
	}
	duty := &domain.Duty{
		ID: dutyID, Name: "modelfail-duty", Role: "testing",
		Description: "A duty for model-failure testing.", Prompt: "Do the task.",
	}
	assignment := &domain.Assignment{
		ID: assignmentID, AgentID: agentID, DutyID: dutyID,
		Enabled: true, Backend: backendRef, Config: map[string]any{},
		Outputs: []domain.OutputBinding{
			{Plugin: "must-not-deliver-plugin", Action: "notify", Params: map[string]any{"m": "x"}},
		},
	}

	req := ExecuteRequest{
		Assignment: assignment, Agent: agent, Duty: duty,
		TriggerKind: "event", EventParams: map[string]any{"mr_iid": "99"},
		Executor: fakeExec,
	}

	run, err := pipeline.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute returned error: %v (model-reported failure must not be a pipeline error)", err)
	}
	if run.Status != domain.RunStatusFailed {
		t.Errorf("status = %q, want failed", run.Status)
	}
	if run.Error == nil || !strings.Contains(*run.Error, "status 2") {
		t.Errorf("run.Error = %v, want llm failure message", run.Error)
	}
	// Full result recorded (transcript retained).
	if run.LLMResult == nil || run.LLMResult.Transcript != "TRANSCRIPT_SENTINEL" {
		t.Errorf("LLMResult = %+v, want transcript retained", run.LLMResult)
	}
	if run.Tokens != 11 {
		t.Errorf("Tokens = %d, want 11", run.Tokens)
	}
	// Outputs skipped.
	if mock.called {
		t.Error("output plugin was called for a failed-status result")
	}
	if len(run.OutputsDelivered) != 0 {
		t.Errorf("OutputsDelivered = %v, want none", run.OutputsDelivered)
	}
	// Dedup NOT marked: a retry must be possible.
	already, err := store.HasProcessed(ctx, assignmentID.String(), "mr_iid:99")
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Error("dedup key was marked processed for a failed run; retries are now impossible")
	}
	// Stored run reflects failure.
	stored := rr.runs[run.ID]
	if stored == nil || stored.Status != domain.RunStatusFailed {
		t.Errorf("stored run = %+v", stored)
	}
}

func TestPipelineExecute_ZeroStatusStillSucceeds(t *testing.T) {
	// Guard: the new Status check must not affect the success path.
	ctx := context.Background()
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "fine"})
	store := state.NewMemStore()
	backendName := "zerostatus-backend"
	cfg := &config.Config{Backends: []config.Backend{{
		Name: backendName, Kind: "claude", Model: "claude-3-5-sonnet",
		DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
	}}}
	rr := newFakeRunRepo()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: store}

	agentID, dutyID := uuid.New(), uuid.New()
	run, err := pipeline.Execute(ctx, ExecuteRequest{
		Assignment: &domain.Assignment{
			ID: uuid.New(), AgentID: agentID, DutyID: dutyID, Enabled: true,
			Backend: &domain.BackendRef{Name: backendName}, Config: map[string]any{},
		},
		Agent:       &domain.Agent{ID: agentID, Name: "z-agent", Role: "t", SystemPrompt: "s", Enabled: true},
		Duty:        &domain.Duty{ID: dutyID, Name: "z-duty", Role: "t", Description: "d", Prompt: "p"},
		TriggerKind: "manual", EventParams: map[string]any{}, Executor: fakeExec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("status = %q, want succeeded", run.Status)
	}
}

func TestPipelineExecute_ExecutorErrorPreservesPartialResult(t *testing.T) {
	ctx := context.Background()
	fakeExec := &executor.FakeExecutor{
		Result: domain.LLMResult{Status: 1, Summary: "transport died", Transcript: "PARTIAL_TRANSCRIPT", Tokens: 7},
		Err:    fmt.Errorf("chat: connection refused"),
	}
	store := state.NewMemStore()
	backendName := "partial-backend"
	cfg := &config.Config{Backends: []config.Backend{{
		Name: backendName, Kind: "claude", Model: "claude-3-5-sonnet",
		DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
	}}}
	rr := newFakeRunRepo()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: store}

	agentID, dutyID := uuid.New(), uuid.New()
	run, err := pipeline.Execute(ctx, ExecuteRequest{
		Assignment: &domain.Assignment{
			ID: uuid.New(), AgentID: agentID, DutyID: dutyID, Enabled: true,
			Backend: &domain.BackendRef{Name: backendName}, Config: map[string]any{},
		},
		Agent:       &domain.Agent{ID: agentID, Name: "p-agent", Role: "t", SystemPrompt: "s", Enabled: true},
		Duty:        &domain.Duty{ID: dutyID, Name: "p-duty", Role: "t", Description: "d", Prompt: "p"},
		TriggerKind: "manual", EventParams: map[string]any{}, Executor: fakeExec,
	})
	if err == nil {
		t.Fatal("expected executor error to propagate")
	}
	if run == nil {
		t.Fatal("expected run to be returned alongside the error")
	}
	if run.Status != domain.RunStatusFailed {
		t.Errorf("status = %q", run.Status)
	}
	stored := rr.runs[run.ID]
	if stored == nil || stored.LLMResult == nil || stored.LLMResult.Transcript != "PARTIAL_TRANSCRIPT" {
		t.Errorf("stored run = %+v, want partial transcript preserved", stored)
	}
	if stored.Error == nil || !strings.Contains(*stored.Error, "connection refused") {
		t.Errorf("stored error = %v", stored.Error)
	}
}
