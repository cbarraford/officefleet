package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/outputs"
	"github.com/cbarraford/office-fleet/internal/prompt"
	"github.com/cbarraford/office-fleet/internal/repo"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/google/uuid"
)

// runRepo is the interface Pipeline uses for run persistence.
// *repo.RunRepo satisfies this interface.
type runRepo interface {
	Insert(ctx context.Context, run *domain.Run) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.RunStatus, errMsg *string) error
	UpdateResult(ctx context.Context, id uuid.UUID, result *domain.LLMResult, outputs []domain.OutputDelivery, status domain.RunStatus) error
}

// SecretsProvider loads all named secrets into a map for prompt rendering.
type SecretsProvider interface {
	Load(ctx context.Context) (map[string]string, error)
}

// Pipeline executes Assignments end-to-end.
type Pipeline struct {
	cfg     *config.Config
	runRepo runRepo
	store   state.Store
	secrets SecretsProvider

	// onRunUpdate, when set, fires after a run is first recorded and after
	// each terminal record (succeeded/failed/skipped). The same *domain.Run
	// pointer is reused and mutated between calls, so the callback MUST
	// read/marshal it synchronously and MUST NOT block or retain the pointer.
	// Used by the API's SSE feed; nil-safe.
	onRunUpdate func(*domain.Run)
}

// SetRunUpdateHook registers fn to receive run lifecycle updates.
func (p *Pipeline) SetRunUpdateHook(fn func(*domain.Run)) { p.onRunUpdate = fn }

func (p *Pipeline) emitRunUpdate(run *domain.Run) {
	if p.onRunUpdate != nil {
		p.onRunUpdate(run)
	}
}

func NewPipeline(cfg *config.Config, rr *repo.RunRepo, store state.Store, sp SecretsProvider) *Pipeline {
	return &Pipeline{cfg: cfg, runRepo: rr, store: store, secrets: sp}
}

// ExecuteRequest is the input for one run invocation.
type ExecuteRequest struct {
	Assignment  *domain.Assignment
	Agent       *domain.Agent
	Duty        *domain.Duty
	TriggerKind string
	EventID     *string        // id of the triggering event, if any (event-subscription)
	EventParams map[string]any // operator params for manual; event payload for event-subscription
	Executor    executor.Executor
}

// Skip reasons recorded on a Run when the pause gate prevents execution.
const (
	SkipReasonAgentPaused      = "agent_paused"
	SkipReasonAssignmentPaused = "assignment_paused"
)

