package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// --- In-memory fakes ---

type fakeAgentStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.Agent
}

func newFakeAgentStore() *fakeAgentStore {
	return &fakeAgentStore{rows: map[uuid.UUID]*domain.Agent{}}
}

func (f *fakeAgentStore) List(_ context.Context) ([]*domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.Agent, 0, len(f.rows))
	for _, a := range f.rows {
		cp := *a
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeAgentStore) GetByID(_ context.Context, id uuid.UUID) (*domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.rows[id]
	if !ok {
		return nil, fmt.Errorf("agent %s not found", id)
	}
	cp := *a
	return &cp, nil
}

func (f *fakeAgentStore) Insert(_ context.Context, a *domain.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Simulate unique name violation
	for _, existing := range f.rows {
		if existing.Name == a.Name {
			return fmt.Errorf("duplicate: 23505 unique constraint violation")
		}
	}
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	cp := *a
	f.rows[a.ID] = &cp
	return nil
}

func (f *fakeAgentStore) Update(_ context.Context, a *domain.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[a.ID]; !ok {
		return fmt.Errorf("agent %s not found", a.ID)
	}
	cp := *a
	f.rows[a.ID] = &cp
	return nil
}

func (f *fakeAgentStore) Delete(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[id]; !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	delete(f.rows, id)
	return nil
}

type fakeDutyStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.Duty
}

func newFakeDutyStore() *fakeDutyStore {
	return &fakeDutyStore{rows: map[uuid.UUID]*domain.Duty{}}
}

func (f *fakeDutyStore) List(_ context.Context) ([]*domain.Duty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.Duty, 0, len(f.rows))
	for _, d := range f.rows {
		cp := *d
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeDutyStore) GetByID(_ context.Context, id uuid.UUID) (*domain.Duty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.rows[id]
	if !ok {
		return nil, fmt.Errorf("duty %s not found", id)
	}
	cp := *d
	return &cp, nil
}

func (f *fakeDutyStore) Insert(_ context.Context, d *domain.Duty) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	cp := *d
	f.rows[d.ID] = &cp
	return nil
}

func (f *fakeDutyStore) Update(_ context.Context, d *domain.Duty) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[d.ID]; !ok {
		return fmt.Errorf("duty %s not found", d.ID)
	}
	cp := *d
	f.rows[d.ID] = &cp
	return nil
}

func (f *fakeDutyStore) Delete(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[id]; !ok {
		return fmt.Errorf("duty %s not found", id)
	}
	delete(f.rows, id)
	return nil
}

type fakeAssignmentStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.Assignment
}

func newFakeAssignmentStore() *fakeAssignmentStore {
	return &fakeAssignmentStore{rows: map[uuid.UUID]*domain.Assignment{}}
}

func (f *fakeAssignmentStore) List(_ context.Context) ([]*domain.Assignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.Assignment, 0, len(f.rows))
	for _, a := range f.rows {
		cp := *a
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeAssignmentStore) GetByID(_ context.Context, id uuid.UUID) (*domain.Assignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.rows[id]
	if !ok {
		return nil, fmt.Errorf("assignment %s not found", id)
	}
	cp := *a
	return &cp, nil
}

func (f *fakeAssignmentStore) Insert(_ context.Context, a *domain.Assignment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Simulate unique (agent_id, duty_id) violation
	for _, existing := range f.rows {
		if existing.AgentID == a.AgentID && existing.DutyID == a.DutyID {
			return fmt.Errorf("duplicate: 23505 unique constraint violation")
		}
	}
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	cp := *a
	f.rows[a.ID] = &cp
	return nil
}

func (f *fakeAssignmentStore) Update(_ context.Context, a *domain.Assignment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[a.ID]; !ok {
		return fmt.Errorf("assignment %s not found", a.ID)
	}
	cp := *a
	f.rows[a.ID] = &cp
	return nil
}

func (f *fakeAssignmentStore) Delete(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[id]; !ok {
		return fmt.Errorf("assignment %s not found", id)
	}
	delete(f.rows, id)
	return nil
}

