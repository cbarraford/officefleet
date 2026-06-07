package run

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/outputs"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/prompt"
	"github.com/cbarraford/office-fleet/internal/repo"
	"github.com/cbarraford/office-fleet/internal/state"
)

// runRepo is the interface Pipeline uses for run persistence.
// *repo.RunRepo satisfies this interface.
type runRepo interface {
	Insert(ctx context.Context, run *domain.Run) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.RunStatus, errMsg *string) error
	UpdateResult(ctx context.Context, id uuid.UUID, result *domain.LLMResult, outputs []domain.OutputDelivery, status domain.RunStatus) error
}

// Pipeline executes Assignments end-to-end.
type Pipeline struct {
	cfg     *config.Config
	runRepo runRepo
	store   state.Store
	plugins map[string]plugin.Plugin // name -> initialized plugin
}

func NewPipeline(cfg *config.Config, rr *repo.RunRepo, store state.Store) *Pipeline {
	plugins := map[string]plugin.Plugin{}
	for _, p := range plugin.All() {
		plugins[p.Name()] = p
	}
	return &Pipeline{cfg: cfg, runRepo: rr, store: store, plugins: plugins}
}

// ExecuteRequest is the input for one run invocation.
type ExecuteRequest struct {
	Assignment  *domain.Assignment
	Agent       *domain.Agent
	Duty        *domain.Duty
	TriggerKind string
	EventParams map[string]any // operator params for manual; event payload for event-subscription
	Executor    executor.Executor
}

// Execute runs the full pipeline for one assignment and records the result.
func (p *Pipeline) Execute(ctx context.Context, req ExecuteRequest) (*domain.Run, error) {
	// Build prompt context.
	promptCtx := prompt.Context{
		Event:      req.EventParams,
		Agent:      map[string]any{"name": req.Agent.Name, "role": req.Agent.Role, "system_prompt": req.Agent.SystemPrompt},
		Duty:       map[string]any{"name": req.Duty.Name, "role": req.Duty.Role, "description": req.Duty.Description},
		Assignment: map[string]any(req.Assignment.Config),
		State:      map[string]any{},
		Now:        time.Now(),
	}
	if promptCtx.Event == nil {
		promptCtx.Event = map[string]any{}
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
	backend, _, err := config.ResolveBackend(p.cfg, findAssignmentConfig(p.cfg, req.Assignment))
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
		RenderedSystemPrompt: systemPrompt,
		RenderedPrompt:       taskPrompt,
		Status:               domain.RunStatusRunning,
		StartedAt:            time.Now(),
	}
	if err := p.runRepo.Insert(ctx, run); err != nil {
		return nil, fmt.Errorf("record run start: %w", err)
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
		errMsg := llmErr.Error()
		_ = p.runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusFailed, &errMsg)
		run.Status = domain.RunStatusFailed
		run.Error = &errMsg
		return run, fmt.Errorf("executor: %w", llmErr)
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

	run.LLMResult = &llmResult
	run.OutputsDelivered = deliveries
	run.Status = status
	finished := time.Now()
	run.FinishedAt = &finished
	return run, nil
}

// findAssignmentConfig maps a domain.Assignment back to its config.AssignmentConfig for backend resolution.
func findAssignmentConfig(cfg *config.Config, a *domain.Assignment) config.AssignmentConfig {
	for _, ac := range cfg.Assignments {
		// Match by agent+duty names is not possible directly; look up by IDs is not in config.
		// Use the assignment's Backend field directly if set.
		_ = ac
	}
	// Return a minimal AssignmentConfig so ResolveBackend can still work via Agent/Duty fallback.
	var ac config.AssignmentConfig
	if a.Backend != nil {
		ac.Backend = a.Backend
	}
	return ac
}
