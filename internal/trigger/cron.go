package trigger

import (
	"fmt"
	"time"
)

// cronSchedule wraps a parsed cron expression.
type cronSchedule struct {
	fields [5]cronField // min hour dom mon dow
}

type cronField struct {
	values []int
	star   bool
}

func (s *cronSchedule) Next(t time.Time) time.Time {
	// Advance by at least one minute.
	t = t.Add(time.Minute).Truncate(time.Minute)
	// Try up to 366*24*60 minutes to find a match (handles any valid cron).
	for i := 0; i < 366*24*60; i++ {
		if s.matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

func (s *cronSchedule) matches(t time.Time) bool {
	return s.fieldMatches(s.fields[0], t.Minute()) &&
		s.fieldMatches(s.fields[1], t.Hour()) &&
		s.fieldMatches(s.fields[2], t.Day()) &&
		s.fieldMatches(s.fields[3], int(t.Month())) &&
		s.fieldMatches(s.fields[4], int(t.Weekday()))
}

func (s *cronSchedule) fieldMatches(f cronField, v int) bool {
	if f.star {
		return true
	}
	for _, x := range f.values {
		if x == v {
			return true
		}
	}
	return false
}

func parseCron(expr string) (*cronSchedule, error) {
	var fields [5]string
	n, _ := fmt.Sscanf(expr, "%s %s %s %s %s", &fields[0], &fields[1], &fields[2], &fields[3], &fields[4])
	if n != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d: %q", n, expr)
	}

	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	sched := &cronSchedule{}
	for i, f := range fields {
		cf, err := parseCronField(f, bounds[i][0], bounds[i][1])
		if err != nil {
			return nil, fmt.Errorf("field %d: %w", i, err)
		}
		sched.fields[i] = cf
	}
	return sched, nil
}

func parseCronField(s string, min, max int) (cronField, error) {
	if s == "*" {
		return cronField{star: true}, nil
	}
	// Simple single-value parsing; step/range not needed for SP1 acceptance.
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return cronField{}, fmt.Errorf("unsupported field value %q", s)
	}
	if v < min || v > max {
		return cronField{}, fmt.Errorf("value %d out of range [%d,%d]", v, min, max)
	}
	return cronField{values: []int{v}}, nil
}
