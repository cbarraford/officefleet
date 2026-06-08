package state_test

import (
	"context"
	"testing"

	"github.com/cbarraford/office-fleet/internal/state"
)

var _ state.Store = (*state.MemStore)(nil)

func TestMemStore_SetGet(t *testing.T) {
	ctx := context.Background()
	s := state.NewMemStore()
	if err := s.Set(ctx, "a1", "k", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Get(ctx, "a1", "k")
	if err != nil || !ok || string(v) != "hello" {
		t.Fatalf("got %q ok=%v err=%v", v, ok, err)
	}
}

func TestMemStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := state.NewMemStore()
	_ = s.Set(ctx, "a1", "k", []byte("v"))
	_ = s.Delete(ctx, "a1", "k")
	_, ok, _ := s.Get(ctx, "a1", "k")
	if ok {
		t.Fatal("should be deleted")
	}
}

func TestMemStore_HasProcessed(t *testing.T) {
	ctx := context.Background()
	s := state.NewMemStore()
	ok, _ := s.HasProcessed(ctx, "a1", "sha-abc")
	if ok {
		t.Fatal("should not be processed yet")
	}
	_ = s.MarkProcessed(ctx, "a1", "sha-abc")
	ok, _ = s.HasProcessed(ctx, "a1", "sha-abc")
	if !ok {
		t.Fatal("should be processed after MarkProcessed")
	}
}

func TestMemStore_TwoAssignmentsSameKey(t *testing.T) {
	ctx := context.Background()
	s := state.NewMemStore()
	_ = s.Set(ctx, "agent-1", "cursor", []byte("100"))
	_ = s.Set(ctx, "agent-2", "cursor", []byte("200"))
	v1, _, _ := s.Get(ctx, "agent-1", "cursor")
	v2, _, _ := s.Get(ctx, "agent-2", "cursor")
	if string(v1) != "100" || string(v2) != "200" {
		t.Fatalf("state leakage: v1=%q v2=%q", v1, v2)
	}
}

func TestMemStore_ProcessedIsolation(t *testing.T) {
	ctx := context.Background()
	s := state.NewMemStore()
	_ = s.MarkProcessed(ctx, "a1", "sha-abc")
	ok, err := s.HasProcessed(ctx, "a2", "sha-abc")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("MarkProcessed on a1 must not affect HasProcessed on a2")
	}
}

func TestMemStore_AppendNote(t *testing.T) {
	ctx := context.Background()
	s := state.NewMemStore()

	// AppendNote on two different assignmentIDs must not panic and must not error.
	if err := s.AppendNote(ctx, "assign-1", map[string]string{"msg": "hello"}); err != nil {
		t.Fatalf("AppendNote assign-1: %v", err)
	}
	if err := s.AppendNote(ctx, "assign-2", map[string]string{"msg": "world"}); err != nil {
		t.Fatalf("AppendNote assign-2: %v", err)
	}

	// The two assignments have independent KV state — a Get on one does not
	// affect the other.
	_ = s.Set(ctx, "assign-1", "k", []byte("v1"))
	_ = s.Set(ctx, "assign-2", "k", []byte("v2"))

	v1, ok1, err1 := s.Get(ctx, "assign-1", "k")
	if err1 != nil || !ok1 || string(v1) != "v1" {
		t.Fatalf("assign-1 state: got %q ok=%v err=%v", v1, ok1, err1)
	}

	v2, ok2, err2 := s.Get(ctx, "assign-2", "k")
	if err2 != nil || !ok2 || string(v2) != "v2" {
		t.Fatalf("assign-2 state: got %q ok=%v err=%v", v2, ok2, err2)
	}

	// Verify that two AppendNote calls on assign-1 accumulate two entries.
	if err := s.AppendNote(ctx, "assign-1", map[string]string{"msg": "second"}); err != nil {
		t.Fatalf("AppendNote assign-1 second: %v", err)
	}
	if got := s.NoteCount("assign-1"); got != 2 {
		t.Fatalf("assign-1 note count: want 2, got %d", got)
	}

	// Verify that notes from assign-1 do not appear under assign-2.
	if got := s.NoteCount("assign-2"); got != 1 {
		t.Fatalf("assign-2 note count: want 1, got %d", got)
	}
}