type fakeRunStoreEntity struct {
	stats *domain.AgentStats
}

func (f *fakeRunStoreEntity) ListFiltered(_ context.Context, _ string, _ uuid.UUID, _ int) ([]*domain.Run, error) {
	return nil, nil
}

func (f *fakeRunStoreEntity) GetByID(_ context.Context, _ uuid.UUID) (*domain.Run, error) {
	return nil, fmt.Errorf("not found")
}

func (f *fakeRunStoreEntity) AgentStats(_ context.Context, id uuid.UUID) (*domain.AgentStats, error) {
	if f.stats != nil {
		return f.stats, nil
	}
	return &domain.AgentStats{AgentID: id}, nil
}

// --- Test fixture ---

type entityFixture struct {
	api    *API
	agents *fakeAgentStore
	duties *fakeDutyStore
	asgns  *fakeAssignmentStore
	runs   *fakeRunStoreEntity
	srv    *httptest.Server
	token  string
}

func newEntityFixture(t *testing.T) *entityFixture {
	t.Helper()
	agents := newFakeAgentStore()
	duties := newFakeDutyStore()
	asgns := newFakeAssignmentStore()
	runs := &fakeRunStoreEntity{}

	sessions := auth.NewSessions(newMemSessionStore(domain.RoleAdmin))
	token, err := sessions.Start(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}

	// A minimal config with one known backend for validation tests.
	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "claude-prod", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}},
		},
	}

	a := New(Deps{
		Agents:      agents,
		Duties:      duties,
		Assignments: asgns,
		Runs:        runs,
		Sessions:    sessions,
		Config:      cfg,
	})

	mux := http.NewServeMux()
	a.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &entityFixture{
		api:    a,
		agents: agents,
		duties: duties,
		asgns:  asgns,
		runs:   runs,
		srv:    srv,
		token:  token,
	}
}

func (f *entityFixture) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		buf = bytes.NewBuffer(b)
	} else {
		buf = &bytes.Buffer{}
	}
	req, err := http.NewRequest(method, f.srv.URL+path, buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: f.token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// --- Agent tests ---

func TestAgentCreate_201AndListed(t *testing.T) {
	f := newEntityFixture(t)

	resp := f.do(t, "POST", "/api/v1/agents", map[string]any{
		"name":    "Alice",
		"role":    "analyst",
		"enabled": true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status = %d, want 201", resp.StatusCode)
	}

	var created map[string]any
	decodeBody(t, resp, &created)
	if created["name"] != "Alice" {
		t.Errorf("created agent name = %v, want Alice", created["name"])
	}

	listResp := f.do(t, "GET", "/api/v1/agents", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: status = %d, want 200", listResp.StatusCode)
	}
	var agents []map[string]any
	decodeBody(t, listResp, &agents)
	if len(agents) != 1 {
		t.Fatalf("list returned %d agents, want 1", len(agents))
	}
	if agents[0]["name"] != "Alice" {
		t.Errorf("listed agent name = %v, want Alice", agents[0]["name"])
	}
}

func TestAgentCreate_DuplicateName_409(t *testing.T) {
	f := newEntityFixture(t)

	f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "Bob"})
	resp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "Bob"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create: status = %d, want 409", resp.StatusCode)
	}
}

func TestAgentPatch_PartialUpdate(t *testing.T) {
	f := newEntityFixture(t)

	createResp := f.do(t, "POST", "/api/v1/agents", map[string]any{
		"name":    "Charlie",
		"role":    "engineer",
		"enabled": true,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status = %d", createResp.StatusCode)
	}
	var created map[string]any
	decodeBody(t, createResp, &created)
	agentID := created["id"].(string)

	// PATCH only enabled → other fields must remain unchanged
	patchResp := f.do(t, "PATCH", "/api/v1/agents/"+agentID, map[string]any{
		"enabled": false,
	})
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("patch: status = %d, want 200", patchResp.StatusCode)
	}
	var patched map[string]any
	decodeBody(t, patchResp, &patched)
	if patched["enabled"] != false {
		t.Errorf("patched enabled = %v, want false", patched["enabled"])
	}
	if patched["name"] != "Charlie" {
		t.Errorf("name changed to %v, want Charlie", patched["name"])
	}
	if patched["role"] != "engineer" {
		t.Errorf("role changed to %v, want engineer", patched["role"])
	}
}

