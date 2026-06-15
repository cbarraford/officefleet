package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AssignmentRepo struct{ db *pgxpool.Pool }

func NewAssignmentRepo(db *pgxpool.Pool) *AssignmentRepo { return &AssignmentRepo{db: db} }

func (r *AssignmentRepo) Insert(ctx context.Context, a *domain.Assignment) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	triggerJSON, _ := json.Marshal(a.Trigger)
	outputsJSON, _ := json.Marshal(a.Outputs)
	configJSON, _ := json.Marshal(a.Config)
	var backendJSON []byte
	if a.Backend != nil {
		backendJSON, _ = json.Marshal(a.Backend)
	}
	_, err := r.db.Exec(ctx,
		"INSERT INTO assignments (id, agent_id, duty_id, name, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)",
		a.ID, a.AgentID, a.DutyID, a.Name, a.Enabled, triggerJSON, outputsJSON, configJSON,
		backendJSON, a.TaskPromptOverride, a.ExtraInstructions)
	return err
}

func (r *AssignmentRepo) UpsertByAgentAndDuty(ctx context.Context, a *domain.Assignment) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	triggerJSON, _ := json.Marshal(a.Trigger)
	outputsJSON, _ := json.Marshal(a.Outputs)
	configJSON, _ := json.Marshal(a.Config)
	var backendJSON []byte
	if a.Backend != nil {
		backendJSON, _ = json.Marshal(a.Backend)
	}
	return r.db.QueryRow(ctx,
		`INSERT INTO assignments (id, agent_id, duty_id, name, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		 ON CONFLICT (agent_id, duty_id, name) DO UPDATE SET
		   enabled=EXCLUDED.enabled,
		   trigger=EXCLUDED.trigger,
		   outputs=EXCLUDED.outputs,
		   config=EXCLUDED.config,
		   backend=EXCLUDED.backend,
		   task_prompt_override=EXCLUDED.task_prompt_override,
		   extra_instructions=EXCLUDED.extra_instructions,
		   updated_at=NOW()
		 RETURNING id`,
		a.ID, a.AgentID, a.DutyID, a.Name, a.Enabled, triggerJSON, outputsJSON, configJSON,
		backendJSON, a.TaskPromptOverride, a.ExtraInstructions,
	).Scan(&a.ID)
}

func (r *AssignmentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Assignment, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, agent_id, duty_id, name, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions, created_at, updated_at FROM assignments WHERE id=$1", id)
	return scanAssignment(row)
}

func (r *AssignmentRepo) GetByAgentAndDuty(ctx context.Context, agentID, dutyID uuid.UUID) (*domain.Assignment, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, agent_id, duty_id, name, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions, created_at, updated_at FROM assignments WHERE agent_id=$1 AND duty_id=$2 LIMIT 1", agentID, dutyID)
	return scanAssignment(row)
}

func (r *AssignmentRepo) List(ctx context.Context) ([]*domain.Assignment, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, agent_id, duty_id, name, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions, created_at, updated_at FROM assignments ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *AssignmentRepo) Update(ctx context.Context, a *domain.Assignment) error {
	triggerJSON, _ := json.Marshal(a.Trigger)
	outputsJSON, _ := json.Marshal(a.Outputs)
	configJSON, _ := json.Marshal(a.Config)
	var backendJSON []byte
	if a.Backend != nil {
		backendJSON, _ = json.Marshal(a.Backend)
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE assignments SET enabled=$2, trigger=$3, outputs=$4, config=$5, backend=$6,
		   task_prompt_override=$7, extra_instructions=$8, name=$9, updated_at=NOW() WHERE id=$1`,
		a.ID, a.Enabled, triggerJSON, outputsJSON, configJSON, backendJSON,
		a.TaskPromptOverride, a.ExtraInstructions, a.Name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("assignment %s not found", a.ID)
	}
	return nil
}

func (r *AssignmentRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM assignments WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("assignment %s not found", id)
	}
	return nil
}

func scanAssignment(s scanner) (*domain.Assignment, error) {
	var a domain.Assignment
	var triggerJSON, outputsJSON, configJSON, backendJSON []byte
	if err := s.Scan(&a.ID, &a.AgentID, &a.DutyID, &a.Name, &a.Enabled,
		&triggerJSON, &outputsJSON, &configJSON, &backendJSON,
		&a.TaskPromptOverride, &a.ExtraInstructions,
		&a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan assignment: %w", err)
	}
	if err := unmarshalJSONB(triggerJSON, &a.Trigger); err != nil {
		return nil, fmt.Errorf("scan assignment %s: trigger: %w", a.ID, err)
	}
	if err := unmarshalJSONB(outputsJSON, &a.Outputs); err != nil {
		return nil, fmt.Errorf("scan assignment %s: outputs: %w", a.ID, err)
	}
	if err := unmarshalJSONB(configJSON, &a.Config); err != nil {
		return nil, fmt.Errorf("scan assignment %s: config: %w", a.ID, err)
	}
	// A pointer target leaves Backend nil for a JSON "null" or NULL column.
	if err := unmarshalJSONB(backendJSON, &a.Backend); err != nil {
		return nil, fmt.Errorf("scan assignment %s: backend: %w", a.ID, err)
	}
	return &a, nil
}
