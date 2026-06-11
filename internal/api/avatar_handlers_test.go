package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// avatarTestAPI builds an API with agents + a fake avatar service.
func avatarTestAPI(t *testing.T, role string) (*API, *fakeAgentStore, *fakeAvatarService, string) {
	t.Helper()
	sessions := auth.NewSessions(newMemSessionStore(role))
	agents := newFakeAgentStore()
	avatars := newFakeAvatarService()
	a := New(Deps{Sessions: sessions, Agents: agents, Avatars: avatars})
	token, err := sessions.Start(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return a, agents, avatars, token
}

func avatarReq(t *testing.T, a *API, method, path, token, contentType string, body []byte) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	a.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func seedAgentForAvatar(t *testing.T, agents *fakeAgentStore) *domain.Agent {
	t.Helper()
	agent := &domain.Agent{ID: uuid.New(), Name: "Ada", Role: "Reviewer", Enabled: true}
	if err := agents.Insert(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	return agent
}

var testPNG = append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("fake-image-data")...)

func TestRegenerateAvatar(t *testing.T) {
	a, agents, avatars, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents/"+agent.ID.String()+"/avatar/regenerate", token, "", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if got := avatars.assignedIDs(); len(got) != 1 || got[0] != agent.ID {
		t.Errorf("Assign calls = %v, want [%s]", got, agent.ID)
	}
}

func TestRegenerateAvatarUnknownAgent(t *testing.T) {
	a, _, _, token := avatarTestAPI(t, domain.RoleAdmin)
	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents/"+uuid.NewString()+"/avatar/regenerate", token, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRegenerateAvatarViewerForbidden(t *testing.T) {
	a, agents, _, token := avatarTestAPI(t, domain.RoleViewer)
	agent := seedAgentForAvatar(t, agents)
	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents/"+agent.ID.String()+"/avatar/regenerate", token, "", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestUploadAvatar(t *testing.T) {
	a, agents, avatars, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+agent.ID.String()+"/avatar", token, "image/png", testPNG)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := avatars.uploadFor(agent.ID); !bytes.Equal(got, testPNG) {
		t.Errorf("uploaded bytes mismatch (got %d bytes)", len(got))
	}
}

func TestUploadAvatarRejectsNonPNG(t *testing.T) {
	a, agents, _, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+agent.ID.String()+"/avatar", token, "image/png", []byte("GIF89a-not-a-png"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadAvatarRejectsWrongContentType(t *testing.T) {
	a, agents, _, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+agent.ID.String()+"/avatar", token, "image/jpeg", testPNG)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadAvatarRejectsOversize(t *testing.T) {
	a, agents, _, token := avatarTestAPI(t, domain.RoleAdmin)
	agent := seedAgentForAvatar(t, agents)

	big := make([]byte, (1<<20)+1)
	copy(big, testPNG)
	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+agent.ID.String()+"/avatar", token, "image/png", big)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadAvatarUnknownAgent(t *testing.T) {
	a, _, _, token := avatarTestAPI(t, domain.RoleAdmin)
	resp := avatarReq(t, a, http.MethodPut, "/api/v1/agents/"+uuid.NewString()+"/avatar", token, "image/png", testPNG)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestCreateAgentTriggersAvatarAssign(t *testing.T) {
	a, _, avatars, token := avatarTestAPI(t, domain.RoleAdmin)
	body := []byte(`{"name": "Newbie", "role": "Tester"}`)
	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents", token, "application/json", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if got := avatars.assignedIDs(); len(got) != 1 {
		t.Errorf("Assign calls = %d, want 1 (create must fire generation)", len(got))
	}
}

func TestCreateAgentNilAvatarServiceIsSafe(t *testing.T) {
	// Existing entity tests construct the API without Avatars — creation
	// must not panic when the service is absent.
	sessions := auth.NewSessions(newMemSessionStore(domain.RoleAdmin))
	agents := newFakeAgentStore()
	a := New(Deps{Sessions: sessions, Agents: agents})
	token, err := sessions.Start(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	resp := avatarReq(t, a, http.MethodPost, "/api/v1/agents", token, "application/json", []byte(`{"name":"X"}`))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
}
