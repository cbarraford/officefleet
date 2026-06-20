package trigger

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Fires repeatedly until ctx is cancelled, and stops promptly afterward.
func TestRunContinuous_FiresUntilCancelled(t *testing.T) {
	var n int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunContinuous(ctx, time.Millisecond, func(context.Context) { atomic.AddInt32(&n, 1) })
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunContinuous did not return after cancel")
	}
	if atomic.LoadInt32(&n) < 2 {
		t.Fatalf("expected multiple fires, got %d", n)
	}
}

// A cancelled context fires zero times (guard runs before the first fire).
func TestRunContinuous_NoFireWhenAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var n int32
	RunContinuous(ctx, time.Hour, func(context.Context) { atomic.AddInt32(&n, 1) })
	if n != 0 {
		t.Fatalf("expected 0 fires on cancelled ctx, got %d", n)
	}
}
