package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/secrets"
	"github.com/google/uuid"
)

// --- Fakes for record-level stores (run/event/secret/user/invoker) ---

type fakeRunStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.Run
}

func newFakeRunStore() *fakeRunStore {
	return &fakeRunStore{rows: map[uuid.UUID]*domain.Run{}}
}

func (f *fakeRunStore) ListFiltered(_ context.Context, status string, agentID uuid.UUID, limit int) ([]*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.Run, 0)
	for _, r := range f.rows {
		if status != "" && string(r.Status) != status {
			continue
		}
		if agentID != uuid.Nil && r.AgentID != agentID {
			continue
		}
		cp := *r
		out = append(out, &cp)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeRunStore) GetByID(_ context.Context, id uuid.UUID) (*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return nil, fmt.Errorf("run %s not found", id)
	}
	cp := *r
	return &cp, nil
}

func (f *fakeRunStore) AgentStats(_ context.Context, agentID uuid.UUID) (*domain.AgentStats, error) {
	return &domain.AgentStats{AgentID: agentID}, nil
}

type fakeEventStore struct {
	mu           sync.Mutex
	rows         map[uuid.UUID]*domain.Event
	pendingCalls []uuid.UUID
}

func newFakeEventStore() *fakeEventStore {
	return &fakeEventStore{rows: map[uuid.UUID]*domain.Event{}}
}

func (f *fakeEventStore) ListRecent(_ context.Context, status string, limit int) ([]*domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.Event, 0)
	for _, e := range f.rows {
		if status != "" && string(e.Status) != status {
			continue
		}
		cp := *e
		out = append(out, &cp)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeEventStore) GetByID(_ context.Context, id uuid.UUID) (*domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.rows[id]
	if !ok {
		return nil, fmt.Errorf("event %s not found", id)
	}
	cp := *e
	return &cp, nil
}

func (f *fakeEventStore) MarkPending(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pendingCalls = append(f.pendingCalls, id)
	return nil
}

type fakeSecretStore struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{rows: map[string][]byte{}}
}

func (f *fakeSecretStore) Upsert(_ context.Context, name string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	f.rows[name] = cp
	return nil
}

func (f *fakeSecretStore) List(_ context.Context) (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string][]byte, len(f.rows))
	for k, v := range f.rows {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out, nil
}

func (f *fakeSecretStore) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[name]; !ok {
		return fmt.Errorf("secret %q not found", name)
	}
	delete(f.rows, name)
	return nil
}

type fakeUserStore struct {
	mu    sync.Mutex
	users map[string]*domain.User
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{users: map[string]*domain.User{}}
}

func (f *fakeUserStore) GetByUsername(_ context.Context, username string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[username]
	if !ok {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}

type fakeInvoker struct {
	mu        sync.Mutex
	calls     []invokerCall
	returnRun *domain.Run
}

type invokerCall struct {
	assignmentID uuid.UUID
	triggerKind  string
	eventID      *string
	params       map[string]any
}

func newFakeInvoker(returnRun *domain.Run) *fakeInvoker {
	return &fakeInvoker{returnRun: returnRun}
}

func (f *fakeInvoker) Invoke(_ context.Context, assignmentID uuid.UUID, triggerKind string, eventID *string, params map[string]any) (*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, invokerCall{
		assignmentID: assignmentID,
		triggerKind:  triggerKind,
		eventID:      eventID,
		params:       params,
	})
	if f.returnRun != nil {
		return f.returnRun, nil
	}
	return &domain.Run{
		ID:           uuid.New(),
		AssignmentID: assignmentID,
		TriggerKind:  triggerKind,
		Status:       domain.RunStatusRunning,
	}, nil
}

// --- flexMemSessionStore: per-userID role ---

type flexMemSessionStore struct {
	mu    sync.Mutex
	rows  map[string]flexSession
	roles map[uuid.UUID]string
}

type flexSession struct {
	userID uuid.UUID
}

func (m *flexMemSessionStore) setRole(userID uuid.UUID, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.roles == nil {
		m.roles = map[uuid.UUID]string{}
	}
	m.roles[userID] = role
}

func (m *flexMemSessionStore) Create(_ context.Context, tokenHash string, userID uuid.UUID, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[tokenHash] = flexSession{userID: userID}
	return nil
}

func (m *flexMemSessionStore) Lookup(_ context.Context, tokenHash string) (uuid.UUID, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[tokenHash]
	if !ok {
		return uuid.Nil, "", fmt.Errorf("session not found")
	}
	role := domain.RoleAdmin
	if m.roles != nil {
		if r, ok := m.roles[s.userID]; ok {
			role = r
		}
	}
	return s.userID, role, nil
}

