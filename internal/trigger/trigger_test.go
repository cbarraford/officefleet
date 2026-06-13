package trigger_test

import (
	"context"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/trigger"
)

func TestManualTrigger_Kind(t *testing.T) {
	m := trigger.NewManual(trigger.Params{"mr_iid": "42"})
	if m.Kind() != "manual" {
		t.Fatalf("expected manual, got %q", m.Kind())
	}
	if m.Params()["mr_iid"] != "42" {
		t.Fatal("params not preserved")
	}
}

func TestCronTrigger_Validate_Valid(t *testing.T) {
	c := trigger.NewCron("0 9 * * 1")
	if err := c.Validate(); err != nil {
		t.Fatalf("valid cron rejected: %v", err)
	}
}

func TestCronTrigger_Validate_Invalid(t *testing.T) {
	c := trigger.NewCron("not a cron")
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for invalid cron")
	}
}

func TestCronTrigger_Next(t *testing.T) {
	// "0 9 * * *" fires at 09:00 every day.
	c := trigger.NewCron("0 9 * * *")
	from := time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC)
	next, err := c.Next(from)
	if err != nil {
		t.Fatal(err)
	}
	if next.Hour() != 9 || next.Minute() != 0 {
		t.Fatalf("expected 09:00, got %v", next)
	}
}

func TestCronTrigger_RangeWeekdays(t *testing.T) {
	// "0 9 * * 1-5" is the sample fleet.yaml schedule: 9am Mon-Fri.
	c := trigger.NewCron("0 9 * * 1-5")
	if err := c.Validate(); err != nil {
		t.Fatalf("range expression rejected: %v", err)
	}
	// Saturday (weekday 6) should not match; advance from Friday 9am to find next firing.
	// from = Friday 2026-06-05 09:01 — next should be Monday 2026-06-08 09:00.
	from := time.Date(2026, 6, 5, 9, 1, 0, 0, time.UTC)
	next, err := c.Next(from)
	if err != nil {
		t.Fatal(err)
	}
	if int(next.Weekday()) != 1 || next.Hour() != 9 || next.Minute() != 0 {
		t.Fatalf("expected Monday 09:00, got %v (weekday %d)", next, next.Weekday())
	}
}

func TestCronTrigger_StepExpression(t *testing.T) {
	// "*/15 * * * *" fires every 15 minutes.
	c := trigger.NewCron("*/15 * * * *")
	if err := c.Validate(); err != nil {
		t.Fatalf("step expression rejected: %v", err)
	}
	from := time.Date(2026, 6, 7, 8, 1, 0, 0, time.UTC)
	next, err := c.Next(from)
	if err != nil {
		t.Fatal(err)
	}
	if next.Minute() != 15 {
		t.Fatalf("expected minute 15, got %d", next.Minute())
	}
}

func TestScheduler_FiresDueAssignments(t *testing.T) {
	sched := trigger.NewScheduler()
	c := trigger.NewCron("* * * * *") // every minute
	// Set next to a time in the past so it fires immediately.
	past := time.Now().Add(-2 * time.Minute)
	if err := sched.Add("assignment-1", c, past); err != nil {
		t.Fatal(err)
	}
	fired := make(chan string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go sched.Run(ctx, func(_ context.Context, id string) {
		fired <- id
	})
	select {
	case id := <-fired:
		if id != "assignment-1" {
			t.Fatalf("unexpected assignment: %q", id)
		}
	case <-ctx.Done():
		t.Fatal("scheduler did not fire within timeout")
	}
}

func TestScheduler_DoesNotBlockOnSlowFire(t *testing.T) {
	sched := trigger.NewScheduler()
	c := trigger.NewCron("* * * * *")
	past := time.Now().Add(-2 * time.Minute)
	if err := sched.Add("slow", c, past); err != nil {
		t.Fatal(err)
	}
	if err := sched.Add("fast", c, past); err != nil {
		t.Fatal(err)
	}

	releaseSlow := make(chan struct{})
	fastFired := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go sched.Run(ctx, func(_ context.Context, id string) {
		if id == "slow" {
			<-releaseSlow
			return
		}
		if id == "fast" {
			fastFired <- struct{}{}
		}
	})
	defer close(releaseSlow)

	select {
	case <-fastFired:
	case <-ctx.Done():
		t.Fatal("fast assignment did not fire while slow assignment was blocked")
	}
}

func TestScheduler_RecoversPanics(t *testing.T) {
	sched := trigger.NewScheduler()
	c := trigger.NewCron("* * * * *")
	past := time.Now().Add(-2 * time.Minute)
	if err := sched.Add("panic", c, past); err != nil {
		t.Fatal(err)
	}
	if err := sched.Add("ok", c, past); err != nil {
		t.Fatal(err)
	}

	okFired := make(chan struct{}, 1)
	panicSeen := make(chan any, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go func() {
		defer func() { panicSeen <- recover() }()
		sched.Run(ctx, func(_ context.Context, id string) {
			if id == "panic" {
				panic("boom")
			}
			if id == "ok" {
				okFired <- struct{}{}
			}
		})
	}()

	select {
	case p := <-panicSeen:
		if p != nil {
			t.Fatalf("scheduler let panic escape: %v", p)
		}
	case <-okFired:
	case <-ctx.Done():
		t.Fatal("scheduler did not continue after panicking assignment")
	}
}

func TestScheduler_RunTimeoutContext(t *testing.T) {
	sched := trigger.NewScheduler()
	sched.SetRunTimeout(50 * time.Millisecond)
	c := trigger.NewCron("* * * * *")
	if err := sched.Add("assignment-1", c, time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	deadlineSeen := make(chan time.Duration, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go sched.Run(ctx, func(runCtx context.Context, _ string) {
		deadline, ok := runCtx.Deadline()
		if !ok {
			deadlineSeen <- 0
			return
		}
		deadlineSeen <- time.Until(deadline)
	})

	select {
	case remaining := <-deadlineSeen:
		if remaining <= 0 || remaining > 100*time.Millisecond {
			t.Fatalf("run timeout deadline = %s, want about 50ms", remaining)
		}
	case <-ctx.Done():
		t.Fatal("scheduler did not fire")
	}
}
