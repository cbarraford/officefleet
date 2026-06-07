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