func (m *flexMemSessionStore) Delete(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, tokenHash)
	return nil
}

func (m *flexMemSessionStore) DeleteExpired(_ context.Context) error { return nil }

// --- Integration fixture ---

type apiFixture struct {
	api         *API
	srv         *httptest.Server
	client      *http.Client
	userStore   *fakeUserStore
	runStore    *fakeRunStore
	eventStore  *fakeEventStore
	secretStore *fakeSecretStore
	invoker     *fakeInvoker
	sessions    *auth.Sessions

	adminToken  string
	viewerToken string
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()

	userStore := newFakeUserStore()
	runStore := newFakeRunStore()
	eventStore := newFakeEventStore()
	secretStore := newFakeSecretStore()

	flexSessStore := &flexMemSessionStore{rows: map[string]flexSession{}}
	sessions := auth.NewSessions(flexSessStore)

	// Create admin user with a real PBKDF2 hash (one hash ~0.3s)
	adminHash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	adminUserID := uuid.New()
	userStore.users["admin"] = &domain.User{
		ID:           adminUserID,
		Username:     "admin",
		PasswordHash: adminHash,
		Role:         domain.RoleAdmin,
	}

	// Viewer user — session started directly, no need to hash a password
	viewerUserID := uuid.New()
	userStore.users["viewer"] = &domain.User{
		ID:       viewerUserID,
		Username: "viewer",
		// dummy hash that will always fail VerifyPassword
		PasswordHash: "pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA==$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		Role:         domain.RoleViewer,
	}

	// Start sessions for both users
	flexSessStore.setRole(adminUserID, domain.RoleAdmin)
	adminToken, err := sessions.Start(context.Background(), adminUserID)
	if err != nil {
		t.Fatal(err)
	}

	flexSessStore.setRole(viewerUserID, domain.RoleViewer)
	viewerToken, err := sessions.Start(context.Background(), viewerUserID)
	if err != nil {
		t.Fatal(err)
	}

	// Real secrets cipher for encryption tests
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher(base64.StdEncoding.EncodeToString(keyBytes))
	if err != nil {
		t.Fatal(err)
	}

	returnRun := &domain.Run{
		ID:          uuid.New(),
		TriggerKind: "manual",
		Status:      domain.RunStatusRunning,
	}
	inv := newFakeInvoker(returnRun)

	// Config with a backend that has an api_key (to test redaction)
	cfg := &config.Config{
		Backends: []config.Backend{
			{
				Name:    "secure-backend",
				Kind:    "openai-compatible",
				Auth:    config.BackendAuth{Mode: "api_key", APIKey: "secret-api-key-12345"},
				BaseURI: "https://example.com",
				Model:   "gpt-4",
			},
		},
	}

	a := New(Deps{
		Agents:      newFakeAgentStore(),
		Duties:      newFakeDutyStore(),
		Assignments: newFakeAssignmentStore(),
		Runs:        runStore,
		Events:      eventStore,
		Secrets:     secretStore,
		Users:       userStore,
		Sessions:    sessions,
		Invoker:     inv,
		Encryptor:   cipher,
		IsEncrypted: secrets.IsEncrypted,
		Config:      cfg,
	})

	mux := http.NewServeMux()
	a.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &apiFixture{
		api:         a,
		srv:         srv,
		client:      client,
		userStore:   userStore,
		runStore:    runStore,
		eventStore:  eventStore,
		secretStore: secretStore,
		invoker:     inv,
		sessions:    sessions,
		adminToken:  adminToken,
		viewerToken: viewerToken,
	}
}

// doJSON performs a JSON request with optional session cookie.
func (f *apiFixture) doJSON(t *testing.T, method, path string, body any, token string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
		r = &buf
	} else {
		r = strings.NewReader("")
	}
	req, err := http.NewRequest(method, f.srv.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readBodyJSON(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// --- Tests ---

func TestAPI_Login_WrongPassword_401(t *testing.T) {
	f := newAPIFixture(t)
	resp := f.doJSON(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin",
		"password": "wrong-password",
	}, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", resp.StatusCode)
	}
	var body map[string]string
	readBodyJSON(t, resp, &body)
	if body["error"] != "invalid credentials" {
		t.Errorf("wrong password error = %q, want 'invalid credentials'", body["error"])
	}
}

func TestAPI_Login_UnknownUser_401_IdenticalBody(t *testing.T) {
	f := newAPIFixture(t)

	respUnknown := f.doJSON(t, "POST", "/api/v1/login", map[string]string{
		"username": "no-such-user",
		"password": "anything",
	}, "")
	if respUnknown.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown user: status = %d, want 401", respUnknown.StatusCode)
	}
	var bodyUnknown map[string]string
	readBodyJSON(t, respUnknown, &bodyUnknown)

	respWrong := f.doJSON(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin",
		"password": "wrong-password",
	}, "")
	var bodyWrong map[string]string
	readBodyJSON(t, respWrong, &bodyWrong)

	// Both must return identical error bodies (no username oracle)
	if bodyUnknown["error"] != bodyWrong["error"] {
		t.Errorf("unknown-user body %q != wrong-password body %q (username oracle!)",
			bodyUnknown["error"], bodyWrong["error"])
	}
}

