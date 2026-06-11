package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/google/uuid"
)

func (a *API) handleListRuns(w http.ResponseWriter, r *http.Request) {
	var agentID uuid.UUID
	if v := r.URL.Query().Get("agent_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid agent_id")
			return
		}
		agentID = id
	}
	runs, err := a.runs.ListFiltered(r.Context(), r.URL.Query().Get("status"), agentID, parseLimit(r))
	if err != nil {
		a.logf("api: list runs: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (a *API) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	run, err := a.runs.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (a *API) handleListEvents(w http.ResponseWriter, r *http.Request) {
	events, err := a.events.ListRecent(r.Context(), r.URL.Query().Get("status"), parseLimit(r))
	if err != nil {
		a.logf("api: list events: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (a *API) handleReplayEvent(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := a.events.GetByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err := a.events.MarkPending(r.Context(), id); err != nil {
		a.logf("api: replay event: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if a.notify != nil {
		a.notify(id) // in-process dispatcher nudge: immediate redispatch
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "requeued"})
}

func (a *API) handleListBackends(w http.ResponseWriter, r *http.Request) {
	type backendView struct {
		Name          string `json:"name"`
		Kind          string `json:"kind"`
		AuthMode      string `json:"auth_mode"`
		Model         string `json:"model,omitempty"`
		DefaultEffort string `json:"default_effort,omitempty"`
	}
	out := make([]backendView, 0, len(a.cfg.Backends))
	for i := range a.cfg.Backends {
		b := &a.cfg.Backends[i]
		out = append(out, backendView{
			Name: b.Name, Kind: b.Kind, AuthMode: b.Auth.Mode,
			Model: b.Model, DefaultEffort: b.DefaultEffort,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleAgentStats(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	stats, err := a.runs.AgentStats(r.Context(), id)
	if err != nil {
		a.logf("api: agent stats: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (a *API) handleRunNow(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Params map[string]any `json:"params"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // empty body = no params
	}
	if body.Params == nil {
		body.Params = map[string]any{}
	}
	run, err := a.invoker.Invoke(r.Context(), id, "manual", nil, body.Params)
	if err != nil {
		a.logf("api: run-now %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "run failed to start: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (a *API) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	raw, err := a.secretsRepo.List(r.Context())
	if err != nil {
		a.logf("api: list secrets: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type entry struct {
		Name      string `json:"name"`
		Encrypted bool   `json:"encrypted"`
	}
	out := make([]entry, 0, len(raw))
	for name, v := range raw {
		out = append(out, entry{Name: name, Encrypted: a.isEncrypted(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handlePutSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "secret name required")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}
	if a.encryptor == nil {
		writeError(w, http.StatusInternalServerError, "master key not configured")
		return
	}
	enc, err := a.encryptor.Encrypt([]byte(body.Value))
	if err != nil {
		a.logf("api: encrypt secret: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := a.secretsRepo.Upsert(r.Context(), name, enc); err != nil {
		a.logf("api: store secret: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "encrypted": true})
}

func (a *API) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.secretsRepo.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