// Execute runs the full pipeline for one assignment and records the result.
func (p *Pipeline) Execute(ctx context.Context, req ExecuteRequest) (*domain.Run, error) {
	// Pause gate: a disabled agent or assignment must not start new work,
	// regardless of trigger kind. The skip is still recorded so it is auditable.
	if !req.Agent.Enabled || !req.Assignment.Enabled {
		reason := SkipReasonAgentPaused
		if req.Agent.Enabled {
			reason = SkipReasonAssignmentPaused
		}
		run := &domain.Run{
			ID:           uuid.New(),
			AssignmentID: req.Assignment.ID,
			AgentID:      req.Agent.ID,
			DutyID:       req.Duty.ID,
			TriggerKind:  req.TriggerKind,
			EventID:      req.EventID,
			Status:       domain.RunStatusSkipped,
			StartedAt:    time.Now(),
			Error:        &reason,
		}
		if err := p.runRepo.Insert(ctx, run); err != nil {
			return nil, fmt.Errorf("record skipped run: %w", err)
		}
		// Insert does not persist the error column; UpdateStatus records the
		// skip reason and finished_at.
		_ = p.runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusSkipped, &reason)
		p.emitRunUpdate(run)
		return run, nil
	}

	// Load secrets for prompt rendering. Guard nil so tests without a provider still work.
	secretsMap := map[string]string{}
	if p.secrets != nil {
		m, err := p.secrets.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("load secrets: %w", err)
		}
		secretsMap = m
	}

	// Build prompt context.
	promptCtx := prompt.Context{
		Event:      req.EventParams,
		Agent:      map[string]any{"name": req.Agent.Name, "role": req.Agent.Role, "system_prompt": req.Agent.SystemPrompt},
		Duty:       map[string]any{"name": req.Duty.Name, "role": req.Duty.Role, "description": req.Duty.Description},
		Assignment: map[string]any(req.Assignment.Config),
		State:      map[string]any{},
		Now:        time.Now(),
		Secrets:    secretsMap,
	}
	if promptCtx.Event == nil {
		promptCtx.Event = map[string]any{}
	}

	// Load all stored state keys for this assignment into promptCtx.State so
	// templates like {{.State.last_reviewed_sha}} resolve to their actual values.
	stateEntries, err := p.store.List(ctx, req.Assignment.ID.String())
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	for k, v := range stateEntries {
		// Try JSON first; fall back to treating the bytes as a raw string.
		var val any
		if jsonErr := json.Unmarshal(v, &val); jsonErr != nil {
			val = string(v)
		}
		promptCtx.State[k] = val
	}

	// Select task prompt: override or duty default.
	taskTemplate := req.Duty.Prompt
	if req.Assignment.TaskPromptOverride != nil && *req.Assignment.TaskPromptOverride != "" {
		taskTemplate = *req.Assignment.TaskPromptOverride
	}
	extraInstructions := ""
	if req.Assignment.ExtraInstructions != nil {
		extraInstructions = *req.Assignment.ExtraInstructions
	}

	// Render prompts.
	systemPrompt, taskPrompt, err := prompt.ComposePrompts(
		req.Agent.SystemPrompt, taskTemplate, extraInstructions, promptCtx)
	if err != nil {
		return nil, fmt.Errorf("compose prompts: %w", err)
	}

	// Resolve backend model/effort from config.
	backend, _, err := config.ResolveBackend(p.cfg, findAssignmentConfig(p.cfg, req.Assignment, req.Agent.Name, req.Duty.Name))
	if err != nil {
		return nil, fmt.Errorf("resolve backend: %w", err)
	}

	// Create workspace.
	workspace, err := os.MkdirTemp("", "fleet-run-*")
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	// Record run start.
	run := &domain.Run{
		ID:                   uuid.New(),
		AssignmentID:         req.Assignment.ID,
		AgentID:              req.Agent.ID,
		DutyID:               req.Duty.ID,
		TriggerKind:          req.TriggerKind,
		EventID:              req.EventID,
		RenderedSystemPrompt: systemPrompt,
		RenderedPrompt:       taskPrompt,
		Status:               domain.RunStatusRunning,
		StartedAt:            time.Now(),
	}
	if err := p.runRepo.Insert(ctx, run); err != nil {
		return nil, fmt.Errorf("record run start: %w", err)
	}
	p.emitRunUpdate(run)

	// Dedup: skip if this event has already been processed for this assignment.
	// NOTE: Insert is intentionally called before this check so that every
	// invocation is recorded in the database for audit purposes. Duplicate
	// events are stored with RunStatusSkipped rather than being silently dropped.
	dedupKey := deriveDedupKey(req.EventParams)
	if dedupKey != "" {
		already, err := p.store.HasProcessed(ctx, req.Assignment.ID.String(), dedupKey)
		if err != nil {
			return nil, fmt.Errorf("dedup check: %w", err)
		}
		if already {
			_ = p.runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusSkipped, nil)
			run.Status = domain.RunStatusSkipped
			p.emitRunUpdate(run)
			return run, nil
		}
	}

	// Execute LLM.
	llmReq := executor.LLMRequest{
		SystemPrompt: systemPrompt,
		Prompt:       taskPrompt,
		Workspace:    workspace,
		Tools:        req.Duty.RequiredTools,
		Model:        backend.Model,
		Effort:       backend.DefaultEffort,
	}
	llmResult, llmErr := req.Executor.Run(ctx, llmReq)
	if llmErr != nil {
		// The executor also returns a partial result (transcript, tokens
		// accumulated before the failure); record it for audit alongside the
		// error. Outputs and dedup marking are skipped, as on every failure.
		errMsg := llmErr.Error()
		if uerr := p.runRepo.UpdateResult(ctx, run.ID, &llmResult, nil, domain.RunStatusFailed); uerr != nil {
			return nil, fmt.Errorf("record run result: %w", uerr)
		}
		_ = p.runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusFailed, &errMsg)
		run.LLMResult = &llmResult
		run.Tokens = llmResult.Tokens
		run.Cost = llmResult.Cost
		run.Status = domain.RunStatusFailed
		run.Error = &errMsg
		finished := time.Now()
		run.FinishedAt = &finished
		p.emitRunUpdate(run)
		return run, fmt.Errorf("executor: %w", llmErr)
	}

	// Model-reported failure: a nonzero status means the work did not succeed.
	// Record the full result for audit (including the transcript) but skip
	// output delivery — a failed run must not post half-formed outputs — and
	// skip dedup marking so the work can be retried. This also captures the
	// claude path's is_error, which parseClaudeOutput maps to Status 1.
	if llmResult.Status != 0 {
		errMsg := fmt.Sprintf("llm reported failure status %d: %s", llmResult.Status, llmResult.Summary)
		if err := p.runRepo.UpdateResult(ctx, run.ID, &llmResult, nil, domain.RunStatusFailed); err != nil {
			return nil, fmt.Errorf("record run result: %w", err)
		}
		_ = p.runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusFailed, &errMsg)
		run.LLMResult = &llmResult
		run.Tokens = llmResult.Tokens
		run.Cost = llmResult.Cost
		run.Status = domain.RunStatusFailed
		run.Error = &errMsg
		finished := time.Now()
		run.FinishedAt = &finished
		p.emitRunUpdate(run)
		return run, nil
	}

	// Deliver outputs.
	deliveries := outputs.Deliver(ctx, req.Assignment.Outputs, llmResult, promptCtx)

	// Record completion.
	status := domain.RunStatusSucceeded
	for _, d := range deliveries {
		if d.Status == "failed" {
			status = domain.RunStatusFailed
			break
		}
	}
	if err := p.runRepo.UpdateResult(ctx, run.ID, &llmResult, deliveries, status); err != nil {
		return nil, fmt.Errorf("record run result: %w", err)
	}
	if dedupKey != "" {
		_ = p.store.MarkProcessed(ctx, req.Assignment.ID.String(), dedupKey)
	}

	run.LLMResult = &llmResult
	run.Tokens = llmResult.Tokens
	run.Cost = llmResult.Cost
	run.OutputsDelivered = deliveries
	run.Status = status
	finished := time.Now()
	run.FinishedAt = &finished
	p.emitRunUpdate(run)
	return run, nil
}