func TestAPI_Login_OK_Cookie_Me_Logout(t *testing.T) {
	f := newAPIFixture(t)

	resp := f.doJSON(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin",
		"password": "correct-password",
	}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status = %d, want 200", resp.StatusCode)
	}

	// Verify session cookie is HttpOnly
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("login response missing session cookie")
	}
	if !sessionCookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}

	// /me with the cookie
	meResp := f.doJSON(t, "GET", "/api/v1/me", nil, sessionCookie.Value)
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("/me: status = %d, want 200", meResp.StatusCode)
	}
	var meBody map[string]string
	readBodyJSON(t, meResp, &meBody)
	if meBody["role"] != domain.RoleAdmin {
		t.Errorf("/me role = %q, want %q", meBody["role"], domain.RoleAdmin)
	}

	// Logout
	logoutResp := f.doJSON(t, "POST", "/api/v1/logout", nil, sessionCookie.Value)
	if logoutResp.StatusCode != http.StatusOK {
		t.Fatalf("logout: status = %d, want 200", logoutResp.StatusCode)
	}

	// After logout /me must return 401
	afterResp := f.doJSON(t, "GET", "/api/v1/me", nil, sessionCookie.Value)
	if afterResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout /me: status = %d, want 401", afterResp.StatusCode)
	}
}

func TestAPI_Viewer_GET_200_PutSecret_403(t *testing.T) {
	f := newAPIFixture(t)

	// Viewer GET /api/v1/runs → 200
	getResp := f.doJSON(t, "GET", "/api/v1/runs", nil, f.viewerToken)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("viewer GET /runs: status = %d, want 200", getResp.StatusCode)
	}

	// Viewer PUT secret → 403 (mutation not allowed for viewer)
	putResp := f.doJSON(t, "PUT", "/api/v1/secrets/mykey", map[string]string{"value": "myval"}, f.viewerToken)
	if putResp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT /secrets: status = %d, want 403", putResp.StatusCode)
	}
}

func TestAPI_RunNow_ParamsFlowToInvoker(t *testing.T) {
	f := newAPIFixture(t)

	asgn := &domain.Assignment{ID: uuid.New(), Enabled: true}
	f.api.assignments.(*fakeAssignmentStore).mu.Lock()
	f.api.assignments.(*fakeAssignmentStore).rows[asgn.ID] = asgn
	f.api.assignments.(*fakeAssignmentStore).mu.Unlock()

	params := map[string]any{"mr_iid": "7"}
	resp := f.doJSON(t, "POST", "/api/v1/assignments/"+asgn.ID.String()+"/run",
		map[string]any{"params": params}, f.adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run-now: status = %d, want 200", resp.StatusCode)
	}

	var run domain.Run
	readBodyJSON(t, resp, &run)
	if run.TriggerKind != "manual" {
		t.Errorf("run.TriggerKind = %q, want manual", run.TriggerKind)
	}

	f.invoker.mu.Lock()
	defer f.invoker.mu.Unlock()
	if len(f.invoker.calls) != 1 {
		t.Fatalf("invoker called %d times, want 1", len(f.invoker.calls))
	}
	call := f.invoker.calls[0]
	if call.assignmentID != asgn.ID {
		t.Errorf("invoker assignment_id = %v, want %v", call.assignmentID, asgn.ID)
	}
	if call.triggerKind != "manual" {
		t.Errorf("invoker trigger_kind = %q, want manual", call.triggerKind)
	}
	if call.eventID != nil {
		t.Errorf("invoker eventID = %v, want nil", call.eventID)
	}
	if call.params["mr_iid"] != "7" {
		t.Errorf("invoker params[mr_iid] = %v, want 7", call.params["mr_iid"])
	}
}

