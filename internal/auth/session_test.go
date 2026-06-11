package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/google/uuid"
)

// memSessionStore is an in-memory SessionStore for tests.
type memSessionStore struct {
	rows map[string]memSession
}

type memSession struct {
	userID  uuid.UUID
	role    string
	expires time.Time
}

func newMemSessionStore() *memSessionStore { return &memSessionStore{rows: map[string]memSession{}} }

func (m *memSessionStore) Create(_ context.Context, tokenHash string, userID uuid.UUID, expiresAt time.Time) error {
	m.rows[tokenHash] = memSession{userID: userID, role: domain.RoleAdmin, expires: expiresAt}
	return nil
}

func (m *memSessionStore) Lookup(_ context.Context, tokenHash string) (uuid.UUID, string, error) {
	s, ok := m.rows[tokenHash]
	if !ok || time.Now().After(s.expires) {
		delete(m.rows, tokenHash)
		return uuid.Nil, "", fmt.Errorf("session not found")
	}
	return s.userID, s.role, nil
}

func (m *memSessionStore) Delete(_ context.Context, tokenHash string) error {
	delete(m.rows, tokenHash)
	return nil
}

func (m *memSessionStore) DeleteExpired(_ context.Context) error { return nil }

func TestSessionLifecycle(t *testing.T) {
	store := newMemSessionStore()
	svc := NewSessions(store)
	userID := uuid.New()

	token, err := svc.Start(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 32 {
		t.Errorf("token too short: %d chars", len(token))
	}
	// The raw token must NOT be a store key (only its hash is stored).
	if _, ok := store.rows[token]; ok {
		t.Error("raw token stored; only the hash may be persisted")
	}

	gotID, role, err := svc.Validate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != userID || role != domain.RoleAdmin {
		t.Errorf("validate = %v/%q", gotID, role)
	}

	if err := svc.End(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Validate(context.Background(), token); err == nil {
		t.Error("validated after End")
	}
}

func TestValidateUnknownToken(t *testing.T) {
	svc := NewSessions(newMemSessionStore())
	if _, _, err := svc.Validate(context.Background(), "no-such-token"); err == nil {
		t.Error("unknown token validated")
	}
}
