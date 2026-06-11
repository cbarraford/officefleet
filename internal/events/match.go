// Package events implements the SP3 eventing core: ingestion, the in-process
// bus, the dispatcher, and the poll loop. The events table is the durable
// source of truth; delivery is at-least-once with per-assignment dedup
// downstream making redelivery safe.
package events

import (
	"fmt"

	"github.com/cbarraford/office-fleet/internal/domain"
)

// Matches reports whether an event-subscription filter matches an event.
// The filter must contain non-empty "source" and "event_type" strings
// (config validation enforces this; absence here is defensively a non-match).
// Every other key must exactly match the same-named top-level payload_norm
// field; values are compared as strings via fmt.Sprint so YAML ints match
// JSON float64s.
func Matches(filter map[string]any, ev *domain.Event) bool {
	src, _ := filter["source"].(string)
	typ, _ := filter["event_type"].(string)
	if src == "" || typ == "" || src != ev.SourcePlugin || typ != ev.EventType {
		return false
	}
	for k, v := range filter {
		if k == "source" || k == "event_type" {
			continue
		}
		nv, ok := ev.PayloadNorm[k]
		if !ok || fmt.Sprint(v) != fmt.Sprint(nv) {
			return false
		}
	}
	return true
}
