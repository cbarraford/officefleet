package trigger

import (
	"context"
	"sync"
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

// Validate checks the cron expression is parseable.
func (c *CronTrigger) Validate() error {
	_, err := parseCron(c.Schedule)
	return err
}

// Next returns the next scheduled time after t.
func (c *CronTrigger) Next(t time.Time) (time.Time, error) {
	sched, err := parseCron(c.Schedule)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(t), nil
}

// Scheduler runs the cron trigger loop, calling fire for each due assignment.
type Scheduler struct {
	entries    []schedEntry
	workers    int
	runTimeout time.Duration
	mu         sync.Mutex
	inFlight   map[string]bool
}

const (
	defaultSchedulerWorkers = 4
	defaultRunTimeout       = 15 * time.Minute
)

type schedEntry struct {
	AssignmentID string
	trigger      *CronTrigger
	next         time.Time
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		workers:    defaultSchedulerWorkers,
		runTimeout: defaultRunTimeout,
		inFlight:   map[string]bool{},
	}
}

func (s *Scheduler) SetRunTimeout(timeout time.Duration) {
	if timeout > 0 {
		s.runTimeout = timeout
	}
}

func (s *Scheduler) SetWorkers(workers int) {
	if workers > 0 {
		s.workers = workers
	}
}

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
	workers := s.workers
	if workers <= 0 {
		workers = defaultSchedulerWorkers
	}
	sem := make(chan struct{}, workers)
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
			next, err := e.trigger.Next(e.next)
			if err == nil {
				e.next = next
				s.entries = append(s.entries, e)
			}
			s.startRun(ctx, sem, fire, e.AssignmentID)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Scheduler) startRun(ctx context.Context, sem chan struct{}, fire func(ctx context.Context, assignmentID string), assignmentID string) bool {
	s.mu.Lock()
	if s.inFlight == nil {
		s.inFlight = map[string]bool{}
	}
	if s.inFlight[assignmentID] {
		s.mu.Unlock()
		return false
	}
	select {
	case sem <- struct{}{}:
		s.inFlight[assignmentID] = true
	default:
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	go func() {
		defer func() {
			_ = recover()
			<-sem
			s.mu.Lock()
			delete(s.inFlight, assignmentID)
			s.mu.Unlock()
		}()

		runCtx := ctx
		cancel := func() {}
		if s.runTimeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, s.runTimeout)
		}
		defer cancel()
		fire(runCtx, assignmentID)
	}()
	return true
}
