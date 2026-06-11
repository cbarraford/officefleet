package api

import (
	"context"
	"net/http"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
)

type ctxKey int

const (
	ctxKeyRole ctxKey = iota
	ctxKeyUserID
)

// route serves authenticated requests via the inner mux (built once per API
// instance — the once lives on the struct so tests can build several APIs).
func (a *API) route(w http.ResponseWriter, r *http.Request) {
	a.innerOnce.Do(func() { a.inner = a.authedMux() })
	a.inner.ServeHTTP(w, r)
}

// requireAuth authenticates the session cookie and enforces roles:
// viewers may only GET (the SSE stream is a GET).
func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		userID, role, err := a.sessions.Validate(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if role != domain.RoleAdmin && r.Method != http.MethodGet {
			writeError(w, http.StatusForbidden, "viewer role is read-only")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyRole, role)
		ctx = context.WithValue(ctx, ctxKeyUserID, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
