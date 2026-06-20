package domain_test

import (
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
)

func TestJobValid(t *testing.T) {
	if !domain.JobDeveloper.Valid() {
		t.Errorf("JobDeveloper.Valid() = false, want true")
	}
	if domain.Job("").Valid() {
		t.Errorf("empty job Valid() = true, want false")
	}
	if domain.Job("lawyer").Valid() {
		t.Errorf("unknown job Valid() = true, want false")
	}
}
