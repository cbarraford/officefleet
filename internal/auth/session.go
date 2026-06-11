package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SessionTTL is the fixed session lifetime.
const SessionTTL = 7 * 24 * time.Hour

// CookieName is the session cookie's name.
const CookieName = "fleet_session"

// SessionStore persists sessions; *repo.SessionRepo satisfies it. Lookup
// returns the user id and role for an unexpired session (joining users) and
// treats expired rows as not-found.
type SessionStore interface {
	Create(ctx context.Context, tokenHash string, userID uuid.UUID, expiresAt time.Time) error
	Lookup(ctx context.Context, tokenHash string) (uuid.UUID, string, error)
	Delete(ctx context.Context, tokenHash string) error
	DeleteExpired(ctx context.Context) error
}

// Sessions issues and validates opaque tokens. The store only ever sees
// SHA-256(token): a leaked sessions table cannot be replayed.
type Sessions struct {
	store SessionStore
}

func NewSessions(store SessionStore) *Sessions { return &Sessions{store: store} }

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Start creates a session for userID and returns the raw token (cookie value).
func (s *Sessions) Start(ctx context.Context, userID uuid.UUID) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: token: %w", err)
	}
	token := hex.EncodeToString(raw)
	_ = s.store.DeleteExpired(ctx) // opportunistic cleanup
	if err := s.store.Create(ctx, hashToken(token), userID, time.Now().Add(SessionTTL)); err != nil {
		return "", fmt.Errorf("auth: create session: %w", err)
	}
	return token, nil
}

// Validate resolves a raw token to (userID, role); errors when missing/expired.
func (s *Sessions) Validate(ctx context.Context, token string) (uuid.UUID, string, error) {
	return s.store.Lookup(ctx, hashToken(token))
}

// End destroys the session for a raw token.
func (s *Sessions) End(ctx context.Context, token string) error {
	return s.store.Delete(ctx, hashToken(token))
}
