package run

import (
	"context"
	"fmt"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/google/uuid"
)

type fakeAssignmentGetter struct {
	byID map[uuid.UUID]*domain.Assignment
}

func (f *fakeAssignmentGetter) GetByID(_ context.Context, id uuid.UUID) (*domain.Assignment, error) {
	a, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("assignment %s not found", id)
	}
	return a, nil
}

type fakeAgentGetter struct {
	byID      map[uuid.UUID]*domain.Agent
	listCalls int
}

func (f *fakeAgentGetter) GetByID(_ context.Context, id uuid.UUID) (*domain.Agent, error) {
	a, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("agent %s not found", id)
	}
	return a, nil
}

func (f *fakeAgentGetter) List(_ context.Context) ([]*domain.Agent, error) {
	f.listCalls++
	return nil, fmt.Errorf("List must not be used by Invoker")
}

type fakeDutyGetter struct {
	byID      map[uuid.UUID]*domain.Duty
	listCalls int
}

func (f *fakeDutyGetter) GetByID(_ context.Context, id uuid.UUID) (*domain.Duty, error) {
	d, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("duty %s not found", id)
	}
	return d, nil
}

func (f *fakeDutyGetter) List(_ context.Context) ([]*domain.Duty, error) {
	f.listCalls++
	return nil, fmt.Errorf("List must not be used by Invoker")
}

func invokerFixture(t *testing.T) (*Invoker, *fakeRunRepo, uuid.UUID, *executor.FakeExecutor, *fakeAgentGetter, *fakeDutyGetter) {
	t.Helper()
	backendName := "inv-backend"
	agentID, dutyID, assignmentID := uuid.New(), uuid.New(), uuid.New()
	cfg := &config.Config{
		Backends: []config.Backend{{
			Name: backendName, Kind: "claude", Model: "claude-3-5-sonnet",
			DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
		}},
		Agents:      []config.AgentConfig{{Name: "inv-agent", DefaultBackend: domain.BackendRef{Name: backendName}}},
		Duties:      []config.DutyConfig{{Name: "inv-duty"}},
		Assignments: []config.AssignmentConfig{{Agent: "inv-agent", Duty: "inv-duty"}},
	}
	rr := newFakeRunRepo()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: state.NewMemStore()}
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "invoked"})
	agents := &fakeAgentGetter{byID: map[uuid.UUID]*domain.Agent{
		agentID: {
			ID: agentID, Name: "inv-agent", Role: "t", SystemPrompt: "s",
			DefaultBackend: domain.BackendRef{Name: backendName}, Enabled: true,
		},
	}}
	duties := &fakeDutyGetter{byID: map[uuid.UUID]*domain.Duty{
		dutyID: {
			ID: dutyID, Name: "inv-duty", Role: "t", Description: "d", Prompt: "p",
		},
	}}

	inv := NewInvokerWithExecutorBuilder(cfg, pipeline,
		&fakeAssignmentGetter{byID: map[uuid.UUID]*domain.Assignment{
			assignmentID: {ID: assignmentID, AgentID: agentID, DutyID: dutyID, Enabled: true, Config: map[string]any{}},
		}},
		agents, duties,
		func(_ *config.Config, _ *config.Backend) (executor.Executor, error) {
			return fakeExec, nil
		})
	return inv, rr, assignmentID, fakeExec, agents, duties
}

func TestInvoker_Invoke(t *testing.T) {
	inv, rr, assignmentID, fakeExec, agents, duties := invokerFixture(t)
	eventID := "deadbeef-0000-0000-0000-000000000000"
	run, err := inv.Invoke(context.Background(), assignmentID, "event-subscription",
		&eventID, map[string]any{"mr_iid": "9"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("status = %q", run.Status)
	}
	if run.TriggerKind != "event-subscription" {
		t.Errorf("trigger kind = %q", run.TriggerKind)
	}
	if run.EventID == nil || *run.EventID != eventID {
		t.Errorf("EventID = %v", run.EventID)
	}
	if fakeExec.LastReq.Prompt == "" {
		t.Error("executor was not called")
	}
	if len(rr.runs) != 1 {
		t.Errorf("recorded runs = %d", len(rr.runs))
	}
	if agents.listCalls != 0 || duties.listCalls != 0 {
		t.Fatalf("Invoker used List; agent calls=%d duty calls=%d", agents.listCalls, duties.listCalls)
	}
}

func TestInvoker_UnknownAssignment(t *testing.T) {
	inv, _, _, _, _, _ := invokerFixture(t)
	_, err := inv.Invoke(context.Background(), uuid.New(), "cron", nil, map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown assignment")
	}
}

func TestInvoker_DefaultBuildExecutor(t *testing.T) {
	// nil backend -> claude default; defined backend -> factory dispatch.
	ex, err := defaultBuildExecutor(&config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ex.(*executor.ClaudeExecutor); !ok {
		t.Errorf("nil backend built %T, want *executor.ClaudeExecutor", ex)
	}
	cfg := &config.Config{}
	ex, err = defaultBuildExecutor(cfg, &config.Backend{
		Name: "e", Kind: "openai-compatible", BaseURI: "http://x/v1", Model: "m",
		Auth: config.BackendAuth{Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ex.(*executor.EndpointExecutor); !ok {
		t.Errorf("endpoint backend built %T", ex)
	}
}
