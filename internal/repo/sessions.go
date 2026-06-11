package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time assertion: SessionRepo must satisfy auth.SessionStore.
var _ auth.SessionStore = (*SessionRepo)(nil)

type SessionRepo struct{ db *pgxpool.Pool }

func NewSessionRepo(db *pgxpool.Pool) *SessionRepo { return &SessionRepo{db: db} }

func (r *SessionRepo) Create(ctx context.Context, tokenHash string, userID uuid.UUID, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx,
		"INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1,$2,$3)",
		tokenHash, userID, expiresAt)
	return err
}

// Lookup resolves an unexpired session to (userID, role), lazily deleting
// expired rows it encounters.
func (r *SessionRepo) Lookup(ctx context.Context, tokenHash string) (uuid.UUID, string, error) {
	var userID uuid.UUID
	var role string
	var expiresAt time.Time
	err := r.db.QueryRow(ctx,
		`SELECT s.user_id, u.role, s.expires_at FROM sessions s
		 JOIN users u ON u.id = s.user_id WHERE s.token_hash=$1`,
		tokenHash).Scan(&userID, &role, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", fmt.Errorf("session not found")
	}
	if err != nil {
		return uuid.Nil, "", err
	}
	if time.Now().After(expiresAt) {
		_, _ = r.db.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", tokenHash)
		return uuid.Nil, "", fmt.Errorf("session expired")
	}
	return userID, role, nil
}

func (r *SessionRepo) Delete(ctx context.Context, tokenHash string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", tokenHash)
	return err
}

func (r *SessionRepo) DeleteExpired(ctx context.Context) error {
	_, err := r.db.Exec(ctx, "DELETE FROM sessions WHERE expires_at < NOW()")
	return err
}
