package run

import "testing"

func TestResolveForge(t *testing.T) {
	gh, err := resolveForge(map[string]any{"forge": "github"})
	if err != nil {
		t.Fatal(err)
	}
	if gh["cli"] != "gh" {
		t.Errorf("github cli = %v", gh["cli"])
	}

	def, err := resolveForge(map[string]any{}) // no forge key -> default gitlab
	if err != nil {
		t.Fatal(err)
	}
	if def["cli"] != "glab" {
		t.Errorf("default forge cli = %v, want glab", def["cli"])
	}

	if _, err := resolveForge(map[string]any{"forge": "nope"}); err == nil {
		t.Error("expected error for unknown forge")
	}
}
