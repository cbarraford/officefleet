package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store over a pgx pool.
type PostgresStore struct{ db *pgxpool.Pool }

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Get(ctx context.Context, assignmentID, key string) ([]byte, bool, error) {
	var val []byte
	err := s.db.QueryRow(ctx, "SELECT value FROM assignment_state WHERE assignment_id=$1 AND key=$2",
		assignmentID, key).Scan(&val)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return val, err == nil, err
}

func (s *PostgresStore) Set(ctx context.Context, assignmentID, key string, val []byte) error {
	_, err := s.db.Exec(ctx,
		"INSERT INTO assignment_state (assignment_id, key, value, updated_at) VALUES ($1,$2,$3,NOW()) ON CONFLICT (assignment_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()",
		assignmentID, key, val)
	return err
}

func (s *PostgresStore) Delete(ctx context.Context, assignmentID, key string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM assignment_state WHERE assignment_id=$1 AND key=$2",
		assignmentID, key)
	return err
}

func (s *PostgresStore) List(ctx context.Context, assignmentID string) (map[string][]byte, error) {
	rows, err := s.db.Query(ctx, "SELECT key, value FROM assignment_state WHERE assignment_id=$1", assignmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var key string
		var val []byte
		if err := rows.Scan(&key, &val); err != nil {
			return nil, err
		}
		out[key] = val
	}
	return out, rows.Err()
}

func (s *PostgresStore) AppendNote(ctx context.Context, assignmentID string, note any) error {
	noteJSON, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("marshal note: %w", err)
	}
	_, err = s.db.Exec(ctx, "INSERT INTO assignment_notes (assignment_id, note) VALUES ($1,$2)",
		assignmentID, noteJSON)
	return err
}

func (s *PostgresStore) HasProcessed(ctx context.Context, assignmentID, dedupKey string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx,
		"SELECT TRUE FROM assignment_processed WHERE assignment_id=$1 AND dedup_key=$2",
		assignmentID, dedupKey).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return exists, err
}

func (s *PostgresStore) MarkProcessed(ctx context.Context, assignmentID, dedupKey string) error {
	_, err := s.db.Exec(ctx,
		"INSERT INTO assignment_processed (assignment_id, dedup_key) VALUES ($1,$2) ON CONFLICT (assignment_id, dedup_key) DO NOTHING",
		assignmentID, dedupKey)
	return err
}

func (s *PostgresStore) ClaimProcessed(ctx context.Context, assignmentID, dedupKey string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		"INSERT INTO assignment_processed (assignment_id, dedup_key) VALUES ($1,$2) ON CONFLICT (assignment_id, dedup_key) DO NOTHING",
		assignmentID, dedupKey)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresStore) DeleteProcessed(ctx context.Context, assignmentID, dedupKey string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM assignment_processed WHERE assignment_id=$1 AND dedup_key=$2",
		assignmentID, dedupKey)
	return err
}
