package seed

import (
	"context"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// AgentSeeder, DutySeeder, AssignmentSeeder are the repo capabilities seeding
// needs; the concrete repos satisfy them.
type AgentSeeder interface {
	UpsertByName(ctx context.Context, a *domain.Agent) error
	List(ctx context.Context) ([]*domain.Agent, error)
}

type DutySeeder interface {
	UpsertByName(ctx context.Context, d *domain.Duty) error
	List(ctx context.Context) ([]*domain.Duty, error)
}

type AssignmentSeeder interface {
	UpsertByAgentDutyAndName(ctx context.Context, a *domain.Assignment) error
	List(ctx context.Context) ([]*domain.Assignment, error)
}

// FromConfig seeds agents/duties/assignments from cfg. The DB is the source
// of truth once populated: without force, seeding is skipped unless ALL three
// entity tables are empty (first boot). force re-seeds unconditionally,
// overwriting same-named entities (UI edits included).
func FromConfig(ctx context.Context, cfg *config.Config,
	agentRepo AgentSeeder, dutyRepo DutySeeder, assignRepo AssignmentSeeder, force bool,
) error {
	if !force {
		agents, err := agentRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("seed precheck (agents): %w", err)
		}
		duties, err := dutyRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("seed precheck (duties): %w", err)
		}
		assignments, err := assignRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("seed precheck (assignments): %w", err)
		}
		if len(agents) > 0 || len(duties) > 0 || len(assignments) > 0 {
			fmt.Println("DB already populated; skipping config seed (use 'fleet seed --force' to overwrite)")
			return nil
		}
	}

	// Upsert agents and build a name→id map using the persisted id returned by RETURNING.
	agentIDs := make(map[string]uuid.UUID, len(cfg.Agents))
	for i := range cfg.Agents {
		ac := cfg.Agents[i]
		agent := &domain.Agent{
			Name:           ac.Name,
			Role:           ac.Role,
			SystemPrompt:   ac.SystemPrompt,
			DefaultBackend: ac.DefaultBackend,
			Enabled:        ac.Enabled,
		}
		if err := agentRepo.UpsertByName(ctx, agent); err != nil {
			return fmt.Errorf("upsert agent %q: %w", ac.Name, err)
		}
		agentIDs[ac.Name] = agent.ID
	}

	// Upsert duties and build a name→id map.
	dutyIDs := make(map[string]uuid.UUID, len(cfg.Duties))
	for i := range cfg.Duties {
		dc := cfg.Duties[i]
		duty := &domain.Duty{
			Name:          dc.Name,
			Role:          dc.Role,
			Description:   dc.Description,
			TriggerKinds:  dc.TriggerKinds,
			Prompt:        dc.Prompt,
			RequiredTools: dc.RequiredTools,
			OutputActions: dc.OutputActions,
			ConfigSchema:  dc.ConfigSchema,
			Backend:       dc.Backend,
		}
		if err := dutyRepo.UpsertByName(ctx, duty); err != nil {
			return fmt.Errorf("upsert duty %q: %w", dc.Name, err)
		}
		dutyIDs[dc.Name] = duty.ID
	}

	// Upsert assignments using the persisted agent and duty IDs.
	for i := range cfg.Assignments {
		ac := cfg.Assignments[i]
		agentID, agentOK := agentIDs[ac.Agent]
		dutyID, dutyOK := dutyIDs[ac.Duty]
		if !agentOK {
			return fmt.Errorf("assignment references unknown agent %q", ac.Agent)
		}
		if !dutyOK {
			return fmt.Errorf("assignment references unknown duty %q", ac.Duty)
		}
		assignment := &domain.Assignment{
			Name:               defaultAssignmentName(ac),
			AgentID:            agentID,
			DutyID:             dutyID,
			Enabled:            ac.Enabled,
			Trigger:            ac.Trigger,
			Outputs:            ac.Outputs,
			Config:             ac.Config,
			Backend:            ac.Backend,
			TaskPromptOverride: ac.TaskPromptOverride,
			ExtraInstructions:  ac.ExtraInstructions,
		}
		if err := assignRepo.UpsertByAgentDutyAndName(ctx, assignment); err != nil {
			return fmt.Errorf("upsert assignment (agent=%q duty=%q): %w", ac.Agent, ac.Duty, err)
		}
	}
	return nil
}

func defaultAssignmentName(ac config.AssignmentConfig) string {
	if ac.Name != "" {
		return ac.Name
	}
	if ac.Trigger.Kind != "" {
		return ac.Trigger.Kind
	}
	return "default"
}
