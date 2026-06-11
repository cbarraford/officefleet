package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/prompt"
)

// TestSampleConfig pins the shipped sample: it must parse, validate, and its
// duty prompts must render against a realistic context (catches {{.Event.x}}
// typos and template syntax errors at test time, not at first live run).
func TestSampleConfig(t *testing.T) {
	t.Setenv("FLEET_DATABASE_DSN", "postgres://test")
	cfg, err := config.Load("../../configs/fleet.yaml")
	if err != nil {
		t.Fatalf("sample config must load: %v", err)
	}
	if errs := config.Validate(cfg); len(errs) != 0 {
		t.Fatalf("sample config must validate: %v", errs)
	}

	wantDuties := map[string]bool{"mr-review": false, "code-audit": false, "mr-feedback": false}
	syntheticCtx := prompt.Context{
		Event: map[string]any{
			"mr_iid": 42, "title": "Add limiter", "mr_title": "Add limiter",
			"source_branch": "feat/x", "target_branch": "main", "mr_source_branch": "feat/x",
			"note_body": "why this approach?", "author": "alice", "discussion_id": "abc",
		},
		Agent:      map[string]any{"name": "dev-1", "role": "developer"},
		Duty:       map[string]any{},
		Assignment: map[string]any{"project": "org/repo", "category": "security"},
		State:      map[string]any{},
		Now:        time.Now(),
		Secrets:    map[string]string{"gitlab_token": "tok"},
	}
	for _, d := range cfg.Duties {
		if _, tracked := wantDuties[d.Name]; tracked {
			wantDuties[d.Name] = true
		}
		rendered, err := prompt.Render(d.Prompt, syntheticCtx)
		if err != nil {
			t.Errorf("duty %q prompt does not render: %v", d.Name, err)
			continue
		}
		if strings.Contains(rendered, "<no value>") {
			t.Errorf("duty %q prompt rendered a missing field (<no value>):\n%s", d.Name, rendered)
		}
	}
	for name, seen := range wantDuties {
		if !seen {
			t.Errorf("sample config missing duty %q", name)
		}
	}

	// Every assignment output param template must render too.
	for _, a := range cfg.Assignments {
		for _, out := range a.Outputs {
			for key, v := range out.Params {
				s, ok := v.(string)
				if !ok {
					continue
				}
				itemCtx := syntheticCtx
				itemCtx.Item = map[string]any{
					"path": "a.go", "line": 7, "severity": "high", "body": "x",
					"title": "t", "description": "d", "labels": "l",
				}
				if _, err := prompt.Render(s, itemCtx); err != nil {
					t.Errorf("assignment (%s,%s) output param %q does not render: %v", a.Agent, a.Duty, key, err)
				}
			}
		}
	}
}
