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
		if !domain.Job(*b.Role).Valid() {
			return errValidation("invalid job " + *b.Role)
		}
		agent.Role = domain.Job(*b.Role)
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
	if agent.Role == "" {
		writeError(w, http.StatusBadRequest, "role is required")
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

// --- Skills ---

var validTriggerKinds = []string{"manual", "cron", "event-subscription", "continuous"}

type skillBody struct {
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

func (a *API) applySkillBody(b *skillBody, skill *domain.Skill) error {
	if b.Name != nil {
		if strings.TrimSpace(*b.Name) == "" {
			return errValidation("name must not be empty")
		}
		skill.Name = *b.Name
	}
	if b.Role != nil {
		if !domain.Job(*b.Role).Valid() {
			return errValidation("invalid job " + *b.Role)
		}
		skill.Role = domain.Job(*b.Role)
	}
	if b.Description != nil {
		skill.Description = *b.Description
	}
	if b.TriggerKinds != nil {
		for _, k := range b.TriggerKinds {
			if !slices.Contains(validTriggerKinds, k) {
				return errValidation("invalid trigger_kind " + k + "; must be one of manual, cron, event-subscription, continuous")
			}
		}
		skill.TriggerKinds = b.TriggerKinds
	}
	if b.Prompt != nil {
		skill.Prompt = *b.Prompt
	}
	if b.RequiredTools != nil {
		skill.RequiredTools = b.RequiredTools
	}
	if b.OutputActions != nil {
		skill.OutputActions = b.OutputActions
	}
	if len(b.ConfigSchema) > 0 {
		if string(b.ConfigSchema) == "null" {
			skill.ConfigSchema = nil
		} else {
			var m map[string]any
			if err := json.Unmarshal(b.ConfigSchema, &m); err != nil {
				return errValidation("config_schema must be a JSON object")
			}
			skill.ConfigSchema = m
		}
	}
	if b.Backend != nil {
		if !a.backendNameExists(b.Backend) {
			return errValidation("unknown backend " + b.Backend.Name)
		}
		skill.Backend = b.Backend
	}
	return nil
}

func (a *API) handleListSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := a.skills.List(r.Context())
	if err != nil {
		a.logf("api: list skills: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

func (a *API) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	var body skillBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	skill := &domain.Skill{}
	if err := a.applySkillBody(&body, skill); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if skill.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if skill.Role == "" {
		writeError(w, http.StatusBadRequest, "role is required")
		return
	}
	if err := a.skills.Insert(r.Context(), skill); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill with that name already exists")
			return
		}
		a.logf("api: create skill: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, skill)
}

func (a *API) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	skill, err := a.skills.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	writeJSON(w, http.StatusOK, skill)
}

func (a *API) handlePatchSkill(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	skill, err := a.skills.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	var body skillBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := a.applySkillBody(&body, skill); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.skills.Update(r.Context(), skill); err != nil {
		a.logf("api: update skill: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, skill)
}

func (a *API) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.skills.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Assignments ---

type assignmentBody struct {
	AgentID            *uuid.UUID             `json:"agent_id"`
	SkillID             *uuid.UUID             `json:"skill_id"`
	Name               *string                `json:"name"`
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
		skill, err := a.skills.GetByID(ctx, asg.SkillID)
		if err == nil && len(skill.TriggerKinds) > 0 && !slices.Contains(skill.TriggerKinds, asg.Trigger.Kind) {
			return errValidation("skill does not support trigger kind " + asg.Trigger.Kind)
		}
	}
	if !a.backendNameExists(asg.Backend) {
		return errValidation("unknown backend " + asg.Backend.Name)
	}
	for _, out := range asg.Outputs {
		if err := out.ValidateForEach(); err != nil {
			return errValidation(err.Error())
		}
	}
	agentRow, aerr := a.agents.GetByID(ctx, asg.AgentID)
	skillRow, serr := a.skills.GetByID(ctx, asg.SkillID)
	if aerr == nil && serr == nil && agentRow.Role != skillRow.Role {
		return errValidation("skill job " + string(skillRow.Role) + " does not match agent job " + string(agentRow.Role))
	}
	return nil
}

func applyAssignmentBody(b *assignmentBody, asg *domain.Assignment, isCreate bool) {
	if isCreate {
		if b.AgentID != nil {
			asg.AgentID = *b.AgentID
		}
		if b.SkillID != nil {
			asg.SkillID = *b.SkillID
		}
	}
	if b.Name != nil {
		asg.Name = *b.Name
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
	if asg.SkillID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "skill_id is required")
		return
	}
	if err := a.validateAssignment(r.Context(), asg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.skills.GetByID(r.Context(), asg.SkillID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown skill_id")
		return
	}
	if _, err := a.agents.GetByID(r.Context(), asg.AgentID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown agent_id")
		return
	}
	if err := a.assignments.Insert(r.Context(), asg); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an assignment for that agent and skill already exists")
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

// handleGetAssignmentState returns the per-assignment KV state (dedup keys,
// cursors, last_reviewed_sha, small memory blobs) for operator inspection (#25).
func (a *API) handleGetAssignmentState(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if a.state == nil {
		writeError(w, http.StatusServiceUnavailable, "state store not configured")
		return
	}
	raw, err := a.state.List(r.Context(), id.String())
	if err != nil {
		a.logf("api: get assignment state: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = string(v)
	}
	writeJSON(w, http.StatusOK, out)
}
