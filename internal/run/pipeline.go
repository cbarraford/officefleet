package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
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
	// SkipReasonDuplicateEvent marks redeliveries collapsed by per-assignment
	// dedup (dogfood finding: a reason-less skipped run is unreadable in the UI).
	SkipReasonDuplicateEvent = "duplicate_event"
	SkipReasonModelSkip      = "model_skip"
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

	// Dedup: atomically claim this event for this assignment before the expensive
	// LLM run. Insert is intentionally called before this check so duplicates are
	// still audited as skipped runs rather than silently dropped.
	dedupKey := deriveDedupKey(req.EventParams)
	dedupClaimed := false
	if dedupKey != "" {
		claimed, err := p.store.ClaimProcessed(ctx, req.Assignment.ID.String(), dedupKey)
		if err != nil {
			return nil, fmt.Errorf("dedup claim: %w", err)
		}
		if !claimed {
			reason := SkipReasonDuplicateEvent
			_ = p.runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusSkipped, &reason)
			run.Status = domain.RunStatusSkipped
			run.Error = &reason
			p.emitRunUpdate(run)
			return run, nil
		}
		dedupClaimed = true
	}
	releaseDedupClaim := func() error {
		if !dedupClaimed {
			return nil
		}
		return p.store.DeleteProcessed(ctx, req.Assignment.ID.String(), dedupKey)
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
		if err := releaseDedupClaim(); err != nil {
			return run, fmt.Errorf("release dedup claim: %w", err)
		}
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
		if err := releaseDedupClaim(); err != nil {
			return run, fmt.Errorf("release dedup claim: %w", err)
		}
		return run, nil
	}

	if isSkipResult(llmResult) {
		reason := SkipReasonModelSkip
		if err := p.runRepo.UpdateResult(ctx, run.ID, &llmResult, nil, domain.RunStatusSkipped); err != nil {
			return nil, fmt.Errorf("record run result: %w", err)
		}
		_ = p.runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusSkipped, &reason)
		run.LLMResult = &llmResult
		run.Tokens = llmResult.Tokens
		run.Cost = llmResult.Cost
		run.Status = domain.RunStatusSkipped
		run.Error = &reason
		finished := time.Now()
		run.FinishedAt = &finished
		p.emitRunUpdate(run)
		if err := releaseDedupClaim(); err != nil {
			return run, fmt.Errorf("release dedup claim: %w", err)
		}
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
	if status == domain.RunStatusSucceeded {
		if reviewedSHA := extractReviewedSHA(llmResult); reviewedSHA != "" {
			if err := p.store.Set(ctx, req.Assignment.ID.String(), "last_reviewed_sha", []byte(reviewedSHA)); err != nil {
				if releaseErr := releaseDedupClaim(); releaseErr != nil {
					return nil, fmt.Errorf("release dedup claim after state write failure: %w", releaseErr)
				}
				return nil, fmt.Errorf("record reviewed sha: %w", err)
			}
		}
	}
	if err := p.runRepo.UpdateResult(ctx, run.ID, &llmResult, deliveries, status); err != nil {
		return nil, fmt.Errorf("record run result: %w", err)
	}
	if status != domain.RunStatusSucceeded {
		if err := releaseDedupClaim(); err != nil {
			return nil, fmt.Errorf("release dedup claim: %w", err)
		}
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

var reviewedSHARe = regexp.MustCompile(`(?m)\bREVIEWED_SHA=([A-Za-z0-9._/-]+)\b`)

// deriveDedupKey extracts a deduplication key from event params. An explicit
// dedup_key (set by the event envelope) takes precedence over inferred keys:
// a re-pushed MR carries a NEW dedup_key but the SAME mr_iid, and must not be
// collapsed onto an mr_iid-only key. MR dedup is only safe when a commit SHA is
// present; otherwise retries must run rather than skip a changed MR forever.
func deriveDedupKey(params map[string]any) string {
	if v := paramString(params, "dedup_key"); v != "" {
		return fmt.Sprintf("dedup_key:%s", v)
	}
	if mr := paramString(params, "mr_iid"); mr != "" {
		if sha := firstParamString(params, "last_commit_sha", "commit_sha", "head_sha"); sha != "" {
			return fmt.Sprintf("mr_iid:%s:sha:%s", mr, sha)
		}
		return ""
	}
	if pr := paramString(params, "pr_number"); pr != "" {
		if sha := firstParamString(params, "head_sha", "commit_sha", "last_commit_sha"); sha != "" {
			return fmt.Sprintf("pr_number:%s:sha:%s", pr, sha)
		}
		return ""
	}
	if sha := firstParamString(params, "commit_sha", "last_commit_sha", "head_sha"); sha != "" {
		return fmt.Sprintf("sha:%s", sha)
	}
	return ""
}

func paramString(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	v, ok := params[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

func firstParamString(params map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := paramString(params, key); v != "" {
			return v
		}
	}
	return ""
}

func isSkipResult(result domain.LLMResult) bool {
	if strings.EqualFold(strings.TrimSpace(result.Summary), "SKIP") {
		return true
	}
	for _, key := range []string{"summary", "result", "raw"} {
		if v, ok := result.Output[key].(string); ok && strings.EqualFold(strings.TrimSpace(v), "SKIP") {
			return true
		}
	}
	return false
}

func extractReviewedSHA(result domain.LLMResult) string {
	for _, key := range []string{"reviewed_sha", "REVIEWED_SHA"} {
		if v, ok := result.Output[key].(string); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	for _, text := range []string{result.Summary, result.Transcript} {
		if match := reviewedSHARe.FindStringSubmatch(text); len(match) == 2 {
			return match[1]
		}
	}
	if raw, ok := result.Output["raw"].(string); ok {
		if match := reviewedSHARe.FindStringSubmatch(raw); len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string { return &s }