func TestAgentPatch_BadHiredAt_400(t *testing.T) {
	f := newEntityFixture(t)

	createResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "Dana"})
	var created map[string]any
	decodeBody(t, createResp, &created)
	agentID := created["id"].(string)

	resp := f.do(t, "PATCH", "/api/v1/agents/"+agentID, map[string]any{
		"hired_at": "not-a-date",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad hired_at: status = %d, want 400", resp.StatusCode)
	}
}

func TestAgentPatch_UnknownBackend_400(t *testing.T) {
	f := newEntityFixture(t)

	createResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "Eve"})
	var created map[string]any
	decodeBody(t, createResp, &created)
	agentID := created["id"].(string)

	resp := f.do(t, "PATCH", "/api/v1/agents/"+agentID, map[string]any{
		"default_backend": map[string]any{"name": "nonexistent-backend"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown backend: status = %d, want 400", resp.StatusCode)
	}
}

func TestAgentGet_EmbedsStats(t *testing.T) {
	f := newEntityFixture(t)

	createResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "Frank"})
	var created map[string]any
	decodeBody(t, createResp, &created)
	agentID := created["id"].(string)

	// Seed the fake run store with stats for this agent
	id, _ := uuid.Parse(agentID)
	f.runs.stats = &domain.AgentStats{
		AgentID:   id,
		TotalRuns: 42,
	}

	resp := f.do(t, "GET", "/api/v1/agents/"+agentID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	decodeBody(t, resp, &body)
	if body["agent"] == nil {
		t.Fatal("response missing 'agent' key")
	}
	if body["stats"] == nil {
		t.Fatal("response missing 'stats' key")
	}
	stats := body["stats"].(map[string]any)
	if stats["total_runs"] == nil {
		t.Fatal("stats missing total_runs")
	}
	// JSON numbers decode as float64
	if stats["total_runs"].(float64) != 42 {
		t.Errorf("total_runs = %v, want 42", stats["total_runs"])
	}
}

func TestAgentDelete_404After(t *testing.T) {
	f := newEntityFixture(t)

	createResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "Grace"})
	var created map[string]any
	decodeBody(t, createResp, &created)
	agentID := created["id"].(string)

	delResp := f.do(t, "DELETE", "/api/v1/agents/"+agentID, nil)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status = %d, want 200", delResp.StatusCode)
	}

	getResp := f.do(t, "GET", "/api/v1/agents/"+agentID, nil)
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", getResp.StatusCode)
	}
}

// --- Duty tests ---

func TestDutyCreate_BadTriggerKind_400(t *testing.T) {
	f := newEntityFixture(t)

	resp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name":          "ReviewDuty",
		"trigger_kinds": []string{"manual", "foobar"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad trigger kind: status = %d, want 400", resp.StatusCode)
	}
	var body map[string]any
	decodeBody(t, resp, &body)
	if !strings.Contains(body["error"].(string), "foobar") {
		t.Errorf("error should mention 'foobar': %v", body["error"])
	}
}

func TestDutyCreate_ValidTriggerKinds_201(t *testing.T) {
	f := newEntityFixture(t)

	resp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name":          "MRReview",
		"trigger_kinds": []string{"manual", "event-subscription"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("valid duty create: status = %d, want 201", resp.StatusCode)
	}
}

// --- Assignment tests ---

