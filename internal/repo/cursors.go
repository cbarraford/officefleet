package repo

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CursorRepo struct{ db *pgxpool.Pool }

func NewCursorRepo(db *pgxpool.Pool) *CursorRepo { return &CursorRepo{db: db} }

// Get returns the stored cursor for a plugin, or "" when none exists.
func (r *CursorRepo) Get(ctx context.Context, plugin string) (string, error) {
	var cursor string
	err := r.db.QueryRow(ctx, "SELECT cursor FROM poll_cursors WHERE plugin=$1", plugin).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return cursor, err
}

func (r *CursorRepo) Set(ctx context.Context, plugin, cursor string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO poll_cursors (plugin, cursor, updated_at) VALUES ($1,$2,NOW())
		 ON CONFLICT (plugin) DO UPDATE SET cursor=EXCLUDED.cursor, updated_at=NOW()`,
		plugin, cursor)
	return err
}
