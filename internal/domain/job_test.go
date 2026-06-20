package domain_test

import (
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
)

func TestJobValid(t *testing.T) {
	if !domain.JobDeveloper.Valid() {
		t.Errorf("JobDeveloper.Valid() = false, want true")
	}
	if domain.JobUnknown.Valid() {
		t.Errorf("JobUnknown.Valid() = true, want false (not a real job)")
	}
	if domain.Job("").Valid() {
		t.Errorf("empty job Valid() = true, want false")
	}
	if domain.Job("lawyer").Valid() {
		t.Errorf("unknown job Valid() = true, want false")
	}
}

func TestJobString(t *testing.T) {
	if got := domain.Job("").String(); got != "unknown" {
		t.Errorf("empty job String() = %q, want \"unknown\"", got)
	}
	if got := domain.JobUnknown.String(); got != "unknown" {
		t.Errorf("JobUnknown.String() = %q, want \"unknown\"", got)
	}
	if got := domain.JobDeveloper.String(); got != "developer" {
		t.Errorf("JobDeveloper.String() = %q, want \"developer\"", got)
	}
}
