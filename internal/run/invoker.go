package run

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/google/uuid"
)

// defaultRunTimeout bounds a single assignment run so a hung claude CLI, a
// stalled HTTP call, or a wedged tool cannot block a daemon worker forever
// (issue #8). Cancellation reaps the claude process group (see claude.go).
const defaultRunTimeout = 15 * time.Minute

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
	secrets     SecretsProvider // resolves ${secret:...} backend api_keys; may be nil in tests
	// buildExecutor is a test seam; defaults to factory-based resolution.
	buildExecutor func(cfg *config.Config, b *config.Backend) (executor.Executor, error)
}

func NewInvoker(cfg *config.Config, pipeline *Pipeline, assignments AssignmentGetter, agents AgentLister, duties DutyLister, secrets SecretsProvider) *Invoker {
	return &Invoker{
		cfg: cfg, pipeline: pipeline,
		assignments: assignments, agents: agents, duties: duties,
		secrets:       secrets,
		buildExecutor: defaultBuildExecutor,
	}
}

// secretRefRe matches ${secret:name} references in backend auth config.
var secretRefRe = regexp.MustCompile(`\$\{secret:([^}]+)\}`)

// resolveSecretRefs replaces every ${secret:name} in s with the decrypted value,
// failing closed if any referenced secret is missing or empty (issue #24).
func resolveSecretRefs(s string, secrets map[string]string) (string, error) {
	var missing string
	out := secretRefRe.ReplaceAllStringFunc(s, func(m string) string {
		name := secretRefRe.FindStringSubmatch(m)[1]
		v, ok := secrets[name]
		if !ok || v == "" {
			missing = name
			return m
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("secret %q not found", missing)
	}
	return out, nil
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
	ctx, cancel := context.WithTimeout(ctx, defaultRunTimeout)
	defer cancel()

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
	// Resolve ${secret:...} backend api_key references against the encrypted
	// secret store BEFORE the executor is built, failing closed if a referenced
	// secret is missing — otherwise the literal placeholder reaches executor
	// auth (issue #24). resolved is a copy, so mutating it never touches cfg.
	if resolved != nil && resolved.Auth.Mode == "api_key" && inv.secrets != nil {
		secretsMap, serr := inv.secrets.Load(ctx)
		if serr != nil {
			return nil, fmt.Errorf("load secrets for backend %q auth: %w", resolved.Name, serr)
		}
		key, kerr := resolveSecretRefs(resolved.Auth.APIKey, secretsMap)
		if kerr != nil {
			return nil, fmt.Errorf("backend %q api_key: %w", resolved.Name, kerr)
		}
		resolved.Auth.APIKey = key
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
