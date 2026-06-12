package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// --- Validation helpers ---

type validationError string

func (e validationError) Error() string { return string(e) }
func errValidation(msg string) error    { return validationError(msg) }

// backendNameExists reports whether a BackendRef names a configured backend.
// A nil cfg or empty name is legal (resolution falls through).
func (a *API) backendNameExists(ref *domain.BackendRef) bool {
	if ref == nil || ref.Name == "" {
		return true
	}
	if a.cfg == nil {
		return false
	}
	for i := range a.cfg.Backends {
		if a.cfg.Backends[i].Name == ref.Name {
			return true
		}
	}
	return false
}

// isUniqueViolation matches Postgres unique-violation errors without
// importing pgconn: SQLSTATE 23505 appears in the error text.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}

// --- Agents ---

func (a *API) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := a.agents.List(r.Context())
	if err != nil {
		a.logf("api: list agents: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

type agentBody struct {
	Name           *string            `json:"name"`
	Role           *string            `json:"role"`
	SystemPrompt   *string            `json:"system_prompt"`
	DefaultBackend *domain.BackendRef `json:"default_backend"`
	Enabled        *bool              `json:"enabled"`
	AvatarURL      *string            `json:"avatar_url"`
	HiredAt        *string            `json:"hired_at"` // YYYY-MM-DD
}

// applyAgentBody applies provided (non-nil) fields onto agent — PATCH
// semantics; create passes a zero-value agent.
func (a *API) applyAgentBody(b *agentBody, agent *domain.Agent) error {
	if b.Name != nil {
		if strings.TrimSpace(*b.Name) == "" {
			return errValidation("name must not be empty")
		}
		agent.Name = *b.Name
	}
	if b.Role != nil {
		agent.Role = *b.Role
	}
	if b.SystemPrompt != nil {
		agent.SystemPrompt = *b.SystemPrompt
	}
	if b.DefaultBackend != nil {
		if !a.backendNameExists(b.DefaultBackend) {
			return errValidation("unknown backend " + b.DefaultBackend.Name)
		}
		agent.DefaultBackend = *b.DefaultBackend
	}
	if b.Enabled != nil {
		agent.Enabled = *b.Enabled
	}
	if b.AvatarURL != nil {
		agent.AvatarURL = b.AvatarURL
	}
	if b.HiredAt != nil {
		t, err := time.Parse("2006-01-02", *b.HiredAt)
		if err != nil {
			return errValidation("hired_at must be YYYY-MM-DD")
		}
		agent.HiredAt = &t
	}
	return nil
}

func (a *API) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var body agentBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	agent := &domain.Agent{Enabled: true}
	if err := a.applyAgentBody(&body, agent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if agent.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := a.agents.Insert(r.Context(), agent); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an agent with that name already exists")
			return
		}
		a.logf("api: create agent: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if a.avatars != nil {
		a.avatars.Assign(agent) // async per §6.1 — creation never blocks on imagery
	}
	writeJSON(w, http.StatusCreated, agent)
}

func (a *API) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	agent, err := a.agents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	var stats any
	if a.runs != nil {
		s, serr := a.runs.AgentStats(r.Context(), id)
		if serr != nil {
			a.logf("api: agent stats: %v", serr)
			// best-effort: agent detail still loads
		} else {
			stats = s
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agent, "stats": stats})
}

