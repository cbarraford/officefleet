package repo

import (
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/domain"
)

func TestPrepareLLMResultForStorageTruncatesTranscript(t *testing.T) {
	original := &domain.LLMResult{
		Summary:    "done",
		Transcript: strings.Repeat("x", 64),
	}

	stored := prepareLLMResultForStorage(original, 32)
	if len([]byte(stored.Transcript)) > 32 {
		t.Fatalf("stored transcript is %d bytes, want <= 32", len([]byte(stored.Transcript)))
	}
	if !strings.Contains(stored.Transcript, "truncated") {
		t.Fatalf("stored transcript %q missing truncation marker", stored.Transcript)
	}
	if original.Transcript != strings.Repeat("x", 64) {
		t.Fatal("prepareLLMResultForStorage mutated the caller's result")
	}
	if stored.Summary != "done" {
		t.Fatalf("summary = %q, want unchanged", stored.Summary)
	}
}
