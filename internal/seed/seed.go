package seed

import (
	"context"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// AgentSeeder, SkillSeeder, AssignmentSeeder are the repo capabilities seeding
// needs; the concrete repos satisfy them.
type AgentSeeder interface {
	UpsertByName(ctx context.Context, a *domain.Agent) error
	List(ctx context.Context) ([]*domain.Agent, error)
}

type SkillSeeder interface {
	UpsertByName(ctx context.Context, d *domain.Skill) error
	List(ctx context.Context) ([]*domain.Skill, error)
}

type AssignmentSeeder interface {
	UpsertByAgentAndSkill(ctx context.Context, a *domain.Assignment) error
	List(ctx context.Context) ([]*domain.Assignment, error)
}

// FromConfig seeds agents/skills/assignments from cfg. The DB is the source
// of truth once populated: without force, seeding is skipped unless ALL three
// entity tables are empty (first boot). force re-seeds unconditionally,
// overwriting same-named entities (UI edits included).
func FromConfig(ctx context.Context, cfg *config.Config,
	agentRepo AgentSeeder, skillRepo SkillSeeder, assignRepo AssignmentSeeder, force bool,
) error {
	if !force {
		agents, err := agentRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("seed precheck (agents): %w", err)
		}
		skills, err := skillRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("seed precheck (skills): %w", err)
		}
		assignments, err := assignRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("seed precheck (assignments): %w", err)
		}
		if len(agents) > 0 || len(skills) > 0 || len(assignments) > 0 {
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

	// Upsert skills and build a name→id map.
	skillIDs := make(map[string]uuid.UUID, len(cfg.Skills))
	for i := range cfg.Skills {
		dc := cfg.Skills[i]
		skill := &domain.Skill{
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
		if err := skillRepo.UpsertByName(ctx, skill); err != nil {
			return fmt.Errorf("upsert skill %q: %w", dc.Name, err)
		}
		skillIDs[dc.Name] = skill.ID
	}

	// Upsert assignments using the persisted agent and skill IDs.
	for i := range cfg.Assignments {
		ac := cfg.Assignments[i]
		agentID, agentOK := agentIDs[ac.Agent]
		skillID, skillOK := skillIDs[ac.Skill]
		if !agentOK {
			return fmt.Errorf("assignment references unknown agent %q", ac.Agent)
		}
		if !skillOK {
			return fmt.Errorf("assignment references unknown skill %q", ac.Skill)
		}
		assignment := &domain.Assignment{
			AgentID:            agentID,
			SkillID:             skillID,
			Name:               ac.Name,
			Enabled:            ac.Enabled,
			Trigger:            ac.Trigger,
			Outputs:            ac.Outputs,
			Config:             ac.Config,
			Backend:            ac.Backend,
			TaskPromptOverride: ac.TaskPromptOverride,
			ExtraInstructions:  ac.ExtraInstructions,
		}
		if err := assignRepo.UpsertByAgentAndSkill(ctx, assignment); err != nil {
			return fmt.Errorf("upsert assignment (agent=%q skill=%q name=%q): %w", ac.Agent, ac.Skill, ac.Name, err)
		}
	}

	// Warn about DB assignments no longer present in config. Seeding upserts but
	// never deletes, so a config edit that drops an assignment leaves the old
	// row behind and still firing — surface it rather than silently diverging.
	configTuples := make(map[string]bool, len(cfg.Assignments))
	for _, ac := range cfg.Assignments {
		configTuples[agentIDs[ac.Agent].String()+"\x00"+skillIDs[ac.Skill].String()+"\x00"+ac.Name] = true
	}
	dbAssignments, err := assignRepo.List(ctx)
	if err != nil {
		return fmt.Errorf("seed orphan check: %w", err)
	}
	for _, a := range dbAssignments {
		if !configTuples[a.AgentID.String()+"\x00"+a.SkillID.String()+"\x00"+a.Name] {
			fmt.Printf("warning: assignment %s (agent_id=%s skill_id=%s name=%q) is in the DB but not in config; it will keep firing until deleted\n",
				a.ID, a.AgentID, a.SkillID, a.Name)
		}
	}
	return nil
}