func (a *API) handlePatchAgent(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	agent, err := a.agents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	var body agentBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := a.applyAgentBody(&body, agent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.agents.Update(r.Context(), agent); err != nil {
		a.logf("api: update agent: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (a *API) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.agents.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Duties ---

var validTriggerKinds = []string{"manual", "cron", "event-subscription", "continuous"}

type dutyBody struct {
	Name          *string                   `json:"name"`
	Role          *string                   `json:"role"`
	Description   *string                   `json:"description"`
	TriggerKinds  []string                  `json:"trigger_kinds"`
	Prompt        *string                   `json:"prompt"`
	RequiredTools []string                  `json:"required_tools"`
	OutputActions []domain.OutputActionType `json:"output_actions"`
	ConfigSchema  json.RawMessage           `json:"config_schema"`
	Backend       *domain.BackendRef        `json:"backend"`
}

func (a *API) applyDutyBody(b *dutyBody, duty *domain.Duty) error {
	if b.Name != nil {
		if strings.TrimSpace(*b.Name) == "" {
			return errValidation("name must not be empty")
		}
		duty.Name = *b.Name
	}
	if b.Role != nil {
		duty.Role = *b.Role
	}
	if b.Description != nil {
		duty.Description = *b.Description
	}
	if b.TriggerKinds != nil {
		for _, k := range b.TriggerKinds {
			if !slices.Contains(validTriggerKinds, k) {
				return errValidation("invalid trigger_kind " + k + "; must be one of manual, cron, event-subscription, continuous")
			}
		}
		duty.TriggerKinds = b.TriggerKinds
	}
	if b.Prompt != nil {
		duty.Prompt = *b.Prompt
	}
	if b.RequiredTools != nil {
		duty.RequiredTools = b.RequiredTools
	}
	if b.OutputActions != nil {
		duty.OutputActions = b.OutputActions
	}
	if len(b.ConfigSchema) > 0 {
		if string(b.ConfigSchema) == "null" {
			duty.ConfigSchema = nil
		} else {
			var m map[string]any
			if err := json.Unmarshal(b.ConfigSchema, &m); err != nil {
				return errValidation("config_schema must be a JSON object")
			}
			duty.ConfigSchema = m
		}
	}
	if b.Backend != nil {
		if !a.backendNameExists(b.Backend) {
			return errValidation("unknown backend " + b.Backend.Name)
		}
		duty.Backend = b.Backend
	}
	return nil
}

func (a *API) handleListDuties(w http.ResponseWriter, r *http.Request) {
	duties, err := a.duties.List(r.Context())
	if err != nil {
		a.logf("api: list duties: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, duties)
}

func (a *API) handleCreateDuty(w http.ResponseWriter, r *http.Request) {
	var body dutyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	duty := &domain.Duty{}
	if err := a.applyDutyBody(&body, duty); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if duty.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := a.duties.Insert(r.Context(), duty); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a duty with that name already exists")
			return
		}
		a.logf("api: create duty: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, duty)
}

func (a *API) handleGetDuty(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	duty, err := a.duties.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "duty not found")
		return
	}
	writeJSON(w, http.StatusOK, duty)
}

func (a *API) handlePatchDuty(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	duty, err := a.duties.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "duty not found")
		return
	}
	var body dutyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := a.applyDutyBody(&body, duty); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.duties.Update(r.Context(), duty); err != nil {
		a.logf("api: update duty: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, duty)
}

func (a *API) handleDeleteDuty(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.duties.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "duty not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Assignments ---

type assignmentBody struct {
	Name               *string                `json:"name"`
	AgentID            *uuid.UUID             `json:"agent_id"`
	DutyID             *uuid.UUID             `json:"duty_id"`
	Enabled            *bool                  `json:"enabled"`
	Trigger            *domain.TriggerConfig  `json:"trigger"`
	Outputs            []domain.OutputBinding `json:"outputs"`
	Config             map[string]any         `json:"config"`
	Backend            *domain.BackendRef     `json:"backend"`
	TaskPromptOverride *string                `json:"task_prompt_override"`
	ExtraInstructions  *string                `json:"extra_instructions"`
}

// validateAssignment mirrors config.Validate's event-subscription rules so
// the API cannot create what the dispatcher would reject.
func (a *API) validateAssignment(ctx context.Context, asg *domain.Assignment) error {
	if asg.Trigger.Kind == "event-subscription" {
		src, _ := asg.Trigger.Filter["source"].(string)
		typ, _ := asg.Trigger.Filter["event_type"].(string)
		if src == "" || typ == "" {
			return errValidation("event-subscription trigger requires non-empty filter.source and filter.event_type")
		}
	}
	if asg.Trigger.Kind != "" {
		duty, err := a.duties.GetByID(ctx, asg.DutyID)
		if err == nil && len(duty.TriggerKinds) > 0 && !slices.Contains(duty.TriggerKinds, asg.Trigger.Kind) {
			return errValidation("duty does not support trigger kind " + asg.Trigger.Kind)
		}
	}
	if !a.backendNameExists(asg.Backend) {
		return errValidation("unknown backend " + asg.Backend.Name)
	}
	return nil
}

func applyAssignmentBody(b *assignmentBody, asg *domain.Assignment, isCreate bool) {
	if b.Name != nil {
		asg.Name = *b.Name
	}
	if isCreate {
		if b.AgentID != nil {
			asg.AgentID = *b.AgentID
		}
		if b.DutyID != nil {
			asg.DutyID = *b.DutyID
		}
	}
	if b.Enabled != nil {
		asg.Enabled = *b.Enabled
	}
	if b.Trigger != nil {
		asg.Trigger = *b.Trigger
	}
	if b.Outputs != nil {
		asg.Outputs = b.Outputs
	}
	if b.Config != nil {
		asg.Config = b.Config
	}
	if b.Backend != nil {
		asg.Backend = b.Backend
	}
	if b.TaskPromptOverride != nil {
		asg.TaskPromptOverride = b.TaskPromptOverride
	}
	if b.ExtraInstructions != nil {
		asg.ExtraInstructions = b.ExtraInstructions
	}
}

func (a *API) handleListAssignments(w http.ResponseWriter, r *http.Request) {
	assignments, err := a.assignments.List(r.Context())
	if err != nil {
		a.logf("api: list assignments: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, assignments)
}

func (a *API) handleCreateAssignment(w http.ResponseWriter, r *http.Request) {
	var body assignmentBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	asg := &domain.Assignment{Enabled: true}
	applyAssignmentBody(&body, asg, true)
	if asg.AgentID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if asg.DutyID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "duty_id is required")
		return
	}
	if err := a.validateAssignment(r.Context(), asg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.duties.GetByID(r.Context(), asg.DutyID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown duty_id")
		return
	}
	if _, err := a.agents.GetByID(r.Context(), asg.AgentID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown agent_id")
		return
	}
	if err := a.assignments.Insert(r.Context(), asg); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an assignment for that agent, duty, and name already exists")
			return
		}
		a.logf("api: create assignment: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, asg)
}

func (a *API) handleGetAssignment(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	asg, err := a.assignments.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}
	writeJSON(w, http.StatusOK, asg)
}

func (a *API) handlePatchAssignment(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	asg, err := a.assignments.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}
	var body assignmentBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	applyAssignmentBody(&body, asg, false)
	if err := a.validateAssignment(r.Context(), asg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.assignments.Update(r.Context(), asg); err != nil {
		a.logf("api: update assignment: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, asg)
}

func (a *API) handleDeleteAssignment(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.assignments.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