// findAssignmentConfig maps a domain.Assignment back to its config.AssignmentConfig for backend resolution.
func findAssignmentConfig(cfg *config.Config, a *domain.Assignment, agentName, dutyName string) config.AssignmentConfig {
	for _, ac := range cfg.Assignments {
		if ac.Agent == agentName && ac.Duty == dutyName {
			return ac
		}
	}
	// Return a minimal AssignmentConfig so ResolveBackend can still work via Agent/Duty fallback.
	var ac config.AssignmentConfig
	ac.Agent = agentName
	ac.Duty = dutyName
	if a.Backend != nil {
		ac.Backend = a.Backend
	}
	return ac
}

// deriveDedupKey extracts a deduplication key from event params. An explicit
// dedup_key (set by the event envelope) takes precedence over inferred keys:
// a re-pushed MR carries a NEW dedup_key but the SAME mr_iid, and must not be
// collapsed onto the mr_iid-derived key.
func deriveDedupKey(params map[string]any) string {
	if v, ok := params["dedup_key"]; ok {
		return fmt.Sprintf("dedup_key:%v", v)
	}
	if v, ok := params["mr_iid"]; ok {
		return fmt.Sprintf("mr_iid:%v", v)
	}
	if v, ok := params["commit_sha"]; ok {
		return fmt.Sprintf("sha:%v", v)
	}
	return ""
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string { return &s }
