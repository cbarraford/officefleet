package trigger

import (
	"fmt"
	"strconv"
	"strings"
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

func (s *cronSchedule) Next(t time.Time) (time.Time, error) {
	// Advance by at least one minute.
	t = t.Add(time.Minute).Truncate(time.Minute)
	// Try up to five years to cover valid leap-day schedules while still
	// surfacing impossible expressions such as February 31.
	for i := 0; i < 5*366*24*60; i++ {
		if s.matches(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron schedule has no matching time in the next 5 years")
}

func (s *cronSchedule) matches(t time.Time) bool {
	dom := s.fields[2]
	dow := s.fields[4]
	domMatches := s.fieldMatches(dom, t.Day())
	dowMatches := s.fieldMatches(dow, int(t.Weekday()))
	dayMatches := domMatches && dowMatches
	if !dom.star && !dow.star {
		dayMatches = domMatches || dowMatches
	}
	return s.fieldMatches(s.fields[0], t.Minute()) &&
		s.fieldMatches(s.fields[1], t.Hour()) &&
		s.fieldMatches(s.fields[3], int(t.Month())) &&
		dayMatches
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
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d: %q", len(parts), expr)
	}

	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	sched := &cronSchedule{}
	for i, f := range parts {
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
	if strings.Contains(s, ",") {
		var vals []int
		seen := map[int]bool{}
		for _, part := range strings.Split(s, ",") {
			if part == "" {
				return cronField{}, fmt.Errorf("unsupported field value %q", s)
			}
			cf, err := parseCronField(part, min, max)
			if err != nil {
				return cronField{}, err
			}
			if cf.star {
				return cf, nil
			}
			for _, v := range cf.values {
				if !seen[v] {
					seen[v] = true
					vals = append(vals, v)
				}
			}
		}
		return cronField{values: vals}, nil
	}

	// */step
	if len(s) > 2 && s[:2] == "*/" {
		var step int
		var err error
		if step, err = strconv.Atoi(s[2:]); err != nil || step <= 0 {
			return cronField{}, fmt.Errorf("unsupported field value %q", s)
		}
		var vals []int
		for v := min; v <= max; v += step {
			vals = append(vals, v)
		}
		return cronField{values: vals}, nil
	}

	// lo-hi or lo-hi/step
	if idx := indexByte(s, '-'); idx > 0 {
		var lo, hi int
		var err error
		if lo, err = strconv.Atoi(s[:idx]); err != nil {
			return cronField{}, fmt.Errorf("unsupported field value %q", s)
		}
		rest := s[idx+1:]
		step := 1
		if si := indexByte(rest, '/'); si > 0 {
			if step, err = strconv.Atoi(rest[si+1:]); err != nil || step <= 0 {
				return cronField{}, fmt.Errorf("unsupported field value %q", s)
			}
			rest = rest[:si]
		}
		if hi, err = strconv.Atoi(rest); err != nil {
			return cronField{}, fmt.Errorf("unsupported field value %q", s)
		}
		if lo < min || hi > max || lo > hi {
			return cronField{}, fmt.Errorf("range %d-%d out of bounds [%d,%d]", lo, hi, min, max)
		}
		var vals []int
		for v := lo; v <= hi; v += step {
			vals = append(vals, v)
		}
		return cronField{values: vals}, nil
	}

	// Single value.
	v, err := strconv.Atoi(s)
	if err != nil {
		return cronField{}, fmt.Errorf("unsupported field value %q", s)
	}
	if v < min || v > max {
		return cronField{}, fmt.Errorf("value %d out of range [%d,%d]", v, min, max)
	}
	return cronField{values: []int{v}}, nil
}

// indexByte returns the index of the first occurrence of b in s, or -1.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