func TestAssignment_EventSubscription_MissingSource_400(t *testing.T) {
	f := newEntityFixture(t)

	// Create the duty that supports event-subscription
	dutyResp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name":          "OnEvent",
		"trigger_kinds": []string{"event-subscription"},
	})
	var duty map[string]any
	decodeBody(t, dutyResp, &duty)
	dutyID := duty["id"].(string)

	agentResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "AgentH"})
	var agent map[string]any
	decodeBody(t, agentResp, &agent)
	agentID := agent["id"].(string)

	// Missing source in filter
	resp := f.do(t, "POST", "/api/v1/assignments", map[string]any{
		"agent_id": agentID,
		"duty_id":  dutyID,
		"trigger": map[string]any{
			"kind":   "event-subscription",
			"filter": map[string]any{"event_type": "mr_opened"}, // source missing
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing source: status = %d, want 400", resp.StatusCode)
	}
}

func TestAssignment_DutyKindMismatch_400(t *testing.T) {
	f := newEntityFixture(t)

	// Duty only supports "manual"
	dutyResp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name":          "ManualOnly",
		"trigger_kinds": []string{"manual"},
	})
	var duty map[string]any
	decodeBody(t, dutyResp, &duty)
	dutyID := duty["id"].(string)

	agentResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "AgentI"})
	var agent map[string]any
	decodeBody(t, agentResp, &agent)
	agentID := agent["id"].(string)

	// Trigger kind "cron" not in duty's trigger_kinds
	resp := f.do(t, "POST", "/api/v1/assignments", map[string]any{
		"agent_id": agentID,
		"duty_id":  dutyID,
		"trigger": map[string]any{
			"kind":     "cron",
			"schedule": "0 * * * *",
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duty kind mismatch: status = %d, want 400", resp.StatusCode)
	}
}

func TestAssignment_Valid_201(t *testing.T) {
	f := newEntityFixture(t)

	dutyResp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name":          "CronDuty",
		"trigger_kinds": []string{"cron"},
	})
	var duty map[string]any
	decodeBody(t, dutyResp, &duty)
	dutyID := duty["id"].(string)

	agentResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "AgentJ"})
	var agent map[string]any
	decodeBody(t, agentResp, &agent)
	agentID := agent["id"].(string)

	resp := f.do(t, "POST", "/api/v1/assignments", map[string]any{
		"agent_id": agentID,
		"duty_id":  dutyID,
		"trigger": map[string]any{
			"kind":     "cron",
			"schedule": "0 * * * *",
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("valid assignment create: status = %d, want 201", resp.StatusCode)
	}
	var body map[string]any
	decodeBody(t, resp, &body)
	if body["id"] == nil {
		t.Fatal("response missing ID")
	}
}

func TestAssignment_EventSubscription_Valid_201(t *testing.T) {
	f := newEntityFixture(t)

	dutyResp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name":          "EventDuty",
		"trigger_kinds": []string{"event-subscription"},
	})
	var duty map[string]any
	decodeBody(t, dutyResp, &duty)
	dutyID := duty["id"].(string)

	agentResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "AgentK"})
	var agent map[string]any
	decodeBody(t, agentResp, &agent)
	agentID := agent["id"].(string)

	resp := f.do(t, "POST", "/api/v1/assignments", map[string]any{
		"agent_id": agentID,
		"duty_id":  dutyID,
		"trigger": map[string]any{
			"kind": "event-subscription",
			"filter": map[string]any{
				"source":     "gitlab",
				"event_type": "mr_opened",
			},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("valid event-subscription assignment: status = %d, want 201", resp.StatusCode)
	}
}

func TestAssignment_ForEachValidation_Create(t *testing.T) {
	tests := []struct {
		name       string
		forEach    string
		wantStatus int
	}{
		{name: "bare key", forEach: "issues", wantStatus: http.StatusCreated},
		{name: "template expression", forEach: "{{.Event.x}}", wantStatus: http.StatusBadRequest},
		{name: "path expression", forEach: "issues[0]", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEntityFixture(t)
			dutyResp := f.do(t, "POST", "/api/v1/duties", map[string]any{
				"name":          "ForEachDuty",
				"trigger_kinds": []string{"manual"},
			})
			var duty map[string]any
			decodeBody(t, dutyResp, &duty)

			agentResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "ForEachAgent"})
			var agent map[string]any
			decodeBody(t, agentResp, &agent)

			resp := f.do(t, "POST", "/api/v1/assignments", map[string]any{
				"agent_id": agent["id"].(string),
				"duty_id":  duty["id"].(string),
				"outputs": []map[string]any{{
					"plugin":   "gitlab",
					"action":   "create_issue",
					"for_each": tt.forEach,
				}},
			})
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusBadRequest {
				var body map[string]any
				decodeBody(t, resp, &body)
				if !strings.Contains(body["error"].(string), "for_each") {
					t.Errorf("error should mention for_each: %v", body["error"])
				}
			}
		})
	}
}