func TestAPI_RunNow_EmptyParamsDefaultsToMap(t *testing.T) {
	f := newAPIFixture(t)

	asgn := &domain.Assignment{ID: uuid.New(), Enabled: true}
	f.api.assignments.(*fakeAssignmentStore).mu.Lock()
	f.api.assignments.(*fakeAssignmentStore).rows[asgn.ID] = asgn
	f.api.assignments.(*fakeAssignmentStore).mu.Unlock()

	// No body at all — params should default to empty map (not nil)
	resp := f.doJSON(t, "POST", "/api/v1/assignments/"+asgn.ID.String()+"/run", nil, f.adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run-now no body: status = %d, want 200", resp.StatusCode)
	}

	f.invoker.mu.Lock()
	defer f.invoker.mu.Unlock()
	if len(f.invoker.calls) != 1 {
		t.Fatalf("invoker called %d times, want 1", len(f.invoker.calls))
	}
	if f.invoker.calls[0].params == nil {
		t.Error("invoker params must be non-nil empty map, not nil")
	}
}

func TestAPI_ReplayEvent_MarksPendingAndNotifies(t *testing.T) {
	f := newAPIFixture(t)

	eventID := uuid.New()
	f.eventStore.mu.Lock()
	f.eventStore.rows[eventID] = &domain.Event{
		ID:     eventID,
		Status: domain.EventStatusDispatched,
	}
	f.eventStore.mu.Unlock()

	var notifiedID uuid.UUID
	var notifyMu sync.Mutex
	f.api.notify = func(id uuid.UUID) {
		notifyMu.Lock()
		notifiedID = id
		notifyMu.Unlock()
	}

	resp := f.doJSON(t, "POST", "/api/v1/events/"+eventID.String()+"/replay", nil, f.adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay: status = %d, want 200", resp.StatusCode)
	}

	f.eventStore.mu.Lock()
	calls := append([]uuid.UUID{}, f.eventStore.pendingCalls...)
	f.eventStore.mu.Unlock()
	if len(calls) != 1 || calls[0] != eventID {
		t.Errorf("MarkPending calls = %v, want [%v]", calls, eventID)
	}

	notifyMu.Lock()
	nid := notifiedID
	notifyMu.Unlock()
	if nid != eventID {
		t.Errorf("notify called with %v, want %v", nid, eventID)
	}
}

func TestAPI_ListBackends_RedactsAPIKey(t *testing.T) {
	f := newAPIFixture(t)

	resp := f.doJSON(t, "GET", "/api/v1/backends", nil, f.adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list backends: status = %d, want 200", resp.StatusCode)
	}

	// Read the full body as a string and also decode it
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read backends body: %v", err)
	}
	bodyStr := string(bodyBytes)

	if strings.Contains(bodyStr, "secret-api-key-12345") {
		t.Error("response body contains the api_key — must be redacted")
	}

	var backends []map[string]any
	if err := json.Unmarshal(bodyBytes, &backends); err != nil {
		t.Fatalf("decode backends: %v", err)
	}
	if len(backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(backends))
	}
	if backends[0]["name"] != "secure-backend" {
		t.Errorf("backend name = %v, want secure-backend", backends[0]["name"])
	}
	if _, hasAPIKey := backends[0]["api_key"]; hasAPIKey {
		t.Error("response must not contain api_key field")
	}
	if _, hasAuth := backends[0]["auth"]; hasAuth {
		t.Error("response must not contain auth field")
	}
	// auth_mode should be present (safe field)
	if backends[0]["auth_mode"] != "api_key" {
		t.Errorf("auth_mode = %v, want api_key", backends[0]["auth_mode"])
	}
}

