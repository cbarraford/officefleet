package repo

import (
	"context"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AgentRepo struct{ db *pgxpool.Pool }

func NewAgentRepo(db *pgxpool.Pool) *AgentRepo { return &AgentRepo{db: db} }

func (r *AgentRepo) Insert(ctx context.Context, a *domain.Agent) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	backendJSON, err := marshalJSONField("default_backend", a.DefaultBackend)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx,
		"INSERT INTO agents (id, name, role, system_prompt, default_backend, enabled, avatar_url, hired_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)",
		a.ID, a.Name, a.Role, a.SystemPrompt, backendJSON, a.Enabled, a.AvatarURL, a.HiredAt)
	return err
}

func (r *AgentRepo) UpsertByName(ctx context.Context, a *domain.Agent) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	backendJSON, err := marshalJSONField("default_backend", a.DefaultBackend)
	if err != nil {
		return err
	}
	return r.db.QueryRow(ctx,
		`INSERT INTO agents (id, name, role, system_prompt, default_backend, enabled, avatar_url, hired_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (name) DO UPDATE SET
		   role=EXCLUDED.role,
		   system_prompt=EXCLUDED.system_prompt,
		   default_backend=EXCLUDED.default_backend,
		   enabled=EXCLUDED.enabled,
		   updated_at=NOW()
		 RETURNING id`,
		a.ID, a.Name, a.Role, a.SystemPrompt, backendJSON, a.Enabled, a.AvatarURL, a.HiredAt,
	).Scan(&a.ID)
}

func (r *AgentRepo) GetByName(ctx context.Context, name string) (*domain.Agent, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, role, system_prompt, default_backend, enabled, avatar_url, hired_at, created_at, updated_at FROM agents WHERE name=$1", name)
	return scanAgent(row)
}

func (r *AgentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, role, system_prompt, default_backend, enabled, avatar_url, hired_at, created_at, updated_at FROM agents WHERE id=$1", id)
	return scanAgent(row)
}

func (r *AgentRepo) List(ctx context.Context) ([]*domain.Agent, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, name, role, system_prompt, default_backend, enabled, avatar_url, hired_at, created_at, updated_at FROM agents ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Update persists every editable field by id (PATCH semantics live in the API
// layer: it loads, applies provided fields, then calls Update).
func (r *AgentRepo) Update(ctx context.Context, a *domain.Agent) error {
	backendJSON, err := marshalJSONField("default_backend", a.DefaultBackend)
	if err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE agents SET name=$2, role=$3, system_prompt=$4, default_backend=$5,
		   enabled=$6, avatar_url=$7, hired_at=$8, updated_at=NOW() WHERE id=$1`,
		a.ID, a.Name, a.Role, a.SystemPrompt, backendJSON, a.Enabled, a.AvatarURL, a.HiredAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("agent %s not found", a.ID)
	}
	return nil
}

// UpdateAvatarURL sets only avatar_url — the async avatar worker must not
// clobber concurrent full-row updates.
func (r *AgentRepo) UpdateAvatarURL(ctx context.Context, id uuid.UUID, avatarURL string) error {
	tag, err := r.db.Exec(ctx,
		"UPDATE agents SET avatar_url=$2, updated_at=now() WHERE id=$1", id, avatarURL)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("agent %s not found", id)
	}
	return nil
}

func (r *AgentRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM agents WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("agent %s not found", id)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAgent(s scanner) (*domain.Agent, error) {
	var a domain.Agent
	var backendJSON []byte
	if err := s.Scan(&a.ID, &a.Name, &a.Role, &a.SystemPrompt, &backendJSON, &a.Enabled,
		&a.AvatarURL, &a.HiredAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan agent: %w", err)
	}
	if err := unmarshalJSONField("agent default_backend", backendJSON, &a.DefaultBackend); err != nil {
		return nil, err
	}
	return &a, nil
}
