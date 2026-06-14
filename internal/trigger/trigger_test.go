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

func TestCronTrigger_UnsatisfiableErrors(t *testing.T) {
	// Feb 31 never occurs. Next must return an error, not the zero time — a
	// zero time reads as "always due" and fires a paid run every tick (issue #9).
	c := trigger.NewCron("0 0 31 2 *")
	from := time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC)
	if _, err := c.Next(from); err == nil {
		t.Error("Next on an unsatisfiable schedule must return an error")
	}
	if err := c.Validate(); err == nil {
		t.Error("Validate must reject an unsatisfiable schedule")
	}
}

func TestCronTrigger_RareScheduleStillValid(t *testing.T) {
	// Feb 29 is rare but valid; it must NOT be flagged unsatisfiable.
	c := trigger.NewCron("0 0 29 2 *")
	if err := c.Validate(); err != nil {
		t.Fatalf("valid leap-day schedule rejected: %v", err)
	}
	from := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	next, err := c.Next(from)
	if err != nil {
		t.Fatal(err)
	}
	if next.Year() != 2028 || next.Month() != time.February || next.Day() != 29 {
		t.Fatalf("expected 2028-02-29, got %v", next)
	}
}

func TestCronTrigger_CommaList(t *testing.T) {
	// "0 9,17 * * *" fires at 09:00 AND 17:00. The old fmt.Sscanf parse silently
	// dropped the 17:00 firing (issue #9).
	c := trigger.NewCron("0 9,17 * * *")
	if err := c.Validate(); err != nil {
		t.Fatalf("comma list rejected: %v", err)
	}
	from := time.Date(2026, 6, 7, 9, 30, 0, 0, time.UTC)
	next, err := c.Next(from)
	if err != nil {
		t.Fatal(err)
	}
	if next.Day() != 7 || next.Hour() != 17 || next.Minute() != 0 {
		t.Fatalf("expected same-day 17:00, got %v", next)
	}
}

func TestCronTrigger_DOMorDOW(t *testing.T) {
	// Standard cron: when BOTH day-of-month and day-of-week are restricted, fire
	// when EITHER matches. "0 0 13 * 5" => the 13th OR any Friday.
	c := trigger.NewCron("0 0 13 * 5")
	// From 2026-06-01 (a Monday), the next firing is Friday 2026-06-05 (a Friday
	// that is NOT the 13th) — only the OR rule produces this.
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	next, err := c.Next(from)
	if err != nil {
		t.Fatal(err)
	}
	if next.Month() != time.June || next.Day() != 5 || next.Weekday() != time.Friday {
		t.Fatalf("expected Friday 2026-06-05 (OR semantics), got %v (weekday %v)", next, next.Weekday())
	}
}

func TestCronTrigger_SixFieldsRejected(t *testing.T) {
	// A 6-field expression was silently truncated to 5, shifting field meanings.
	c := trigger.NewCron("0 0 * * * *")
	if err := c.Validate(); err == nil {
		t.Error("a 6-field cron expression must be rejected, not truncated")
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
