package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// memSessionStore is an in-memory auth.SessionStore whose role is fixed per
// store instance (tests build one per role).
type memSessionStore struct {
	mu   sync.Mutex
	role string
	rows map[string]uuid.UUID
}

func newMemSessionStore(role string) *memSessionStore {
	return &memSessionStore{role: role, rows: map[string]uuid.UUID{}}
}

func (m *memSessionStore) Create(_ context.Context, tokenHash string, userID uuid.UUID, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[tokenHash] = userID
	return nil
}

func (m *memSessionStore) Lookup(_ context.Context, tokenHash string) (uuid.UUID, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.rows[tokenHash]
	if !ok {
		return uuid.Nil, "", fmt.Errorf("session not found")
	}
	return id, m.role, nil
}

func (m *memSessionStore) Delete(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, tokenHash)
	return nil
}

func (m *memSessionStore) DeleteExpired(_ context.Context) error { return nil }

// authedAPI returns a minimal API plus a logged-in client cookie for the given role.
func authedAPI(t *testing.T, role string) (*API, string) {
	t.Helper()
	sessions := auth.NewSessions(newMemSessionStore(role))
	a := New(Deps{Sessions: sessions})
	token, err := sessions.Start(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return a, token
}

func doReq(t *testing.T, handler http.Handler, method, path, token string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(method, srv.URL+path, nil)
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

func mountedMux(a *API) *http.ServeMux {
	mux := http.NewServeMux()
	a.Mount(mux)
	return mux
}

func TestMiddleware_NoCookie401(t *testing.T) {
	a, _ := authedAPI(t, domain.RoleAdmin)
	resp := doReq(t, mountedMux(a), http.MethodGet, "/api/v1/me", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_BadToken401(t *testing.T) {
	a, _ := authedAPI(t, domain.RoleAdmin)
	resp := doReq(t, mountedMux(a), http.MethodGet, "/api/v1/me", "not-a-real-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_ViewerGetAllowed(t *testing.T) {
	a, token := authedAPI(t, domain.RoleViewer)
	resp := doReq(t, mountedMux(a), http.MethodGet, "/api/v1/me", token)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMiddleware_ViewerMutation403(t *testing.T) {
	a, token := authedAPI(t, domain.RoleViewer)
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		resp := doReq(t, mountedMux(a), method, "/api/v1/agents", token)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", method, resp.StatusCode)
		}
	}
}

func TestMiddleware_AdminMutationPasses(t *testing.T) {
	// Admin POST must clear the middleware (handler itself may 4xx/5xx on the
	// nil stores — assert specifically NOT 401/403).
	a, token := authedAPI(t, domain.RoleAdmin)
	resp := doReq(t, mountedMux(a), http.MethodPost, "/api/v1/logout", token)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Errorf("admin POST blocked by middleware: %d", resp.StatusCode)
	}
}
