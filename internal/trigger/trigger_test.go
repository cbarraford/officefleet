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
