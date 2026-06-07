package state

import (
	"context"
	"encoding/json"
	"sync"
)

// MemStore is an in-memory Store for tests and development.
type MemStore struct {
	mu        sync.Mutex
	kv        map[string][]byte
	notes     []json.RawMessage
	processed map[string]bool
}

func NewMemStore() *MemStore {
	return &MemStore{kv: map[string][]byte{}, processed: map[string]bool{}}
}

func (m *MemStore) Get(_ context.Context, assignmentID, key string) ([]byte, bool, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	v, ok := m.kv[assignmentID+":"+key]
	return v, ok, nil
}

func (m *MemStore) Set(_ context.Context, assignmentID, key string, val []byte) error {
	m.mu.Lock(); defer m.mu.Unlock()
	m.kv[assignmentID+":"+key] = val; return nil
}

func (m *MemStore) Delete(_ context.Context, assignmentID, key string) error {
	m.mu.Lock(); defer m.mu.Unlock()
	delete(m.kv, assignmentID+":"+key); return nil
}

func (m *MemStore) AppendNote(_ context.Context, _ string, note any) error {
	b, _ := json.Marshal(note)
	m.mu.Lock(); defer m.mu.Unlock()
	m.notes = append(m.notes, b); return nil
}

func (m *MemStore) HasProcessed(_ context.Context, assignmentID, dedupKey string) (bool, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	return m.processed[assignmentID+":"+dedupKey], nil
}

func (m *MemStore) MarkProcessed(_ context.Context, assignmentID, dedupKey string) error {
	m.mu.Lock(); defer m.mu.Unlock()
	m.processed[assignmentID+":"+dedupKey] = true; return nil
}
