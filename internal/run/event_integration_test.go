package run

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/events"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/server"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/google/uuid"

	// Registers the gitlab plugin.
	_ "github.com/cbarraford/office-fleet/internal/plugins/gitlab"
)

const integrationWebhookFixture = `{
  "object_kind": "merge_request",
  "user": {"username": "alice"},
  "project": {"path_with_namespace": "org/repo"},
  "object_attributes": {
    "iid": 7,
    "title": "Integration MR",
    "action": "open",
    "source_branch": "feat/y",
    "target_branch": "main",
    "url": "https://gitlab.example.com/org/repo/-/merge_requests/7",
    "last_commit": {"id": "feedface"}
  }
}`

func TestEventVertical_WebhookToRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- real gitlab plugin, initialized with a webhook secret ---
	gl, ok := plugin.Get("gitlab")
	if !ok {
		t.Fatal("gitlab plugin not registered")
	}
	secrets := func(name string) (string, error) {
		if name == "gitlab_webhook_secret" {
			return "integration-secret", nil
		}
		return "", nil
	}
	if err := gl.Init(ctx, map[string]any{}, secrets); err != nil {
		t.Fatal(err)
	}

	// --- output recorder plugin ---
	recorder := &deliveryRecorder{name: "sp3-recorder-plugin"}
	plugin.Register(recorder)

	// --- pipeline + invoker with fakes ---
	backendName := "sp3-backend"
	agentID, dutyID, assignmentID := uuid.New(), uuid.New(), uuid.New()
	cfg := &config.Config{Backends: []config.Backend{{
		Name: backendName, Kind: "claude", Model: "m",
		DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
	}}}
	rr := newFakeRunRepo()
	memState := state.NewMemStore()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: memState}
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "auto-reviewed"})

	assignment := &domain.Assignment{
		ID: assignmentID, AgentID: agentID, DutyID: dutyID, Enabled: true,
		Backend: &domain.BackendRef{Name: backendName},
		Config:  map[string]any{},
		Trigger: domain.TriggerConfig{Kind: "event-subscription", Filter: map[string]any{
			"source": "gitlab", "event_type": "mr_opened", "project": "org/repo",
		}},
		Outputs: []domain.OutputBinding{{
			Plugin: "sp3-recorder-plugin", Action: "post",
			Params: map[string]any{"body": "{{.Event.llm_summary}}", "mr": "{{.Event.mr_iid}}"},
		}},
	}
	inv := &Invoker{
		cfg: cfg, pipeline: pipeline,
		assignments: &fakeAssignmentGetter{byID: map[uuid.UUID]*domain.Assignment{assignmentID: assignment}},
		agents: &fakeAgentLister{agents: []*domain.Agent{{
			ID: agentID, Name: "sp3-agent", Role: "dev", SystemPrompt: "reviewer",
			DefaultBackend: domain.BackendRef{Name: backendName}, Enabled: true,
		}}},
		duties: &fakeDutyLister{duties: []*domain.Duty{{
			ID: dutyID, Name: "sp3-duty", Role: "dev", Description: "d",
			Prompt: "Review MR !{{.Event.mr_iid}} by {{.Event.author}}",
		}}},
		buildExecutor: func(_ *config.Config, _ *config.Backend) (executor.Executor, error) {
			return fakeExec, nil
		},
	}

	// --- real eventing core over MemStore ---
	store := events.NewMemStore()
	dispatcher := events.NewDispatcher(store, &staticAssignmentLister{list: []*domain.Assignment{assignment}}, inv, 2, 50*time.Millisecond)
	ingestor := events.NewIngestor(store, dispatcher.Notify)
	go dispatcher.Run(ctx)

	// --- real webhook server ---
	httpSrv := httptest.NewServer(server.New(ingestor).Handler())
	defer httpSrv.Close()

	req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/webhooks/gitlab", strings.NewReader(integrationWebhookFixture))
	req.Header.Set("X-Gitlab-Token", "integration-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook status = %d", resp.StatusCode)
	}

	// --- wait for the run (use snapshot() to avoid data race with dispatcher) ---
	// Also wait until the run is in a terminal state (not just inserted).
	waitForCondition(t, 3*time.Second, func() bool {
		snap := rr.snapshot()
		if len(snap) < 1 {
			return false
		}
		for _, r := range snap {
			if r.Status == domain.RunStatusSucceeded || r.Status == domain.RunStatusFailed || r.Status == domain.RunStatusSkipped {
				return true
			}
		}
		return false
	}, "no run recorded in terminal state")

	// Take a final snapshot after the run has settled; no dispatcher writes
	// are expected at this point for this event.
	snap := rr.snapshot()
	var run domain.Run
	for _, r := range snap {
		run = r
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("run status = %q", run.Status)
	}
	if run.TriggerKind != "event-subscription" {
		t.Errorf("trigger kind = %q", run.TriggerKind)
	}
	if run.EventID == nil {
		t.Fatal("run.EventID not stamped")
	}
	if !strings.Contains(run.RenderedPrompt, "!7") || !strings.Contains(run.RenderedPrompt, "alice") {
		t.Errorf("rendered prompt = %q, want event fields", run.RenderedPrompt)
	}
	if recorder.params["body"] != "auto-reviewed" || recorder.params["mr"] != "7" {
		t.Errorf("delivered params = %v", recorder.params)
	}

	// --- event marked dispatched ---
	evID := uuid.MustParse(*run.EventID)
	waitForCondition(t, 2*time.Second, func() bool {
		ev, err := store.GetByID(ctx, evID)
		return err == nil && ev.Status == domain.EventStatusDispatched
	}, "event not marked dispatched")

	// --- duplicate webhook: same MR+SHA -> zero new events ---
	req2, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/webhooks/gitlab", strings.NewReader(integrationWebhookFixture))
	req2.Header.Set("X-Gitlab-Token", "integration-secret")
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("duplicate webhook status = %d", resp2.StatusCode)
	}

	// Event-level dedup: same MR+SHA via webhook again -> still exactly one
	// event and exactly one run.
	if pending, _ := store.ListPending(ctx, 10); len(pending) != 0 {
		t.Errorf("pending after duplicate webhook = %d, want 0 (dedup)", len(pending))
	}
	if got := len(rr.snapshot()); got != 1 {
		t.Errorf("runs after duplicate webhook = %d, want 1", got)
	}

	// NOTE: the replay's "exactly one skipped run" invariant relies on
	// dispatcher.Run serializing dispatch calls (single goroutine consumes
	// bus + ticker); the bus nudge and a rescan tick can both target this
	// event but never concurrently.
	// --- replay: re-queue and expect a dedup-SKIPPED second run ---
	if err := store.MarkPending(ctx, evID); err != nil {
		t.Fatal(err)
	}
	dispatcher.Notify(evID)
	waitForCondition(t, 3*time.Second, func() bool {
		snap := rr.snapshot()
		if len(snap) < 2 {
			return false
		}
		// Wait until the second run is also in a terminal state.
		terminal := 0
		for _, r := range snap {
			if r.Status == domain.RunStatusSucceeded || r.Status == domain.RunStatusFailed || r.Status == domain.RunStatusSkipped {
				terminal++
			}
		}
		return terminal >= 2
	}, "replay produced no second run in terminal state")
	skipped := 0
	for _, r := range rr.snapshot() {
		if r.Status == domain.RunStatusSkipped {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("skipped runs = %d, want exactly 1 (replay dedup)", skipped)
	}
}

type staticAssignmentLister struct{ list []*domain.Assignment }

func (s *staticAssignmentLister) List(_ context.Context) ([]*domain.Assignment, error) {
	return s.list, nil
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(timeout)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal(msg)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
