package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

type assignmentStateBody struct {
	Value any `json:"value"`
}

type assignmentMemoryBody struct {
	Note any `json:"note"`
}

func decodeStateValue(raw []byte) any {
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		return v
	}
	return string(raw)
}

func decodeNote(raw json.RawMessage) any {
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		return v
	}
	return string(raw)
}

func (a *API) requireAssignmentStateStore(w http.ResponseWriter) bool {
	if a.state == nil {
		writeError(w, http.StatusNotImplemented, "assignment state store not configured")
		return false
	}
	return true
}

func (a *API) assignmentExists(r *http.Request) (uuid.UUID, bool) {
	id, err := parseIDParam(r)
	if err != nil {
		return uuid.Nil, false
	}
	if _, err := a.assignments.GetByID(r.Context(), id); err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func (a *API) handleListAssignmentState(w http.ResponseWriter, r *http.Request) {
	if !a.requireAssignmentStateStore(w) {
		return
	}
	id, ok := a.assignmentExists(r)
	if !ok {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}
	state, err := a.state.List(r.Context(), id.String())
	if err != nil {
		a.logf("api: list assignment state: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make(map[string]any, len(state))
	for key, raw := range state {
		out[key] = decodeStateValue(raw)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handlePutAssignmentState(w http.ResponseWriter, r *http.Request) {
	if !a.requireAssignmentStateStore(w) {
		return
	}
	id, ok := a.assignmentExists(r)
	if !ok {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "state key is required")
		return
	}
	var body assignmentStateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	raw, err := json.Marshal(body.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "value must be JSON serializable")
		return
	}
	if err := a.state.Set(r.Context(), id.String(), key, raw); err != nil {
		a.logf("api: set assignment state: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (a *API) handleDeleteAssignmentState(w http.ResponseWriter, r *http.Request) {
	if !a.requireAssignmentStateStore(w) {
		return
	}
	id, ok := a.assignmentExists(r)
	if !ok {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "state key is required")
		return
	}
	if err := a.state.Delete(r.Context(), id.String(), key); err != nil {
		a.logf("api: delete assignment state: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *API) handleListAssignmentMemory(w http.ResponseWriter, r *http.Request) {
	if !a.requireAssignmentStateStore(w) {
		return
	}
	id, ok := a.assignmentExists(r)
	if !ok {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}
	notes, err := a.state.ListNotes(r.Context(), id.String())
	if err != nil {
		a.logf("api: list assignment memory: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]any, len(notes))
	for i, raw := range notes {
		out[i] = decodeNote(raw)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleAppendAssignmentMemory(w http.ResponseWriter, r *http.Request) {
	if !a.requireAssignmentStateStore(w) {
		return
	}
	id, ok := a.assignmentExists(r)
	if !ok {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}
	var body assignmentMemoryBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Note == nil {
		writeError(w, http.StatusBadRequest, "note is required")
		return
	}
	if err := a.state.AppendNote(r.Context(), id.String(), body.Note); err != nil {
		a.logf("api: append assignment memory: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "saved"})
}
