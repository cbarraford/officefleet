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
	if a.Name == "" {
		a.Name = defaultAssignmentName(a.Trigger.Kind)
	}
	triggerJSON, _ := json.Marshal(a.Trigger)
	outputsJSON, _ := json.Marshal(a.Outputs)
	configJSON, _ := json.Marshal(a.Config)
	var backendJSON []byte
	if a.Backend != nil {
		backendJSON, _ = json.Marshal(a.Backend)
	}
	_, err := r.db.Exec(ctx,
		"INSERT INTO assignments (id, name, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)",
		a.ID, a.Name, a.AgentID, a.DutyID, a.Enabled, triggerJSON, outputsJSON, configJSON,
		backendJSON, a.TaskPromptOverride, a.ExtraInstructions)
	return err
}

func (r *AssignmentRepo) UpsertByAgentDutyAndName(ctx context.Context, a *domain.Assignment) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	if a.Name == "" {
		a.Name = defaultAssignmentName(a.Trigger.Kind)
	}
	triggerJSON, _ := json.Marshal(a.Trigger)
	outputsJSON, _ := json.Marshal(a.Outputs)
	configJSON, _ := json.Marshal(a.Config)
	var backendJSON []byte
	if a.Backend != nil {
		backendJSON, _ = json.Marshal(a.Backend)
	}
	return r.db.QueryRow(ctx,
		`INSERT INTO assignments (id, name, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions)
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
		a.ID, a.Name, a.AgentID, a.DutyID, a.Enabled, triggerJSON, outputsJSON, configJSON,
		backendJSON, a.TaskPromptOverride, a.ExtraInstructions,
	).Scan(&a.ID)
}

func (r *AssignmentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Assignment, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions, created_at, updated_at FROM assignments WHERE id=$1", id)
	return scanAssignment(row)
}

func (r *AssignmentRepo) GetByAgentAndDuty(ctx context.Context, agentID, dutyID uuid.UUID) (*domain.Assignment, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions, created_at, updated_at FROM assignments WHERE agent_id=$1 AND duty_id=$2 ORDER BY name LIMIT 1", agentID, dutyID)
	return scanAssignment(row)
}

func (r *AssignmentRepo) List(ctx context.Context) ([]*domain.Assignment, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, name, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions, created_at, updated_at FROM assignments ORDER BY created_at")
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
		`UPDATE assignments SET name=$2, enabled=$3, trigger=$4, outputs=$5, config=$6, backend=$7,
		   task_prompt_override=$8, extra_instructions=$9, updated_at=NOW() WHERE id=$1`,
		a.ID, a.Name, a.Enabled, triggerJSON, outputsJSON, configJSON, backendJSON,
		a.TaskPromptOverride, a.ExtraInstructions)
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
	if err := s.Scan(&a.ID, &a.Name, &a.AgentID, &a.DutyID, &a.Enabled,
		&triggerJSON, &outputsJSON, &configJSON, &backendJSON,
		&a.TaskPromptOverride, &a.ExtraInstructions,
		&a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan assignment: %w", err)
	}
	_ = json.Unmarshal(triggerJSON, &a.Trigger)
	_ = json.Unmarshal(outputsJSON, &a.Outputs)
	_ = json.Unmarshal(configJSON, &a.Config)
	if len(backendJSON) > 2 {
		var b domain.BackendRef
		_ = json.Unmarshal(backendJSON, &b)
		a.Backend = &b
	}
	return &a, nil
}

func defaultAssignmentName(triggerKind string) string {
	if triggerKind != "" {
		return triggerKind
	}
	return "default"
}