func TestAssignment_ForEachValidation_Patch(t *testing.T) {
	f := newEntityFixture(t)
	dutyResp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name":          "PatchForEachDuty",
		"trigger_kinds": []string{"manual"},
	})
	var duty map[string]any
	decodeBody(t, dutyResp, &duty)

	agentResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "PatchForEachAgent"})
	var agent map[string]any
	decodeBody(t, agentResp, &agent)

	createResp := f.do(t, "POST", "/api/v1/assignments", map[string]any{
		"agent_id": agent["id"].(string),
		"duty_id":  duty["id"].(string),
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create assignment: status = %d, want 201", createResp.StatusCode)
	}
	var created map[string]any
	decodeBody(t, createResp, &created)

	patchResp := f.do(t, "PATCH", "/api/v1/assignments/"+created["id"].(string), map[string]any{
		"outputs": []map[string]any{{
			"plugin":   "gitlab",
			"action":   "create_issue",
			"for_each": "issues.items",
		}},
	})
	if patchResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch invalid for_each: status = %d, want 400", patchResp.StatusCode)
	}
	var body map[string]any
	decodeBody(t, patchResp, &body)
	if !strings.Contains(body["error"].(string), "for_each") {
		t.Errorf("error should mention for_each: %v", body["error"])
	}
}

// TestAssignment_Duplicate_409 verifies that creating the same (agent_id,
// duty_id) pair twice returns 409 Conflict.
func TestAssignment_Duplicate_409(t *testing.T) {
	f := newEntityFixture(t)

	dutyResp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name":          "DupDuty",
		"trigger_kinds": []string{"manual"},
	})
	var duty map[string]any
	decodeBody(t, dutyResp, &duty)
	dutyID := duty["id"].(string)

	agentResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "AgentDup"})
	var agent map[string]any
	decodeBody(t, agentResp, &agent)
	agentID := agent["id"].(string)

	body := map[string]any{
		"agent_id": agentID,
		"duty_id":  dutyID,
	}
	first := f.do(t, "POST", "/api/v1/assignments", body)
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201", first.StatusCode)
	}

	second := f.do(t, "POST", "/api/v1/assignments", body)
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create: status = %d, want 409", second.StatusCode)
	}
}

// TestAssignment_UnknownDutyID_400 verifies that a random duty_id returns 400.
func TestAssignment_UnknownDutyID_400(t *testing.T) {
	f := newEntityFixture(t)

	agentResp := f.do(t, "POST", "/api/v1/agents", map[string]any{"name": "AgentUnkDuty"})
	var agent map[string]any
	decodeBody(t, agentResp, &agent)
	agentID := agent["id"].(string)

	resp := f.do(t, "POST", "/api/v1/assignments", map[string]any{
		"agent_id": agentID,
		"duty_id":  uuid.New().String(),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown duty_id: status = %d, want 400", resp.StatusCode)
	}
	var body map[string]any
	decodeBody(t, resp, &body)
	if !strings.Contains(body["error"].(string), "duty_id") {
		t.Errorf("error should mention 'duty_id': %v", body["error"])
	}
}

// TestAssignment_UnknownAgentID_400 verifies that a random agent_id returns 400.
func TestAssignment_UnknownAgentID_400(t *testing.T) {
	f := newEntityFixture(t)

	dutyResp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name":          "UnkAgentDuty",
		"trigger_kinds": []string{"manual"},
	})
	var duty map[string]any
	decodeBody(t, dutyResp, &duty)
	dutyID := duty["id"].(string)

	resp := f.do(t, "POST", "/api/v1/assignments", map[string]any{
		"agent_id": uuid.New().String(),
		"duty_id":  dutyID,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown agent_id: status = %d, want 400", resp.StatusCode)
	}
	var body map[string]any
	decodeBody(t, resp, &body)
	if !strings.Contains(body["error"].(string), "agent_id") {
		t.Errorf("error should mention 'agent_id': %v", body["error"])
	}
}

