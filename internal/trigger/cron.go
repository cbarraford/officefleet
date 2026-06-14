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

// maxScanMinutes bounds the search for the next firing. 5 years covers rare but
// valid schedules (e.g. Feb 29, which recurs every 4 years) while letting
// genuinely unsatisfiable ones (e.g. Feb 31) return an error.
const maxScanMinutes = 5 * 366 * 24 * 60

// Next returns the next firing strictly after t, or an error when the schedule
// matches no time within the scan window (unsatisfiable, e.g. Feb 31). Returning
// an error — rather than the zero time — is critical: the scheduler treats a
// zero time as "always due" and would fire a paid run on every tick (issue #9).
func (s *cronSchedule) Next(t time.Time) (time.Time, error) {
	// Advance by at least one minute.
	t = t.Add(time.Minute).Truncate(time.Minute)
	for i := 0; i < maxScanMinutes; i++ {
		if s.matches(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron: no matching time within 5 years (unsatisfiable schedule)")
}

func (s *cronSchedule) matches(t time.Time) bool {
	if !s.fieldMatches(s.fields[0], t.Minute()) ||
		!s.fieldMatches(s.fields[1], t.Hour()) ||
		!s.fieldMatches(s.fields[3], int(t.Month())) {
		return false
	}
	domMatch := s.fieldMatches(s.fields[2], t.Day())
	dowMatch := s.fieldMatches(s.fields[4], int(t.Weekday()))
	// Standard cron semantics: when BOTH day-of-month and day-of-week are
	// restricted, fire when EITHER matches; otherwise the single restricted
	// field (or, if both are "*", every day) applies.
	if !s.fields[2].star && !s.fields[4].star {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
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
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d: %q", len(fields), expr)
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

// parseCronField parses one whitespace-delimited field, which may be a single
// "*" or a comma-separated list of atoms (single value, range, or step).
func parseCronField(s string, min, max int) (cronField, error) {
	if s == "*" {
		return cronField{star: true}, nil
	}
	var vals []int
	seen := map[int]bool{}
	for _, atom := range strings.Split(s, ",") {
		av, err := parseCronAtom(atom, min, max)
		if err != nil {
			return cronField{}, err
		}
		for _, v := range av {
			if !seen[v] {
				seen[v] = true
				vals = append(vals, v)
			}
		}
	}
	if len(vals) == 0 {
		return cronField{}, fmt.Errorf("empty field %q", s)
	}
	return cronField{values: vals}, nil
}

// parseCronAtom expands one comma element into the integers it covers.
// Supported forms: "*", "*/step", "lo-hi", "lo-hi/step", and a single value.
func parseCronAtom(s string, min, max int) ([]int, error) {
	if s == "*" {
		return rangeVals(min, max, 1), nil
	}

	// */step
	if rest, ok := strings.CutPrefix(s, "*/"); ok {
		step, err := strconv.Atoi(rest)
		if err != nil || step <= 0 {
			return nil, fmt.Errorf("unsupported field value %q", s)
		}
		return rangeVals(min, max, step), nil
	}

	// lo-hi or lo-hi/step
	if idx := strings.IndexByte(s, '-'); idx > 0 {
		lo, err := strconv.Atoi(s[:idx])
		if err != nil {
			return nil, fmt.Errorf("unsupported field value %q", s)
		}
		rest := s[idx+1:]
		step := 1
		if si := strings.IndexByte(rest, '/'); si > 0 {
			step, err = strconv.Atoi(rest[si+1:])
			if err != nil || step <= 0 {
				return nil, fmt.Errorf("unsupported field value %q", s)
			}
			rest = rest[:si]
		}
		hi, err := strconv.Atoi(rest)
		if err != nil {
			return nil, fmt.Errorf("unsupported field value %q", s)
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("range %d-%d out of bounds [%d,%d]", lo, hi, min, max)
		}
		return rangeVals(lo, hi, step), nil
	}

	// Single value.
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil, fmt.Errorf("unsupported field value %q", s)
	}
	if v < min || v > max {
		return nil, fmt.Errorf("value %d out of range [%d,%d]", v, min, max)
	}
	return []int{v}, nil
}

func rangeVals(lo, hi, step int) []int {
	var vals []int
	for v := lo; v <= hi; v += step {
		vals = append(vals, v)
	}
	return vals
}
