package state

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// MemStore is an in-memory Store for tests and development.
type MemStore struct {
	mu        sync.Mutex
	kv        map[string][]byte
	notes     map[string][]json.RawMessage
	processed map[string]map[string]bool
}

func NewMemStore() *MemStore {
	return &MemStore{kv: map[string][]byte{}, notes: map[string][]json.RawMessage{}, processed: map[string]map[string]bool{}}
}

func (m *MemStore) Get(_ context.Context, assignmentID, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.kv[assignmentID+":"+key]
	return v, ok, nil
}

func (m *MemStore) Set(_ context.Context, assignmentID, key string, val []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kv[assignmentID+":"+key] = val
	return nil
}

func (m *MemStore) Delete(_ context.Context, assignmentID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.kv, assignmentID+":"+key)
	return nil
}

func (m *MemStore) AppendNote(_ context.Context, assignmentID string, note any) error {
	b, _ := json.Marshal(note)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notes[assignmentID] = append(m.notes[assignmentID], b)
	return nil
}

func (m *MemStore) List(_ context.Context, assignmentID string) (map[string][]byte, error) {
	prefix := assignmentID + ":"
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]byte{}
	for k, v := range m.kv {
		if strings.HasPrefix(k, prefix) {
			key := k[len(prefix):]
			out[key] = v
		}
	}
	return out, nil
}

func (m *MemStore) HasProcessed(_ context.Context, assignmentID, dedupKey string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.processed[assignmentID][dedupKey], nil
}

func (m *MemStore) MarkProcessed(_ context.Context, assignmentID, dedupKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.processed[assignmentID] == nil {
		m.processed[assignmentID] = map[string]bool{}
	}
	m.processed[assignmentID][dedupKey] = true
	return nil
}

func (m *MemStore) ClaimProcessed(_ context.Context, assignmentID, dedupKey string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.processed[assignmentID] == nil {
		m.processed[assignmentID] = map[string]bool{}
	}
	if m.processed[assignmentID][dedupKey] {
		return false, nil
	}
	m.processed[assignmentID][dedupKey] = true
	return true, nil
}

func (m *MemStore) DeleteProcessed(_ context.Context, assignmentID, dedupKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.processed[assignmentID] != nil {
		delete(m.processed[assignmentID], dedupKey)
	}
	return nil
}

// NoteCount returns the number of notes stored for the given assignmentID.
// It is provided for testing purposes.
func (m *MemStore) NoteCount(assignmentID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.notes[assignmentID])
}
