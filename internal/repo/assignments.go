package repo

import (
	"context"
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
	triggerJSON, outputsJSON, configJSON, backendJSON, err := marshalAssignment(a)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx,
		"INSERT INTO assignments (id, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
		a.ID, a.AgentID, a.DutyID, a.Enabled, triggerJSON, outputsJSON, configJSON,
		backendJSON, a.TaskPromptOverride, a.ExtraInstructions)
	return err
}

func marshalAssignment(a *domain.Assignment) ([]byte, []byte, []byte, []byte, error) {
	triggerJSON, err := marshalJSONField("assignment trigger", a.Trigger)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	outputsJSON, err := marshalJSONField("assignment outputs", a.Outputs)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	configJSON, err := marshalJSONField("assignment config", a.Config)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var backendJSON []byte
	if a.Backend != nil {
		backendJSON, err = marshalJSONField("assignment backend", a.Backend)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return triggerJSON, outputsJSON, configJSON, backendJSON, nil
}

func (r *AssignmentRepo) UpsertByAgentAndDuty(ctx context.Context, a *domain.Assignment) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	triggerJSON, outputsJSON, configJSON, backendJSON, err := marshalAssignment(a)
	if err != nil {
		return err
	}
	return r.db.QueryRow(ctx,
		`INSERT INTO assignments (id, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		 ON CONFLICT (agent_id, duty_id) DO UPDATE SET
		   enabled=EXCLUDED.enabled,
		   trigger=EXCLUDED.trigger,
		   outputs=EXCLUDED.outputs,
		   config=EXCLUDED.config,
		   backend=EXCLUDED.backend,
		   task_prompt_override=EXCLUDED.task_prompt_override,
		   extra_instructions=EXCLUDED.extra_instructions,
		   updated_at=NOW()
		 RETURNING id`,
		a.ID, a.AgentID, a.DutyID, a.Enabled, triggerJSON, outputsJSON, configJSON,
		backendJSON, a.TaskPromptOverride, a.ExtraInstructions,
	).Scan(&a.ID)
}

func (r *AssignmentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Assignment, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions, created_at, updated_at FROM assignments WHERE id=$1", id)
	return scanAssignment(row)
}

func (r *AssignmentRepo) GetByAgentAndDuty(ctx context.Context, agentID, dutyID uuid.UUID) (*domain.Assignment, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions, created_at, updated_at FROM assignments WHERE agent_id=$1 AND duty_id=$2 LIMIT 1", agentID, dutyID)
	return scanAssignment(row)
}

func (r *AssignmentRepo) List(ctx context.Context) ([]*domain.Assignment, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, agent_id, duty_id, enabled, trigger, outputs, config, backend, task_prompt_override, extra_instructions, created_at, updated_at FROM assignments ORDER BY created_at")
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
	triggerJSON, outputsJSON, configJSON, backendJSON, err := marshalAssignment(a)
	if err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE assignments SET enabled=$2, trigger=$3, outputs=$4, config=$5, backend=$6,
		   task_prompt_override=$7, extra_instructions=$8, updated_at=NOW() WHERE id=$1`,
		a.ID, a.Enabled, triggerJSON, outputsJSON, configJSON, backendJSON,
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
	if err := s.Scan(&a.ID, &a.AgentID, &a.DutyID, &a.Enabled,
		&triggerJSON, &outputsJSON, &configJSON, &backendJSON,
		&a.TaskPromptOverride, &a.ExtraInstructions,
		&a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan assignment: %w", err)
	}
	if err := unmarshalJSONField("assignment trigger", triggerJSON, &a.Trigger); err != nil {
		return nil, err
	}
	if err := unmarshalJSONField("assignment outputs", outputsJSON, &a.Outputs); err != nil {
		return nil, err
	}
	if err := unmarshalJSONField("assignment config", configJSON, &a.Config); err != nil {
		return nil, err
	}
	if !isJSONNull(backendJSON) {
		var b domain.BackendRef
		if err := unmarshalJSONField("assignment backend", backendJSON, &b); err != nil {
			return nil, err
		}
		a.Backend = &b
	}
	return &a, nil
}
