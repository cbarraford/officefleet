package run

import (
	"context"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/google/uuid"
)

// AssignmentGetter, AgentLister, and DutyLister are the repo capabilities the
// Invoker needs; *repo.AssignmentRepo, *repo.AgentRepo, *repo.DutyRepo satisfy
// them structurally.
type AssignmentGetter interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Assignment, error)
}

type AgentLister interface {
	List(ctx context.Context) ([]*domain.Agent, error)
}

type DutyLister interface {
	List(ctx context.Context) ([]*domain.Duty, error)
}

// Invoker executes one assignment by id: it loads the assignment/agent/duty,
// resolves the backend from config, builds the executor, and runs the
// pipeline. The cron scheduler and the event dispatcher share this path.
type Invoker struct {
	cfg         *config.Config
	pipeline    *Pipeline
	assignments AssignmentGetter
	agents      AgentLister
	duties      DutyLister
	// buildExecutor is a test seam; defaults to factory-based resolution.
	buildExecutor func(cfg *config.Config, b *config.Backend) (executor.Executor, error)
}

func NewInvoker(cfg *config.Config, pipeline *Pipeline, assignments AssignmentGetter, agents AgentLister, duties DutyLister) *Invoker {
	return &Invoker{
		cfg: cfg, pipeline: pipeline,
		assignments: assignments, agents: agents, duties: duties,
		buildExecutor: defaultBuildExecutor,
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

	allAgents, err := inv.agents.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	var agent *domain.Agent
	for _, a := range allAgents {
		if a.ID == assignment.AgentID {
			agent = a
			break
		}
	}
	if agent == nil {
		return nil, fmt.Errorf("agent %s not found", assignment.AgentID)
	}

	allDuties, err := inv.duties.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list duties: %w", err)
	}
	var duty *domain.Duty
	for _, d := range allDuties {
		if d.ID == assignment.DutyID {
			duty = d
			break
		}
	}
	if duty == nil {
		return nil, fmt.Errorf("duty %s not found", assignment.DutyID)
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
