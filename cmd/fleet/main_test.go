package main

import (
	"testing"
	"time"
)

func TestParsePruneOlderThan(t *testing.T) {
	d, err := parsePruneOlderThan("2160h")
	if err != nil {
		t.Fatal(err)
	}
	if d != 90*24*time.Hour {
		t.Fatalf("duration = %v, want 90 days", d)
	}
}

func TestParsePruneOlderThanRejectsNonPositive(t *testing.T) {
	for _, input := range []string{"", "0s", "-1h"} {
		if _, err := parsePruneOlderThan(input); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}
