package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// usersTestAPI builds an API whose caller ("caller") is logged in with role.
func usersTestAPI(t *testing.T, role string) (*API, *fakeUserStore, string) {
	t.Helper()
	sessions := auth.NewSessions(newMemSessionStore(role))
	users := newFakeUserStore()
	me := &domain.User{ID: uuid.New(), Username: "caller", Role: role, PasswordHash: "x"}
	users.add(me)
	a := New(Deps{Sessions: sessions, Users: users})
	token, err := sessions.Start(context.Background(), me.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a, users, token
}

func usersReq(t *testing.T, a *API, method, path, token string, body any) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	a.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
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

func TestListUsersOmitsPasswordHash(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	users.add(&domain.User{Username: "bob", Role: domain.RoleViewer, PasswordHash: "super-secret-hash"})

	resp := usersReq(t, a, http.MethodGet, "/api/v1/users", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if strings.Contains(body, "password_hash") || strings.Contains(body, "super-secret-hash") {
		t.Errorf("response leaks password hash: %s", body)
	}
	if !strings.Contains(body, `"bob"`) || !strings.Contains(body, `"caller"`) {
		t.Errorf("response missing expected usernames: %s", body)
	}
}

func TestCreateUser(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	resp := usersReq(t, a, http.MethodPost, "/api/v1/users", token,
		map[string]string{"username": "newbie", "password": "hunter22", "role": "viewer"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	u, err := users.GetByUsername(context.Background(), "newbie")
	if err != nil || u == nil {
		t.Fatalf("user not stored: %v", err)
	}
	if u.Role != domain.RoleViewer {
		t.Errorf("role = %q, want viewer", u.Role)
	}
	if !auth.VerifyPassword(u.PasswordHash, "hunter22") {
		t.Error("stored hash does not verify against the password")
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "password") {
		t.Errorf("create response leaks password material: %s", raw)
	}
}

func TestCreateUserValidation(t *testing.T) {
	cases := []struct {
		name string
		body map[string]string
	}{
		{"empty username", map[string]string{"username": "  ", "password": "pw", "role": "viewer"}},
		{"empty password", map[string]string{"username": "x", "password": "", "role": "viewer"}},
		{"bad role", map[string]string{"username": "x", "password": "pw", "role": "root"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, token := usersTestAPI(t, domain.RoleAdmin)
			resp := usersReq(t, a, http.MethodPost, "/api/v1/users", token, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestCreateUserDuplicate(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	users.add(&domain.User{Username: "bob", Role: domain.RoleViewer, PasswordHash: "x"})
	resp := usersReq(t, a, http.MethodPost, "/api/v1/users", token,
		map[string]string{"username": "bob", "password": "pw", "role": "viewer"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestDeleteUser(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	users.add(&domain.User{Username: "bob", Role: domain.RoleViewer, PasswordHash: "x"})
	resp := usersReq(t, a, http.MethodDelete, "/api/v1/users/bob", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if u, _ := users.GetByUsername(context.Background(), "bob"); u != nil {
		t.Error("user still present after delete")
	}
}

func TestDeleteUserUnknown(t *testing.T) {
	a, _, token := usersTestAPI(t, domain.RoleAdmin)
	resp := usersReq(t, a, http.MethodDelete, "/api/v1/users/ghost", token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDeleteUserSelf(t *testing.T) {
	a, users, token := usersTestAPI(t, domain.RoleAdmin)
	resp := usersReq(t, a, http.MethodDelete, "/api/v1/users/caller", token, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if u, _ := users.GetByUsername(context.Background(), "caller"); u == nil {
		t.Error("self-delete went through")
	}
}

func TestUsersViewerRoleMatrix(t *testing.T) {
	a, _, token := usersTestAPI(t, domain.RoleViewer)
	if resp := usersReq(t, a, http.MethodGet, "/api/v1/users", token, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("viewer GET = %d, want 200", resp.StatusCode)
	}
	if resp := usersReq(t, a, http.MethodPost, "/api/v1/users", token,
		map[string]string{"username": "x", "password": "pw", "role": "viewer"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer POST = %d, want 403", resp.StatusCode)
	}
	if resp := usersReq(t, a, http.MethodDelete, "/api/v1/users/caller", token, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer DELETE = %d, want 403", resp.StatusCode)
	}
}
