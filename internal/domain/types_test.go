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
		Status:     2,
		Summary:    "task completed successfully",
		Output:     map[string]any{"key": "value", "count": float64(3)},
		Transcript: "user: hello\nassistant: hi",
		Tokens:     1234,
		Cost:       0.0056,
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got domain.LLMResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Status != orig.Status {
		t.Errorf("Status: got %d, want %d", got.Status, orig.Status)
	}
	if got.Summary != orig.Summary {
		t.Errorf("Summary: got %q, want %q", got.Summary, orig.Summary)
	}
	if len(got.Output) != len(orig.Output) {
		t.Errorf("Output length: got %d, want %d", len(got.Output), len(orig.Output))
	} else {
		for k, wantV := range orig.Output {
			gotV, ok := got.Output[k]
			if !ok {
				t.Errorf("Output missing key %q", k)
			} else if gotV != wantV {
				t.Errorf("Output[%q]: got %v, want %v", k, gotV, wantV)
			}
		}
	}
	if got.Transcript != orig.Transcript {
		t.Errorf("Transcript: got %q, want %q", got.Transcript, orig.Transcript)
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

func TestOutputActionType_JSONRoundtrip(t *testing.T) {
	orig := domain.OutputActionType{
		Plugin: "slack",
		Action: "send_message",
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got domain.OutputActionType
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Plugin != orig.Plugin {
		t.Errorf("Plugin: got %q, want %q", got.Plugin, orig.Plugin)
	}
	if got.Action != orig.Action {
		t.Errorf("Action: got %q, want %q", got.Action, orig.Action)
	}
}

func TestOutputBinding_JSONRoundtrip(t *testing.T) {
	orig := domain.OutputBinding{
		Plugin: "email",
		Action: "send",
		Params: map[string]any{"to": "user@example.com", "subject": "report"},
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got domain.OutputBinding
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Plugin != orig.Plugin {
		t.Errorf("Plugin: got %q, want %q", got.Plugin, orig.Plugin)
	}
	if got.Action != orig.Action {
		t.Errorf("Action: got %q, want %q", got.Action, orig.Action)
	}
	if got.Params["to"] != orig.Params["to"] {
		t.Errorf("Params[to]: got %v, want %v", got.Params["to"], orig.Params["to"])
	}
}

func TestOutputDelivery_JSONRoundtrip(t *testing.T) {
	orig := domain.OutputDelivery{
		Plugin: "slack",
		Action: "post",
		Params: map[string]any{"channel": "#general"},
		Status: "delivered",
		Error:  "",
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got domain.OutputDelivery
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Plugin != orig.Plugin {
		t.Errorf("Plugin: got %q, want %q", got.Plugin, orig.Plugin)
	}
	if got.Action != orig.Action {
		t.Errorf("Action: got %q, want %q", got.Action, orig.Action)
	}
	if got.Status != orig.Status {
		t.Errorf("Status: got %q, want %q", got.Status, orig.Status)
	}
	if got.Error != orig.Error {
		t.Errorf("Error: got %q, want %q", got.Error, orig.Error)
	}
}

func TestOutputDelivery_ErrorField_JSONRoundtrip(t *testing.T) {
	orig := domain.OutputDelivery{
		Plugin: "webhook",
		Action: "call",
		Status: "failed",
		Error:  "connection refused",
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got domain.OutputDelivery
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Error != orig.Error {
		t.Errorf("Error: got %q, want %q", got.Error, orig.Error)
	}
}

func TestDuty_StructConstruction(t *testing.T) {
	backend := &domain.BackendRef{Name: "openai", Model: "gpt-4o"}
	duty := domain.Duty{
		ID:            uuid.New(),
		Name:          "daily-report",
		TriggerKinds:  []string{"cron", "webhook"},
		RequiredTools: []string{"search", "summarize"},
		Backend:       backend,
	}

	if len(duty.TriggerKinds) != 2 {
		t.Errorf("TriggerKinds length: got %d, want 2", len(duty.TriggerKinds))
	}
	if duty.TriggerKinds[0] != "cron" {
		t.Errorf("TriggerKinds[0]: got %q, want %q", duty.TriggerKinds[0], "cron")
	}
	if len(duty.RequiredTools) != 2 {
		t.Errorf("RequiredTools length: got %d, want 2", len(duty.RequiredTools))
	}
	if duty.Backend == nil {
		t.Fatal("Backend must not be nil")
	}
	if duty.Backend.Name != "openai" {
		t.Errorf("Backend.Name: got %q, want %q", duty.Backend.Name, "openai")
	}
}

func TestAssignment_StructConstruction(t *testing.T) {
	trigger := domain.TriggerConfig{
		Kind:     "cron",
		Schedule: "0 8 * * 1-5",
	}
	outputs := []domain.OutputBinding{
		{Plugin: "slack", Action: "post", Params: map[string]any{"channel": "#ops"}},
	}
	cfg := map[string]any{"threshold": 10, "region": "us-east-1"}

	assignment := domain.Assignment{
		ID:      uuid.New(),
		Name:    "weekday-review",
		AgentID: uuid.New(),
		DutyID:  uuid.New(),
		Enabled: true,
		Trigger: trigger,
		Outputs: outputs,
		Config:  cfg,
	}

	if assignment.Trigger.Kind != "cron" {
		t.Errorf("Trigger.Kind: got %q, want %q", assignment.Trigger.Kind, "cron")
	}
	if assignment.Name != "weekday-review" {
		t.Errorf("Name: got %q, want %q", assignment.Name, "weekday-review")
	}
	if assignment.Trigger.Schedule != "0 8 * * 1-5" {
		t.Errorf("Trigger.Schedule: got %q, want %q", assignment.Trigger.Schedule, "0 8 * * 1-5")
	}
	if len(assignment.Outputs) != 1 {
		t.Errorf("Outputs length: got %d, want 1", len(assignment.Outputs))
	}
	if assignment.Outputs[0].Plugin != "slack" {
		t.Errorf("Outputs[0].Plugin: got %q, want %q", assignment.Outputs[0].Plugin, "slack")
	}
	if assignment.Config["region"] != "us-east-1" {
		t.Errorf("Config[region]: got %v, want %q", assignment.Config["region"], "us-east-1")
	}
}

func TestRun_StructConstruction(t *testing.T) {
	assignmentID := uuid.New()
	llmResult := &domain.LLMResult{
		Summary: "analysis complete",
		Tokens:  512,
		Cost:    0.0023,
	}

	run := domain.Run{
		ID:           uuid.New(),
		AssignmentID: assignmentID,
		Status:       domain.RunStatusSucceeded,
		Tokens:       512,
		Cost:         0.0023,
		LLMResult:    llmResult,
	}

	if run.AssignmentID != assignmentID {
		t.Errorf("AssignmentID: got %v, want %v", run.AssignmentID, assignmentID)
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("Status: got %q, want %q", run.Status, domain.RunStatusSucceeded)
	}
	if run.Tokens != 512 {
		t.Errorf("Tokens: got %d, want 512", run.Tokens)
	}
	if run.Cost != 0.0023 {
		t.Errorf("Cost: got %v, want 0.0023", run.Cost)
	}
	if run.LLMResult == nil {
		t.Fatal("LLMResult must not be nil")
	}
	if run.LLMResult.Summary != "analysis complete" {
		t.Errorf("LLMResult.Summary: got %q, want %q", run.LLMResult.Summary, "analysis complete")
	}
}

func TestRun_NilLLMResult(t *testing.T) {
	run := domain.Run{
		ID:     uuid.New(),
		Status: domain.RunStatusQueued,
	}

	if run.LLMResult != nil {
		t.Error("LLMResult should be nil for a newly queued run")
	}
}
