package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SecretRepo struct{ db *pgxpool.Pool }

func NewSecretRepo(db *pgxpool.Pool) *SecretRepo { return &SecretRepo{db: db} }

func (r *SecretRepo) Upsert(ctx context.Context, name string, value []byte) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO secrets (name, encrypted_value) VALUES ($1,$2)
		 ON CONFLICT (name) DO UPDATE SET encrypted_value=EXCLUDED.encrypted_value, updated_at=NOW()`,
		name, value)
	return err
}

func (r *SecretRepo) Get(ctx context.Context, name string) ([]byte, error) {
	var v []byte
	err := r.db.QueryRow(ctx, "SELECT encrypted_value FROM secrets WHERE name=$1", name).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("secret %q not found", name)
	}
	return v, err
}

// SecretInfo is a value-free listing entry.
type SecretInfo struct {
	Name      string `json:"name"`
	Encrypted bool   `json:"encrypted"`
}

// List returns names + raw values (the caller derives encrypted-status and
// MUST NOT expose the values).
func (r *SecretRepo) List(ctx context.Context) (map[string][]byte, error) {
	rows, err := r.db.Query(ctx, "SELECT name, encrypted_value FROM secrets ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var name string
		var v []byte
		if err := rows.Scan(&name, &v); err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, rows.Err()
}

func (r *SecretRepo) Delete(ctx context.Context, name string) error {
	tag, err := r.db.Exec(ctx, "DELETE FROM secrets WHERE name=$1", name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("secret %q not found", name)
	}
	return nil
}
