package repo

import (
	"context"
	"encoding/json"
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
	backendJSON, _ := json.Marshal(a.DefaultBackend)
	_, err := r.db.Exec(ctx,
		"INSERT INTO agents (id, name, role, system_prompt, default_backend, enabled) VALUES ($1,$2,$3,$4,$5,$6)",
		a.ID, a.Name, a.Role, a.SystemPrompt, backendJSON, a.Enabled)
	return err
}

func (r *AgentRepo) UpsertByName(ctx context.Context, a *domain.Agent) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	backendJSON, _ := json.Marshal(a.DefaultBackend)
	return r.db.QueryRow(ctx,
		`INSERT INTO agents (id, name, role, system_prompt, default_backend, enabled)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (name) DO UPDATE SET
		   role=EXCLUDED.role,
		   system_prompt=EXCLUDED.system_prompt,
		   default_backend=EXCLUDED.default_backend,
		   enabled=EXCLUDED.enabled,
		   updated_at=NOW()
		 RETURNING id`,
		a.ID, a.Name, a.Role, a.SystemPrompt, backendJSON, a.Enabled,
	).Scan(&a.ID)
}

func (r *AgentRepo) GetByName(ctx context.Context, name string) (*domain.Agent, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, name, role, system_prompt, default_backend, enabled, created_at, updated_at FROM agents WHERE name=$1", name)
	return scanAgent(row)
}

func (r *AgentRepo) List(ctx context.Context) ([]*domain.Agent, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, name, role, system_prompt, default_backend, enabled, created_at, updated_at FROM agents ORDER BY name")
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

type scanner interface {
	Scan(dest ...any) error
}

func scanAgent(s scanner) (*domain.Agent, error) {
	var a domain.Agent
	var backendJSON []byte
	if err := s.Scan(&a.ID, &a.Name, &a.Role, &a.SystemPrompt, &backendJSON, &a.Enabled, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan agent: %w", err)
	}
	_ = json.Unmarshal(backendJSON, &a.DefaultBackend)
	return &a, nil
}
