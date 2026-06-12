package run

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

	// Registers the github plugin.
	_ "github.com/cbarraford/office-fleet/internal/plugins/github"
)

const githubIntegrationFixture = `{
  "action": "opened",
  "pull_request": {
    "number": 9,
    "title": "Integration PR",
    "merged": false,
    "html_url": "https://github.com/org/repo/pull/9",
    "head": {"ref": "feat/z", "sha": "cafef00d"},
    "base": {"ref": "main"},
    "user": {"login": "carol"}
  },
  "repository": {"full_name": "org/repo"}
}`

func TestEventVertical_GitHubWebhookToRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gh, ok := plugin.Get("github")
	if !ok {
		t.Fatal("github plugin not registered")
	}
	secrets := func(name string) (string, error) {
		if name == "github_webhook_secret" {
			return "gh-integration-secret", nil
		}
		return "", nil
	}
	if err := gh.Init(ctx, map[string]any{}, secrets); err != nil {
		t.Fatal(err)
	}

	recorder := &deliveryRecorder{name: "sp3b-recorder-plugin"}
	plugin.Register(recorder)

	backendName := "sp3b-backend"
	agentID, dutyID, assignmentID := uuid.New(), uuid.New(), uuid.New()
	cfg := &config.Config{Backends: []config.Backend{{
		Name: backendName, Kind: "claude", Model: "m",
		DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
	}}}
	rr := newFakeRunRepo()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: state.NewMemStore()}
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "pr-reviewed"})

	assignment := &domain.Assignment{
		ID: assignmentID, AgentID: agentID, DutyID: dutyID, Enabled: true,
		Backend: &domain.BackendRef{Name: backendName},
		Config:  map[string]any{},
		Trigger: domain.TriggerConfig{Kind: "event-subscription", Filter: map[string]any{
			"source": "github", "event_type": "pr_opened", "repo": "org/repo",
		}},
		Outputs: []domain.OutputBinding{{
			Plugin: "sp3b-recorder-plugin", Action: "post",
			Params: map[string]any{"body": "{{.Event.llm_summary}}", "pr": "{{.Event.pr_number}}"},
		}},
	}
	inv := NewInvokerWithExecutorBuilder(cfg, pipeline,
		&fakeAssignmentGetter{byID: map[uuid.UUID]*domain.Assignment{assignmentID: assignment}},
		&fakeAgentGetter{byID: map[uuid.UUID]*domain.Agent{agentID: {
			ID: agentID, Name: "sp3b-agent", Role: "dev", SystemPrompt: "reviewer",
			DefaultBackend: domain.BackendRef{Name: backendName}, Enabled: true,
		}}},
		&fakeDutyGetter{byID: map[uuid.UUID]*domain.Duty{dutyID: {
			ID: dutyID, Name: "sp3b-duty", Role: "dev", Description: "d",
			Prompt: "Review PR #{{.Event.pr_number}} by {{.Event.author}}",
		}}},
		func(_ *config.Config, _ *config.Backend) (executor.Executor, error) {
			return fakeExec, nil
		})

	store := events.NewMemStore()
	dispatcher := events.NewDispatcher(store, &staticAssignmentLister{list: []*domain.Assignment{assignment}}, inv, 2, 50*time.Millisecond)
	ingestor := events.NewIngestor(store, dispatcher.Notify)
	go dispatcher.Run(ctx)

	httpSrv := httptest.NewServer(server.New(ingestor).Handler())
	defer httpSrv.Close()

	mac := hmac.New(sha256.New, []byte("gh-integration-secret"))
	mac.Write([]byte(githubIntegrationFixture))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/webhooks/github",
		strings.NewReader(githubIntegrationFixture))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "pull_request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook status = %d", resp.StatusCode)
	}

	waitForCondition(t, 3*time.Second, func() bool {
		return len(rr.snapshot()) >= 1
	}, "no run recorded")

	waitForCondition(t, 3*time.Second, func() bool {
		return recorder.getParams() != nil
	}, "no output delivered")

	var run domain.Run
	for _, r := range rr.snapshot() {
		run = r
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("run status = %q", run.Status)
	}
	if run.EventID == nil {
		t.Fatal("run.EventID not stamped")
	}
	if !strings.Contains(run.RenderedPrompt, "#9") || !strings.Contains(run.RenderedPrompt, "carol") {
		t.Errorf("rendered prompt = %q, want PR fields", run.RenderedPrompt)
	}
	rparams := recorder.getParams()
	if rparams["body"] != "pr-reviewed" || rparams["pr"] != "9" {
		t.Errorf("delivered params = %v", rparams)
	}

	evID := uuid.MustParse(*run.EventID)
	waitForCondition(t, 2*time.Second, func() bool {
		ev, err := store.GetByID(ctx, evID)
		return err == nil && ev.Status == domain.EventStatusDispatched
	}, "event not marked dispatched")
}
