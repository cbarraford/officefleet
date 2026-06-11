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

type fakeAgentLister struct{ agents []*domain.Agent }

func (f *fakeAgentLister) List(_ context.Context) ([]*domain.Agent, error) { return f.agents, nil }

type fakeDutyLister struct{ duties []*domain.Duty }

func (f *fakeDutyLister) List(_ context.Context) ([]*domain.Duty, error) { return f.duties, nil }

func invokerFixture(t *testing.T) (*Invoker, *fakeRunRepo, uuid.UUID, *executor.FakeExecutor) {
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

	inv := &Invoker{
		cfg:      cfg,
		pipeline: pipeline,
		assignments: &fakeAssignmentGetter{byID: map[uuid.UUID]*domain.Assignment{
			assignmentID: {ID: assignmentID, AgentID: agentID, DutyID: dutyID, Enabled: true, Config: map[string]any{}},
		}},
		agents: &fakeAgentLister{agents: []*domain.Agent{{
			ID: agentID, Name: "inv-agent", Role: "t", SystemPrompt: "s",
			DefaultBackend: domain.BackendRef{Name: backendName}, Enabled: true,
		}}},
		duties: &fakeDutyLister{duties: []*domain.Duty{{
			ID: dutyID, Name: "inv-duty", Role: "t", Description: "d", Prompt: "p",
		}}},
		buildExecutor: func(_ *config.Config, _ *config.Backend) (executor.Executor, error) {
			return fakeExec, nil
		},
	}
	return inv, rr, assignmentID, fakeExec
}

func TestInvoker_Invoke(t *testing.T) {
	inv, rr, assignmentID, fakeExec := invokerFixture(t)
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
}

func TestInvoker_UnknownAssignment(t *testing.T) {
	inv, _, _, _ := invokerFixture(t)
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
