package seed

import (
	"context"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/repo"
	"github.com/google/uuid"
)

// FromConfig upserts all agents, duties, and assignments defined in cfg into the DB.
// Uses INSERT ... ON CONFLICT DO UPDATE to be idempotent across repeated runs.
func FromConfig(ctx context.Context, cfg *config.Config,
	agentRepo *repo.AgentRepo,
	dutyRepo *repo.DutyRepo,
	assignRepo *repo.AssignmentRepo,
) error {
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
		if err := assignRepo.UpsertByAgentAndDuty(ctx, assignment); err != nil {
			return fmt.Errorf("upsert assignment (agent=%q duty=%q): %w", ac.Agent, ac.Duty, err)
		}
	}
	return nil
}
