package events

import (
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
)

func ev(source, eventType string, norm map[string]any) *domain.Event {
	return &domain.Event{SourcePlugin: source, EventType: eventType, PayloadNorm: norm}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		name   string
		filter map[string]any
		event  *domain.Event
		want   bool
	}{
		{"source+type match", map[string]any{"source": "gitlab", "event_type": "mr_opened"},
			ev("gitlab", "mr_opened", nil), true},
		{"source mismatch", map[string]any{"source": "github", "event_type": "mr_opened"},
			ev("gitlab", "mr_opened", nil), false},
		{"type mismatch", map[string]any{"source": "gitlab", "event_type": "mr_merged"},
			ev("gitlab", "mr_opened", nil), false},
		{"missing source -> never matches", map[string]any{"event_type": "mr_opened"},
			ev("gitlab", "mr_opened", nil), false},
		{"missing event_type -> never matches", map[string]any{"source": "gitlab"},
			ev("gitlab", "mr_opened", nil), false},
		{"empty filter -> never matches", map[string]any{},
			ev("gitlab", "mr_opened", nil), false},
		{"extra key exact match", map[string]any{"source": "gitlab", "event_type": "mr_opened", "project": "org/repo"},
			ev("gitlab", "mr_opened", map[string]any{"project": "org/repo"}), true},
		{"extra key mismatch", map[string]any{"source": "gitlab", "event_type": "mr_opened", "project": "org/other"},
			ev("gitlab", "mr_opened", map[string]any{"project": "org/repo"}), false},
		{"extra key absent from norm -> no match", map[string]any{"source": "gitlab", "event_type": "mr_opened", "project": "org/repo"},
			ev("gitlab", "mr_opened", map[string]any{}), false},
		// YAML filters parse numbers as int; JSON payload_norm parses them as float64.
		{"numeric coercion int vs float64", map[string]any{"source": "gitlab", "event_type": "mr_opened", "mr_iid": 42},
			ev("gitlab", "mr_opened", map[string]any{"mr_iid": float64(42)}), true},
		{"numeric mismatch", map[string]any{"source": "gitlab", "event_type": "mr_opened", "mr_iid": 42},
			ev("gitlab", "mr_opened", map[string]any{"mr_iid": float64(43)}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Matches(c.filter, c.event); got != c.want {
				t.Errorf("Matches(%v, %v/%v %v) = %v, want %v",
					c.filter, c.event.SourcePlugin, c.event.EventType, c.event.PayloadNorm, got, c.want)
			}
		})
	}
}
