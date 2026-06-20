package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/forge"
	"github.com/cbarraford/office-fleet/internal/outputs"
	"github.com/cbarraford/office-fleet/internal/plugin"
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
	Skill        *domain.Skill
	TriggerKind string
	EventID     *string        // id of the triggering event, if any (event-subscription)
	EventParams map[string]any // operator params for manual; event payload for event-subscription
	Executor    executor.Executor
	// Backend is the already-resolved backend (model/effort). When set, the
	// pipeline reuses it instead of re-resolving from config (issue #11), which
	// keeps backend resolution single-sourced from the DB refs.
	Backend *config.Backend
}

// Skip reasons recorded on a Run when the pause gate prevents execution.
const (
	SkipReasonAgentPaused      = "agent_paused"
	SkipReasonAssignmentPaused = "assignment_paused"
	// SkipReasonDuplicateEvent marks redeliveries collapsed by per-assignment
	// dedup (dogfood finding: a reason-less skipped run is unreadable in the UI).
	SkipReasonDuplicateEvent = "duplicate_event"
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
			SkillID:       req.Skill.ID,
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
		Agent:      map[string]any{"name": req.Agent.Name, "role": req.Agent.Role.String(), "system_prompt": req.Agent.SystemPrompt},
		Skill:       map[string]any{"name": req.Skill.Name, "role": req.Skill.Role.String(), "description": req.Skill.Description},
		Assignment: req.Assignment.Config,
		State:      map[string]any{},
		Now:        time.Now(),
	}
	if promptCtx.Event == nil {
		promptCtx.Event = map[string]any{}
	}

	fp, err := resolveForge(req.Assignment.Config)
	if err != nil {
		return nil, err
	}
	promptCtx.Forge = fp

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

	// Select task prompt: override or skill default.
	taskTemplate := req.Skill.Prompt
	if req.Assignment.TaskPromptOverride != nil && *req.Assignment.TaskPromptOverride != "" {
		taskTemplate = *req.Assignment.TaskPromptOverride
	}
	extraInstructions := ""
	if req.Assignment.ExtraInstructions != nil {
		extraInstructions = *req.Assignment.ExtraInstructions
	}

	// Render prompts. Secrets are passed out of band (only the `secret` helper
	// can reach them); the rendered output may contain real secret values (e.g.
	// a token in a clone URL) — those are redacted before persistence below.
	systemPrompt, taskPrompt, err := prompt.ComposePrompts(
		req.Agent.SystemPrompt, taskTemplate, extraInstructions, promptCtx, secretsMap)
	if err != nil {
		return nil, fmt.Errorf("compose prompts: %w", err)
	}

	// Backend model/effort: reuse the Invoker's single DB-sourced resolution
	// when provided (issue #11); otherwise (direct pipeline callers / tests)
	// fall back to resolving from config.
	backend := req.Backend
	if backend == nil {
		backend, _, err = config.ResolveBackend(p.cfg, findAssignmentConfig(p.cfg, req.Assignment, req.Agent.Name, req.Skill.Name))
		if err != nil {
			return nil, fmt.Errorf("resolve backend: %w", err)
		}
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
		SkillID:               req.Skill.ID,
		TriggerKind:          req.TriggerKind,
		EventID:              req.EventID,
		RenderedSystemPrompt: redactSecrets(systemPrompt, secretsMap),
		RenderedPrompt:       redactSecrets(taskPrompt, secretsMap),
		Status:               domain.RunStatusRunning,
		StartedAt:            time.Now(),
	}
	if err := p.runRepo.Insert(ctx, run); err != nil {
		return nil, fmt.Errorf("record run start: %w", err)
	}
	p.emitRunUpdate(run)

	// Dedup: atomically CLAIM this event for this assignment. The claim happens
	// BEFORE the (multi-minute) LLM run, so a concurrent re-fire loses the race
	// here rather than both running and double-posting (issue #5, TOCTOU). The
	// claim is RELEASED on any failure below so a failed event can be retried;
	// only a successful run keeps it. Insert is intentionally before this check
	// so every invocation is recorded; duplicates are stored as RunStatusSkipped.
	dedupKey := deriveDedupKey(req.EventParams)
	claimedDedup := false
	releaseDedup := func() {
		if claimedDedup {
			_ = p.store.UnmarkProcessed(ctx, req.Assignment.ID.String(), dedupKey)
		}
	}
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
		claimedDedup = true
	}

	// Fail fast before spending an LLM call if any plugin this assignment will
	// deliver to failed to initialize (issue #3). Otherwise the misconfiguration
	// (e.g. an empty gitlab_token) only surfaces as a delivery error AFTER the
	// paid run has executed.
	for _, out := range req.Assignment.Outputs {
		ierr := plugin.InitError(out.Plugin)
		if ierr == nil {
			continue
		}
		errMsg := fmt.Sprintf("output plugin %q is not usable: %v", out.Plugin, ierr)
		if uerr := p.runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusFailed, &errMsg); uerr != nil {
			return nil, fmt.Errorf("record run failure: %w", uerr)
		}
		run.Status = domain.RunStatusFailed
		run.Error = &errMsg
		finished := time.Now()
		run.FinishedAt = &finished
		releaseDedup()
		p.emitRunUpdate(run)
		return run, nil
	}

	// Execute LLM.
	llmReq := executor.LLMRequest{
		SystemPrompt: systemPrompt,
		Prompt:       taskPrompt,
		Workspace:    workspace,
		Tools:        req.Skill.RequiredTools,
		Model:        backend.Model,
		Effort:       backend.DefaultEffort,
	}
	llmResult, llmErr := req.Executor.Run(ctx, llmReq)
	// Redact secret values from everything that is persisted or delivered: the
	// agent needed the real secret to do its work, but it must not survive into
	// run records, the stored transcript, or a delivered action body (issue #4).
	llmResult.Summary = redactSecrets(llmResult.Summary, secretsMap)
	llmResult.Transcript = redactSecrets(llmResult.Transcript, secretsMap)
	// Cap the stored transcript so a chatty run can't write a multi-MB row and
	// grow the runs table unbounded (issue #16).
	llmResult.Transcript = capTranscript(llmResult.Transcript, maxStoredTranscriptBytes)
	if llmErr != nil {
		// The executor also returns a partial result (transcript, tokens
		// accumulated before the failure); record it for audit alongside the
		// error. Outputs are skipped and the dedup claim is released so the
		// event can be retried, as on every failure.
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
		releaseDedup()
		p.emitRunUpdate(run)
		return run, fmt.Errorf("executor: %w", llmErr)
	}

	// Model-reported failure: a nonzero status means the work did not succeed.
	// Record the full result for audit (including the transcript) but skip
	// output delivery — a failed run must not post half-formed outputs — and
	// release the dedup claim so the work can be retried. This also captures the
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
		releaseDedup()
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
	// Keep the dedup claim only on success; release it on a delivery failure so
	// the event can be retried (issue #5).
	if status != domain.RunStatusSucceeded {
		releaseDedup()
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
func findAssignmentConfig(cfg *config.Config, a *domain.Assignment, agentName, skillName string) config.AssignmentConfig {
	for _, ac := range cfg.Assignments {
		if ac.Agent == agentName && ac.Skill == skillName {
			return ac
		}
	}
	// Return a minimal AssignmentConfig so ResolveBackend can still work via Agent/Skill fallback.
	var ac config.AssignmentConfig
	ac.Agent = agentName
	ac.Skill = skillName
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

// maxStoredTranscriptBytes caps the transcript persisted in a run record.
const maxStoredTranscriptBytes = 256 * 1024

// capTranscript truncates s to at most max bytes, trimming any split rune at the
// cut so the stored value stays valid UTF-8 (issue #16).
func capTranscript(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "\n…[transcript truncated]"
}

// resolveForge picks the Forge profile for an assignment's config. Absent or
// empty `forge` defaults to gitlab (back-compat). Unknown forge is a hard error.
func resolveForge(cfg map[string]any) (map[string]any, error) {
	name := "gitlab"
	if f, ok := cfg["forge"].(string); ok && f != "" {
		name = f
	}
	fp, ok := forge.Profile(name)
	if !ok {
		return nil, fmt.Errorf("unknown forge %q (want gitlab or github)", name)
	}
	return fp, nil
}

// redactSecrets replaces every known secret value in s with a placeholder
// before the text is persisted or delivered (issue #4). Values shorter than 4
// characters are skipped to avoid pathological over-redaction of ordinary text.
func redactSecrets(s string, secrets map[string]string) string {
	for _, v := range secrets {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "***REDACTED***")
		}
	}
	return s
}
