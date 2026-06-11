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
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal llm_result: %w", err)
	}
	outputsJSON, err := json.Marshal(outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs_delivered: %w", err)
	}
	var tokens int
	var cost float64
	if result != nil {
		tokens = result.Tokens
		cost = result.Cost
	}
	_, err = r.db.Exec(ctx,
		"UPDATE runs SET llm_result=$1, outputs_delivered=$2, status=$3, tokens=$4, cost=$5, finished_at=NOW() WHERE id=$6",
		resultJSON, outputsJSON, status, tokens, cost, id)
	return err
}

func (r *RunRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Run, error) {
	row := r.db.QueryRow(ctx,
		"SELECT id, assignment_id, agent_id, duty_id, trigger_kind, event_id, rendered_system_prompt, rendered_prompt, llm_result, outputs_delivered, status, tokens, cost, started_at, finished_at, error FROM runs WHERE id=$1", id)
	return scanRun(row)
}

// ListFiltered returns run summaries newest-first. status/agentID filter when
// non-zero. Summaries exclude llm_result (transcripts can be large).
func (r *RunRepo) ListFiltered(ctx context.Context, status string, agentID uuid.UUID, limit int) ([]*domain.Run, error) {
	q := `SELECT id, assignment_id, agent_id, duty_id, trigger_kind, event_id,
	        '' AS rendered_system_prompt, '' AS rendered_prompt, NULL AS llm_result,
	        outputs_delivered, status, tokens, cost, started_at, finished_at, error
	      FROM runs WHERE 1=1`
	args := []any{}
	n := 1
	if status != "" {
		q += fmt.Sprintf(" AND status=$%d", n)
		args = append(args, status)
		n++
	}
	if agentID != uuid.Nil {
		q += fmt.Sprintf(" AND agent_id=$%d", n)
		args = append(args, agentID)
		n++
	}
	q += fmt.Sprintf(" ORDER BY started_at DESC LIMIT $%d", n)
	args = append(args, limit)

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// AgentStats computes the spec.md §6 derived metrics for one agent.
func (r *RunRepo) AgentStats(ctx context.Context, agentID uuid.UUID) (*domain.AgentStats, error) {
	st := &domain.AgentStats{AgentID: agentID}
	var ok30, fail30, skip30, total30 int
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE started_at > NOW() - INTERVAL '30 days'),
		       COUNT(*) FILTER (WHERE status='succeeded' AND started_at > NOW() - INTERVAL '30 days'),
		       COUNT(*) FILTER (WHERE status='failed'    AND started_at > NOW() - INTERVAL '30 days'),
		       COUNT(*) FILTER (WHERE status='skipped'   AND started_at > NOW() - INTERVAL '30 days'),
		       COALESCE(SUM(tokens),0), COALESCE(SUM(cost),0),
		       COALESCE(SUM(tokens) FILTER (WHERE started_at > NOW() - INTERVAL '30 days'),0),
		       COALESCE(SUM(cost)   FILTER (WHERE started_at > NOW() - INTERVAL '30 days'),0),
		       COALESCE(AVG(EXTRACT(EPOCH FROM (finished_at - started_at))) FILTER (WHERE finished_at IS NOT NULL),0),
		       MAX(started_at)
		FROM runs WHERE agent_id=$1`, agentID).Scan(
		&st.TotalRuns, &total30, &ok30, &fail30, &skip30,
		&st.TotalTokens, &st.TotalCostUSD, &st.TokensLast30d, &st.CostLast30dUSD,
		&st.AvgRunDurationS, &st.LastRunAt)
	if err != nil {
		return nil, fmt.Errorf("agent stats: %w", err)
	}
	st.RunsLast30d = total30
	if ok30+fail30 > 0 {
		st.SuccessRate = float64(ok30) / float64(ok30+fail30)
	}
	if total30 > 0 {
		st.SkipRate = float64(skip30) / float64(total30)
	}
	// Guard: outputs_delivered may hold JSON null for old rows;
	// jsonb_array_elements(null) errors, so skip non-array values.
	err = r.db.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE r.started_at > NOW() - INTERVAL '30 days')
		FROM runs r, jsonb_array_elements(r.outputs_delivered) o
		WHERE r.agent_id=$1 AND jsonb_typeof(r.outputs_delivered)='array'
		  AND o->>'status'='delivered'`, agentID).Scan(
		&st.OutputsDelivered, &st.OutputsLast30d)
	if err != nil {
		return nil, fmt.Errorf("agent output stats: %w", err)
	}
	return st, nil
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
