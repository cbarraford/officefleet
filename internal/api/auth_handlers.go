package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/google/uuid"
)

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, err := a.users.GetByUsername(r.Context(), body.Username)
	if err != nil {
		a.logf("api: login lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Uniform 401 for unknown user and wrong password (no username oracle).
	// VerifyPassword on a dummy hash keeps timing roughly uniform.
	if user == nil {
		_ = auth.VerifyPassword("pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA==$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", body.Password)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, body.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, err := a.sessions.Start(r.Context(), user.ID)
	if err != nil {
		a.logf("api: start session: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	http.SetCookie(w, a.sessionCookie(token, auth.SessionTTL))
	writeJSON(w, http.StatusOK, map[string]string{"username": user.Username, "role": user.Role})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.CookieName); err == nil {
		_ = a.sessions.End(r.Context(), cookie.Value)
	}
	expired := a.sessionCookie("", -time.Hour)
	http.SetCookie(w, expired)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	role, _ := r.Context().Value(ctxKeyRole).(string)
	userID, _ := r.Context().Value(ctxKeyUserID).(uuid.UUID)
	user, err := a.users.GetByID(r.Context(), userID)
	if err != nil {
		a.logf("api: me lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		// The session outlived the account (user deleted): treat as unauthenticated.
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": user.Username, "role": role})
}
