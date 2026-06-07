package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RunRepo struct{ db *pgxpool.Pool }

func NewRunRepo(db *pgxpool.Pool) *RunRepo { return &RunRepo{db: db} }

func (r *RunRepo) Insert(ctx context.Context, run *domain.Run) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	outputsJSON, _ := json.Marshal(run.OutputsDelivered)
	_, err := r.db.Exec(ctx,
		"INSERT INTO runs (id, assignment_id, agent_id, duty_id, trigger_kind, event_id, rendered_system_prompt, rendered_prompt, outputs_delivered, status, started_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)",
		run.ID, run.AssignmentID, run.AgentID, run.DutyID, run.TriggerKind, run.EventID,
		run.RenderedSystemPrompt, run.RenderedPrompt, outputsJSON, run.Status, run.StartedAt)
	return err
}

func (r *RunRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.RunStatus, errMsg *string) error {
	_, err := r.db.Exec(ctx,
		"UPDATE runs SET status=$1, error=$2, finished_at=NOW() WHERE id=$3",
		status, errMsg, id)
	return err
}

func (r *RunRepo) UpdateResult(ctx context.Context, id uuid.UUID, result *domain.LLMResult, outputs []domain.OutputDelivery, status domain.RunStatus) error {
	resultJSON, _ := json.Marshal(result)
	outputsJSON, _ := json.Marshal(outputs)
	var tokens int
	var cost float64
	if result != nil {
		tokens = result.Tokens
		cost = result.Cost
	}
	_, err := r.db.Exec(ctx,
		"UPDATE runs SET llm_result=$1, outputs_delivered=$2, status=$3, tokens=$4, cost=$5, finished_at=NOW() WHERE id=$6",
		resultJSON, outputsJSON, status, tokens, cost, id)
	return err
}

func (r *RunRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Run, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, assignment_id, agent_id, duty_id, trigger_kind, event_id, rendered_system_prompt, rendered_prompt, llm_result, outputs_delivered, status, tokens, cost, started_at, finished_at, error FROM runs WHERE id=$1", id)
	return scanRun(row)
}

func scanRun(s scanner) (*domain.Run, error) {
	var run domain.Run
	var llmResultJSON, outputsJSON []byte
	if err := s.Scan(&run.ID, &run.AssignmentID, &run.AgentID, &run.DutyID,
		&run.TriggerKind, &run.EventID, &run.RenderedSystemPrompt, &run.RenderedPrompt,
		&llmResultJSON, &outputsJSON, &run.Status, &run.Tokens, &run.Cost,
		&run.StartedAt, &run.FinishedAt, &run.Error); err != nil {
		return nil, fmt.Errorf("scan run: %w", err)
	}
	if len(llmResultJSON) > 0 {
		var r domain.LLMResult
		_ = json.Unmarshal(llmResultJSON, &r)
		run.LLMResult = &r
	}
	_ = json.Unmarshal(outputsJSON, &run.OutputsDelivered)
	return &run, nil
}
