package run

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/google/uuid"
)

// deliveryRecorder is a plugin that records the params it was called with.
type deliveryRecorder struct {
	name   string
	params map[string]any
}

func (d *deliveryRecorder) Name() string                       { return d.name }
func (d *deliveryRecorder) EventSources() []plugin.EventSource { return nil }
func (d *deliveryRecorder) Actions() []plugin.Action           { return nil }
func (d *deliveryRecorder) ConfigSchema() plugin.Schema        { return nil }
func (d *deliveryRecorder) Init(_ context.Context, _ map[string]any, _ plugin.SecretLookup) error {
	return nil
}
func (d *deliveryRecorder) Do(_ context.Context, _ string, params map[string]any) (map[string]any, error) {
	d.params = params
	return map[string]any{}, nil
}

func TestPipelineExecute_EndpointBackendEndToEnd(t *testing.T) {
	ctx := context.Background()

	// Scripted openai-compatible server: one tool turn, then submit_result.
	call := 0
	responses := []string{
		`{
		  "choices": [{"message": {"role": "assistant", "content": "",
		    "tool_calls": [{"id": "c1", "type": "function",
		      "function": {"name": "run_command", "arguments": "{\"cmd\":\"echo inspecting\"}"}}]}}],
		  "usage": {"prompt_tokens": 20, "completion_tokens": 10}
		}`,
		`{
		  "choices": [{"message": {"role": "assistant", "content": "",
		    "tool_calls": [{"id": "c2", "type": "function",
		      "function": {"name": "submit_result",
		        "arguments": "{\"summary\":\"review complete\",\"status\":0,\"output\":{\"verdict\":\"approve\"}}"}}]}}],
		  "usage": {"prompt_tokens": 30, "completion_tokens": 15}
		}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if call >= len(responses) {
			t.Errorf("unexpected chat call #%d", call+1)
			w.WriteHeader(500)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "test-model" {
			t.Errorf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[call]))
		call++
	}))
	defer srv.Close()

	backendName := "ep-backend"
	cfg := &config.Config{
		Backends: []config.Backend{{
			Name:    backendName,
			Kind:    "openai-compatible",
			BaseURI: srv.URL,
			Model:   "test-model",
			Auth:    config.BackendAuth{Mode: "none"},
		}},
	}
	ep, err := executor.NewEndpointExecutor(&cfg.Backends[0])
	if err != nil {
		t.Fatal(err)
	}

	recorder := &deliveryRecorder{name: "ep-recorder-plugin"}
	plugin.Register(recorder)

	rr := newFakeRunRepo()
	pipeline := &Pipeline{
		cfg:     cfg,
		runRepo: rr,
		store:   state.NewMemStore(),
		plugins: map[string]plugin.Plugin{},
	}

	agentID, dutyID, assignmentID := uuid.New(), uuid.New(), uuid.New()
	run, err := pipeline.Execute(ctx, ExecuteRequest{
		Assignment: &domain.Assignment{
			ID: assignmentID, AgentID: agentID, DutyID: dutyID, Enabled: true,
			Backend: &domain.BackendRef{Name: backendName},
			Config:  map[string]any{},
			Outputs: []domain.OutputBinding{{
				Plugin: "ep-recorder-plugin",
				Action: "post",
				// llm_summary comes from the submit_result summary;
				// llm_output is the JSON-encoded Output map.
				Params: map[string]any{
					"body":   "{{.Event.llm_summary}}",
					"output": "{{.Event.llm_output}}",
				},
			}},
		},
		Agent: &domain.Agent{
			ID: agentID, Name: "ep-agent", Role: "developer",
			SystemPrompt: "You are a reviewer.", Enabled: true,
		},
		Duty: &domain.Duty{
			ID: dutyID, Name: "ep-duty", Role: "developer",
			Description: "endpoint duty", Prompt: "Review the code.",
			RequiredTools: []string{},
		},
		TriggerKind: "manual",
		EventParams: map[string]any{},
		Executor:    ep,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("status = %q, want succeeded", run.Status)
	}
	if run.LLMResult == nil || run.LLMResult.Summary != "review complete" {
		t.Fatalf("LLMResult = %+v", run.LLMResult)
	}
	if run.Tokens != 75 { // 30 + 45
		t.Errorf("Tokens = %d, want 75", run.Tokens)
	}
	if run.RenderedSystemPrompt == "" || run.RenderedPrompt == "" {
		t.Error("rendered prompts must be recorded")
	}
	// Output delivery rendered from the submit_result payload.
	if len(run.OutputsDelivered) != 1 || run.OutputsDelivered[0].Status != "delivered" {
		t.Fatalf("OutputsDelivered = %+v", run.OutputsDelivered)
	}
	if recorder.params["body"] != "review complete" {
		t.Errorf("delivered body = %v", recorder.params["body"])
	}
	outJSON, _ := recorder.params["output"].(string)
	if !strings.Contains(outJSON, `"verdict":"approve"`) {
		t.Errorf("delivered output = %q, want submit_result output JSON", outJSON)
	}
	// Transcript persisted on the recorded run.
	if !strings.Contains(run.LLMResult.Transcript, "run_command") {
		t.Error("transcript missing tool activity")
	}
}
