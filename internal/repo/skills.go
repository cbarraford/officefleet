package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SkillRepo struct{ db *pgxpool.Pool }

func NewSkillRepo(db *pgxpool.Pool) *SkillRepo { return &SkillRepo{db: db} }

func (r *SkillRepo) Insert(ctx context.Context, d *domain.Skill) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	outputActionsJSON, _ := json.Marshal(d.OutputActions)
	configSchemaJSON, _ := json.Marshal(d.ConfigSchema)
	var backendJSON []byte
	if d.Backend != nil {
		backendJSON, _ = json.Marshal(d.Backend)
	}
	_, err := r.db.Exec(ctx,
		"INSERT INTO skills (id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
		d.ID, d.Name, d.Role, d.Description, d.TriggerKinds, d.Prompt, d.RequiredTools,
		outputActionsJSON, configSchemaJSON, backendJSON)
	return err
}

func (r *SkillRepo) UpsertByName(ctx context.Context, d *domain.Skill) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	outputActionsJSON, _ := json.Marshal(d.OutputActions)
	configSchemaJSON, _ := json.Marshal(d.ConfigSchema)
	var backendJSON []byte
	if d.Backend != nil {
		backendJSON, _ = json.Marshal(d.Backend)
	}
	return r.db.QueryRow(ctx,
		`INSERT INTO skills (id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		 ON CONFLICT (name) DO UPDATE SET
		   role=EXCLUDED.role,
		   description=EXCLUDED.description,
		   trigger_kinds=EXCLUDED.trigger_kinds,
		   prompt=EXCLUDED.prompt,
		   required_tools=EXCLUDED.required_tools,
		   output_actions=EXCLUDED.output_actions,
		   config_schema=EXCLUDED.config_schema,
		   backend=EXCLUDED.backend,
		   updated_at=NOW()
		 RETURNING id`,
		d.ID, d.Name, d.Role, d.Description, d.TriggerKinds, d.Prompt, d.RequiredTools,
		outputActionsJSON, configSchemaJSON, backendJSON,
	).Scan(&d.ID)
}

func (r *SkillRepo) GetByName(ctx context.Context, name string) (*domain.Skill, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend, created_at, updated_at FROM skills WHERE name=$1", name)
	return scanSkill(row)
}

func (r *SkillRepo) List(ctx context.Context) ([]*domain.Skill, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend, created_at, updated_at FROM skills ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Skill
	for rows.Next() {
		d, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *SkillRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Skill, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend, created_at, updated_at FROM skills WHERE id=$1", id)
	return scanSkill(row)
}

func (r *SkillRepo) Update(ctx context.Context, d *domain.Skill) error {
	outputActionsJSON, _ := json.Marshal(d.OutputActions)
	configSchemaJSON, _ := json.Marshal(d.ConfigSchema)
	var backendJSON []byte
	if d.Backend != nil {
		backendJSON, _ = json.Marshal(d.Backend)
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE skills SET name=$2, role=$3, description=$4, trigger_kinds=$5, prompt=$6,
		   required_tools=$7, output_actions=$8, config_schema=$9, backend=$10, updated_at=NOW()
		 WHERE id=$1`,
		d.ID, d.Name, d.Role, d.Description, d.TriggerKinds, d.Prompt, d.RequiredTools,
		outputActionsJSON, configSchemaJSON, backendJSON)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("skill %s not found", d.ID)
	}
	return nil
}

func (r *SkillRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM skills WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("skill %s not found", id)
	}
	return nil
}

func scanSkill(s scanner) (*domain.Skill, error) {
	var d domain.Skill
	var outputActionsJSON, configSchemaJSON, backendJSON []byte
	if err := s.Scan(&d.ID, &d.Name, &d.Role, &d.Description, &d.TriggerKinds, &d.Prompt,
		&d.RequiredTools, &outputActionsJSON, &configSchemaJSON, &backendJSON,
		&d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan skill: %w", err)
	}
	if err := unmarshalJSONB(outputActionsJSON, &d.OutputActions); err != nil {
		return nil, fmt.Errorf("scan skill %s: output_actions: %w", d.ID, err)
	}
	if err := unmarshalJSONB(configSchemaJSON, &d.ConfigSchema); err != nil {
		return nil, fmt.Errorf("scan skill %s: config_schema: %w", d.ID, err)
	}
	if err := unmarshalJSONB(backendJSON, &d.Backend); err != nil {
		return nil, fmt.Errorf("scan skill %s: backend: %w", d.ID, err)
	}
	return &d, nil
}
