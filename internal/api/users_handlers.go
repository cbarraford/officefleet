package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.users.List(r.Context())
	if err != nil {
		a.logf("api: list users: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if users == nil {
		users = []*domain.User{}
	}
	writeJSON(w, http.StatusOK, users) // PasswordHash is json:"-"
}

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Username) == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if body.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}
	if body.Role != domain.RoleAdmin && body.Role != domain.RoleViewer {
		writeError(w, http.StatusBadRequest, "role must be admin or viewer")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		a.logf("api: hash password: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	user := &domain.User{Username: body.Username, PasswordHash: hash, Role: body.Role}
	if err := a.users.Create(r.Context(), user); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a user with that username already exists")
			return
		}
		a.logf("api: create user: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	target, err := a.users.GetByUsername(r.Context(), username)
	if err != nil {
		a.logf("api: delete user lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	callerID, _ := r.Context().Value(ctxKeyUserID).(uuid.UUID)
	if target.ID == callerID {
		writeError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	if err := a.users.Delete(r.Context(), username); err != nil {
		a.logf("api: delete user: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
