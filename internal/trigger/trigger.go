package trigger

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Params carries operator-supplied key-value parameters for a manual trigger.
type Params map[string]string

// Trigger is the interface for all trigger kinds.
// An Assignment picks one kind and supplies its config.
type Trigger interface {
	Kind() string
	// Validate checks that the trigger config is well-formed.
	Validate() error
}

// ManualTrigger fires on-demand with optional operator params.
type ManualTrigger struct {
	triggerParams Params
}

func NewManual(params Params) *ManualTrigger {
	return &ManualTrigger{triggerParams: params}
}

func (m *ManualTrigger) Kind() string    { return "manual" }
func (m *ManualTrigger) Validate() error { return nil }
func (m *ManualTrigger) Params() Params  { return m.triggerParams }

// CronTrigger fires on a schedule defined by a cron expression.
type CronTrigger struct {
	Schedule string
}

func NewCron(schedule string) *CronTrigger {
	return &CronTrigger{Schedule: schedule}
}

func (c *CronTrigger) Kind() string { return "cron" }

// Validate checks the cron expression is parseable and actually satisfiable.
func (c *CronTrigger) Validate() error {
	sched, err := parseCron(c.Schedule)
	if err != nil {
		return err
	}
	// Reject unsatisfiable schedules (e.g. Feb 31) at config time rather than
	// letting them slip through to the scheduler (issue #9).
	if _, err := sched.Next(time.Now()); err != nil {
		return err
	}
	return nil
}

// Next returns the next scheduled time after t, or an error for an
// unsatisfiable schedule.
func (c *CronTrigger) Next(t time.Time) (time.Time, error) {
	sched, err := parseCron(c.Schedule)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(t)
}

// Scheduler runs the cron trigger loop, calling fire for each due assignment.
type Scheduler struct {
	entries []schedEntry
}

type schedEntry struct {
	AssignmentID string
	trigger      *CronTrigger
	next         time.Time
}

func NewScheduler() *Scheduler { return &Scheduler{} }

func (s *Scheduler) Add(assignmentID string, t *CronTrigger, from time.Time) error {
	next, err := t.Next(from)
	if err != nil {
		return err
	}
	s.entries = append(s.entries, schedEntry{AssignmentID: assignmentID, trigger: t, next: next})
	return nil
}

// Run blocks and calls fire whenever an assignment is due. Stops when ctx is done.
func (s *Scheduler) Run(ctx context.Context, fire func(ctx context.Context, assignmentID string)) {
	for {
		now := time.Now()
		var due []schedEntry
		remaining := s.entries[:0]
		for _, e := range s.entries {
			if !e.next.After(now) {
				due = append(due, e)
			} else {
				remaining = append(remaining, e)
			}
		}
		s.entries = remaining

		for _, e := range due {
			fire(ctx, e.AssignmentID)
			next, err := e.trigger.Next(time.Now())
			if err != nil {
				// The schedule no longer matches any time; drop it rather than
				// busy-loop re-firing on the zero time (issue #9).
				fmt.Fprintf(os.Stderr, "warning: dropping assignment %s from scheduler: %v\n", e.AssignmentID, err)
				continue
			}
			e.next = next
			s.entries = append(s.entries, e)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