func TestAPI_Secrets_PutListDelete(t *testing.T) {
	f := newAPIFixture(t)

	plaintext := "my-super-secret-token"

	// PUT /api/v1/secrets/mytoken
	putResp := f.doJSON(t, "PUT", "/api/v1/secrets/mytoken",
		map[string]string{"value": plaintext}, f.adminToken)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT secret: status = %d, want 200", putResp.StatusCode)
	}

	// Verify the stored bytes are FSEC1-prefixed (encrypted)
	f.secretStore.mu.Lock()
	stored, ok := f.secretStore.rows["mytoken"]
	f.secretStore.mu.Unlock()
	if !ok {
		t.Fatal("secret not stored in fake store")
	}
	if !secrets.IsEncrypted(stored) {
		t.Error("stored secret must have FSEC1 prefix (be encrypted)")
	}

	// GET /api/v1/secrets — must return name+encrypted only, no plaintext
	listResp := f.doJSON(t, "GET", "/api/v1/secrets", nil, f.adminToken)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list secrets: status = %d, want 200", listResp.StatusCode)
	}

	listBytes, err := io.ReadAll(listResp.Body)
	if err != nil {
		t.Fatalf("read list body: %v", err)
	}
	if strings.Contains(string(listBytes), plaintext) {
		t.Error("list response body must NOT contain the plaintext secret value")
	}

	var entries []map[string]any
	if err := json.Unmarshal(listBytes, &entries); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 secret entry, got %d", len(entries))
	}
	if entries[0]["name"] != "mytoken" {
		t.Errorf("secret name = %v, want mytoken", entries[0]["name"])
	}
	if entries[0]["encrypted"] != true {
		t.Errorf("encrypted = %v, want true", entries[0]["encrypted"])
	}

	// DELETE /api/v1/secrets/mytoken
	delResp := f.doJSON(t, "DELETE", "/api/v1/secrets/mytoken", nil, f.adminToken)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE secret: status = %d, want 200", delResp.StatusCode)
	}

	// List must be empty now
	listResp2 := f.doJSON(t, "GET", "/api/v1/secrets", nil, f.adminToken)
	var entries2 []map[string]any
	readBodyJSON(t, listResp2, &entries2)
	if len(entries2) != 0 {
		t.Errorf("after delete: expected 0 secrets, got %d", len(entries2))
	}
}

func TestAPI_PutSecret_NilEncryptor_500(t *testing.T) {
	flexSessStore := &flexMemSessionStore{rows: map[string]flexSession{}}
	adminUserID := uuid.New()
	flexSessStore.setRole(adminUserID, domain.RoleAdmin)
	sessions := auth.NewSessions(flexSessStore)
	token, err := sessions.Start(context.Background(), adminUserID)
	if err != nil {
		t.Fatal(err)
	}

	a := New(Deps{
		Agents:      newFakeAgentStore(),
		Duties:      newFakeDutyStore(),
		Assignments: newFakeAssignmentStore(),
		Secrets:     newFakeSecretStore(),
		Sessions:    sessions,
		Config:      &config.Config{},
		// Encryptor: nil (not set) — 500 expected
	})

	mux := http.NewServeMux()
	a.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"value": "test"})
	req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/secrets/mykey", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("nil encryptor: status = %d, want 500", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body["error"], "master key not configured") {
		t.Errorf("error = %q, want to contain 'master key not configured'", body["error"])
	}
}

func TestAPI_SSE_RunStartedAndFinished(t *testing.T) {
	f := newAPIFixture(t)

	// Use a separate httptest server that does NOT close the response body
	// immediately. We need to manage this connection manually.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/api/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: f.adminToken})

	// Use a transport with no timeout on the response body so we can read SSE
	transport := &http.Transport{}
	client := &http.Client{Transport: transport}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// Do NOT use t.Cleanup here — we need to manage lifetime explicitly
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE connect: status = %d, want 200", resp.StatusCode)
	}

	runID := uuid.New()

	// readFrame reads lines from the SSE stream until a "data: " line
	frames := make(chan string, 4)
	readErr := make(chan error, 1)

	reader := bufio.NewReader(resp.Body)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				readErr <- err
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data: ") {
				frames <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()

	// Small delay to ensure SSE goroutine is subscribed before publishing
	time.Sleep(50 * time.Millisecond)

	// Fire run_started
	f.api.RunUpdateSink()(&domain.Run{
		ID:     runID,
		Status: domain.RunStatusRunning,
	})

	// Read frame 1
	var frame1 string
	select {
	case frame1 = <-frames:
	case err := <-readErr:
		t.Fatalf("SSE read error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for SSE frame 1")
	}

	if !strings.Contains(frame1, "run_started") {
		t.Errorf("SSE frame 1 = %q, want to contain run_started", frame1)
	}
	if !strings.Contains(frame1, runID.String()) {
		t.Errorf("SSE frame 1 = %q, want to contain run id %s", frame1, runID)
	}

	// Fire run_finished
	f.api.RunUpdateSink()(&domain.Run{
		ID:     runID,
		Status: domain.RunStatusSucceeded,
	})

	// Read frame 2
	var frame2 string
	select {
	case frame2 = <-frames:
	case err := <-readErr:
		t.Fatalf("SSE read error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for SSE frame 2")
	}

	if !strings.Contains(frame2, "run_finished") {
		t.Errorf("SSE frame 2 = %q, want to contain run_finished", frame2)
	}
	if !strings.Contains(frame2, runID.String()) {
		t.Errorf("SSE frame 2 = %q, want to contain run id %s", frame2, runID)
	}

	// Cancel context to clean up SSE connection
	cancel()
}
