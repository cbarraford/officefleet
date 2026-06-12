package run

import (
	"context"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/google/uuid"
)

// AssignmentGetter, AgentGetter, and DutyGetter are the repo capabilities the
// Invoker needs; *repo.AssignmentRepo, *repo.AgentRepo, *repo.DutyRepo satisfy
// them structurally.
type AssignmentGetter interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Assignment, error)
}

type AgentGetter interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error)
}

type DutyGetter interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Duty, error)
}

// Invoker executes one assignment by id: it loads the assignment/agent/duty,
// resolves the backend from config, builds the executor, and runs the
// pipeline. The cron scheduler and the event dispatcher share this path.
type Invoker struct {
	cfg         *config.Config
	pipeline    *Pipeline
	assignments AssignmentGetter
	agents      AgentGetter
	duties      DutyGetter
	// buildExecutor is a test seam; defaults to factory-based resolution.
	buildExecutor func(cfg *config.Config, b *config.Backend) (executor.Executor, error)
}

func NewInvoker(cfg *config.Config, pipeline *Pipeline, assignments AssignmentGetter, agents AgentGetter, duties DutyGetter) *Invoker {
	return NewInvokerWithExecutorBuilder(cfg, pipeline, assignments, agents, duties, defaultBuildExecutor)
}

func NewInvokerWithExecutorBuilder(
	cfg *config.Config,
	pipeline *Pipeline,
	assignments AssignmentGetter,
	agents AgentGetter,
	duties DutyGetter,
	buildExecutor func(cfg *config.Config, b *config.Backend) (executor.Executor, error),
) *Invoker {
	if buildExecutor == nil {
		buildExecutor = defaultBuildExecutor
	}
	return &Invoker{
		cfg: cfg, pipeline: pipeline,
		assignments: assignments, agents: agents, duties: duties,
		buildExecutor: buildExecutor,
	}
}

// defaultBuildExecutor keeps SP1's behavior: no resolvable backend means the
// subscription claude CLI; otherwise the factory dispatches on kind.
func defaultBuildExecutor(cfg *config.Config, b *config.Backend) (executor.Executor, error) {
	if b == nil {
		return executor.NewClaudeExecutor(""), nil
	}
	return executor.FromBackend(cfg, b)
}

// Invoke runs one assignment end-to-end and returns the recorded Run.
func (inv *Invoker) Invoke(ctx context.Context, assignmentID uuid.UUID, triggerKind string, eventID *string, params map[string]any) (*domain.Run, error) {
	assignment, err := inv.assignments.GetByID(ctx, assignmentID)
	if err != nil {
		return nil, fmt.Errorf("get assignment: %w", err)
	}

	agent, err := inv.agents.GetByID(ctx, assignment.AgentID)
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}

	duty, err := inv.duties.GetByID(ctx, assignment.DutyID)
	if err != nil {
		return nil, fmt.Errorf("get duty: %w", err)
	}

	// Resolve the named backend from config (nil when this assignment has no
	// config counterpart, e.g. DB-only seeds).
	var resolved *config.Backend
	for _, ac := range inv.cfg.Assignments {
		if ac.Agent == agent.Name && ac.Duty == duty.Name {
			if b, _, berr := config.ResolveBackend(inv.cfg, ac); berr == nil {
				resolved = b
			}
			break
		}
	}
	exec, err := inv.buildExecutor(inv.cfg, resolved)
	if err != nil {
		return nil, fmt.Errorf("build executor: %w", err)
	}

	return inv.pipeline.Execute(ctx, ExecuteRequest{
		Assignment:  assignment,
		Agent:       agent,
		Duty:        duty,
		TriggerKind: triggerKind,
		EventID:     eventID,
		EventParams: params,
		Executor:    exec,
	})
}
