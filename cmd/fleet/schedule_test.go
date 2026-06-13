package main

import (
	"context"
	"testing"
	"time"
)

type fakeSchedulerRunReconciler struct {
	affected int64
	cutoff   time.Time
	reason   string
	called   bool
}

func (f *fakeSchedulerRunReconciler) ReconcileStaleRunning(_ context.Context, cutoff time.Time, reason string) (int64, error) {
	f.called = true
	f.cutoff = cutoff
	f.reason = reason
	return f.affected, nil
}

func TestReconcileSchedulerStartupRunsMarksOldRunningAsOrphaned(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	rr := &fakeSchedulerRunReconciler{affected: 2}

	if err := reconcileSchedulerStartupRuns(context.Background(), rr, now); err != nil {
		t.Fatalf("reconcileSchedulerStartupRuns returned error: %v", err)
	}

	if !rr.called {
		t.Fatal("expected stale running reconciliation")
	}
	if rr.cutoff != now.Add(-schedulerOrphanedRunAge) {
		t.Fatalf("cutoff = %v, want %v", rr.cutoff, now.Add(-schedulerOrphanedRunAge))
	}
	if rr.reason != "orphaned by restart" {
		t.Fatalf("reason = %q, want orphaned by restart", rr.reason)
	}
}

func TestReconcileSchedulerShutdownRunsMarksInterruptedRunning(t *testing.T) {
	shutdownStarted := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	rr := &fakeSchedulerRunReconciler{affected: 1}

	if err := reconcileSchedulerShutdownRuns(context.Background(), rr, shutdownStarted); err != nil {
		t.Fatalf("reconcileSchedulerShutdownRuns returned error: %v", err)
	}

	if !rr.called {
		t.Fatal("expected shutdown reconciliation")
	}
	if rr.cutoff != shutdownStarted {
		t.Fatalf("cutoff = %v, want %v", rr.cutoff, shutdownStarted)
	}
	if rr.reason != "interrupted by shutdown" {
		t.Fatalf("reason = %q, want interrupted by shutdown", rr.reason)
	}
}
