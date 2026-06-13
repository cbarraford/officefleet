package repo

import (
	"context"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DutyRepo struct{ db *pgxpool.Pool }

func NewDutyRepo(db *pgxpool.Pool) *DutyRepo { return &DutyRepo{db: db} }

func (r *DutyRepo) Insert(ctx context.Context, d *domain.Duty) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	outputActionsJSON, configSchemaJSON, backendJSON, err := marshalDuty(d)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx,
		"INSERT INTO duties (id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
		d.ID, d.Name, d.Role, d.Description, d.TriggerKinds, d.Prompt, d.RequiredTools,
		outputActionsJSON, configSchemaJSON, backendJSON)
	return err
}

func marshalDuty(d *domain.Duty) ([]byte, []byte, []byte, error) {
	outputActionsJSON, err := marshalJSONField("duty output_actions", d.OutputActions)
	if err != nil {
		return nil, nil, nil, err
	}
	configSchemaJSON, err := marshalJSONField("duty config_schema", d.ConfigSchema)
	if err != nil {
		return nil, nil, nil, err
	}
	var backendJSON []byte
	if d.Backend != nil {
		backendJSON, err = marshalJSONField("duty backend", d.Backend)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return outputActionsJSON, configSchemaJSON, backendJSON, nil
}

func (r *DutyRepo) UpsertByName(ctx context.Context, d *domain.Duty) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	outputActionsJSON, configSchemaJSON, backendJSON, err := marshalDuty(d)
	if err != nil {
		return err
	}
	return r.db.QueryRow(ctx,
		`INSERT INTO duties (id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend)
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

func (r *DutyRepo) GetByName(ctx context.Context, name string) (*domain.Duty, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend, created_at, updated_at FROM duties WHERE name=$1", name)
	return scanDuty(row)
}

func (r *DutyRepo) List(ctx context.Context) ([]*domain.Duty, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend, created_at, updated_at FROM duties ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Duty
	for rows.Next() {
		d, err := scanDuty(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *DutyRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Duty, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, role, description, trigger_kinds, prompt, required_tools, output_actions, config_schema, backend, created_at, updated_at FROM duties WHERE id=$1", id)
	return scanDuty(row)
}

func (r *DutyRepo) Update(ctx context.Context, d *domain.Duty) error {
	outputActionsJSON, configSchemaJSON, backendJSON, err := marshalDuty(d)
	if err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE duties SET name=$2, role=$3, description=$4, trigger_kinds=$5, prompt=$6,
		   required_tools=$7, output_actions=$8, config_schema=$9, backend=$10, updated_at=NOW()
		 WHERE id=$1`,
		d.ID, d.Name, d.Role, d.Description, d.TriggerKinds, d.Prompt, d.RequiredTools,
		outputActionsJSON, configSchemaJSON, backendJSON)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("duty %s not found", d.ID)
	}
	return nil
}

func (r *DutyRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM duties WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("duty %s not found", id)
	}
	return nil
}

func scanDuty(s scanner) (*domain.Duty, error) {
	var d domain.Duty
	var outputActionsJSON, configSchemaJSON, backendJSON []byte
	if err := s.Scan(&d.ID, &d.Name, &d.Role, &d.Description, &d.TriggerKinds, &d.Prompt,
		&d.RequiredTools, &outputActionsJSON, &configSchemaJSON, &backendJSON,
		&d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan duty: %w", err)
	}
	if err := unmarshalJSONField("duty output_actions", outputActionsJSON, &d.OutputActions); err != nil {
		return nil, err
	}
	if err := unmarshalJSONField("duty config_schema", configSchemaJSON, &d.ConfigSchema); err != nil {
		return nil, err
	}
	if !isJSONNull(backendJSON) {
		var b domain.BackendRef
		if err := unmarshalJSONField("duty backend", backendJSON, &b); err != nil {
			return nil, err
		}
		d.Backend = &b
	}
	return &d, nil
}