// TestPatchDutyConfigSchemaSemantics verifies that explicit JSON null clears
// config_schema while an absent field leaves it untouched (PATCH semantics).
func TestPatchDutyConfigSchemaSemantics(t *testing.T) {
	f := newEntityFixture(t)

	// Create a duty with a non-empty config_schema.
	createResp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name": "SchemaDuty",
		"config_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ticket": map[string]any{"type": "string"},
			},
		},
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create duty: status = %d, want 201", createResp.StatusCode)
	}
	var created map[string]any
	decodeBody(t, createResp, &created)
	dutyID := created["id"].(string)

	// Sanity: GET confirms schema is set.
	getResp := f.do(t, "GET", "/api/v1/duties/"+dutyID, nil)
	var got map[string]any
	decodeBody(t, getResp, &got)
	if got["config_schema"] == nil {
		t.Fatal("precondition: config_schema should be set after create")
	}

	// PATCH with "config_schema": null → should clear the schema.
	// json.Marshal(nil interface{}) produces "null", which is what we want.
	patchNullResp := f.do(t, "PATCH", "/api/v1/duties/"+dutyID, map[string]any{
		"config_schema": nil,
	})
	if patchNullResp.StatusCode != http.StatusOK {
		t.Fatalf("patch null config_schema: status = %d, want 200", patchNullResp.StatusCode)
	}
	var afterNull map[string]any
	decodeBody(t, patchNullResp, &afterNull)
	if afterNull["config_schema"] != nil {
		t.Errorf("after PATCH null: config_schema = %v, want nil/null", afterNull["config_schema"])
	}

	// Re-seed schema via POST (create a fresh duty).
	create2Resp := f.do(t, "POST", "/api/v1/duties", map[string]any{
		"name": "SchemaDuty2",
		"config_schema": map[string]any{
			"type": "object",
		},
	})
	if create2Resp.StatusCode != http.StatusCreated {
		t.Fatalf("create duty2: status = %d, want 201", create2Resp.StatusCode)
	}
	var created2 map[string]any
	decodeBody(t, create2Resp, &created2)
	duty2ID := created2["id"].(string)

	// PATCH with config_schema absent → schema must be preserved.
	patchNameResp := f.do(t, "PATCH", "/api/v1/duties/"+duty2ID, map[string]any{
		"name": "SchemaDuty2-renamed",
	})
	if patchNameResp.StatusCode != http.StatusOK {
		t.Fatalf("patch name only: status = %d, want 200", patchNameResp.StatusCode)
	}
	var afterAbsent map[string]any
	decodeBody(t, patchNameResp, &afterAbsent)
	if afterAbsent["config_schema"] == nil {
		t.Errorf("after PATCH absent field: config_schema should be preserved but got nil")
	}
}

// TestAgentCreate_KnownBackend ensures a known backend ref is accepted.
func TestAgentCreate_KnownBackend_201(t *testing.T) {
	f := newEntityFixture(t)

	resp := f.do(t, "POST", "/api/v1/agents", map[string]any{
		"name": "BackendAgent",
		"default_backend": map[string]any{
			"name": "claude-prod",
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("known backend: status = %d, want 201", resp.StatusCode)
	}
}

// TestAgentHiredAt verifies successful hired_at parsing on create.
func TestAgentCreate_HiredAt_201(t *testing.T) {
	f := newEntityFixture(t)

	resp := f.do(t, "POST", "/api/v1/agents", map[string]any{
		"name":     "HiredAgent",
		"hired_at": time.Now().Format("2006-01-02"),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("hired_at create: status = %d, want 201", resp.StatusCode)
	}
}
