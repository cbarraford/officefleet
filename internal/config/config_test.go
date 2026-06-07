package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
)

func TestLoad_ValidYAML(t *testing.T) {
	content := []byte(`
database:
  dsn: "postgres://localhost/fleet_test"
backends:
  - name: claude-default
    kind: claude
    auth:
      mode: subscription
    default_effort: high
agents:
  - name: dev-1
    role: developer
    system_prompt: "You are a developer."
    default_backend:
      name: claude-default
    enabled: true
duties:
  - name: mr-reviewer
    role: developer
    description: "Reviews merge requests"
    trigger_kinds: [manual, cron]
    prompt: "Review MR #{{.Event.mr_iid}}"
    required_tools: [glab]
    output_actions:
      - plugin: gitlab
        action: post_mr_comment
        params_template: ""
assignments:
  - agent: dev-1
    duty: mr-reviewer
    enabled: true
    trigger:
      kind: manual
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].Name != "claude-default" {
		t.Fatalf("unexpected backends: %+v", cfg.Backends)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "dev-1" {
		t.Fatalf("unexpected agents: %+v", cfg.Agents)
	}
}

func TestValidate_MissingBackend(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{{Name: "dev-1", DefaultBackend: domain.BackendRef{Name: "missing-backend"}}},
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for missing backend")
	}
}

func TestValidate_Clean(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "b1", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:   []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "b1"}}},
		Duties:   []config.DutyConfig{{Name: "d1"}},
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}},
	}
	errs := config.Validate(cfg)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestResolveBackend_Precedence(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "agent-backend", Kind: "claude", DefaultEffort: "low"},
			{Name: "duty-backend", Kind: "claude", DefaultEffort: "medium"},
			{Name: "assign-backend", Kind: "claude", DefaultEffort: "high"},
		},
		Agents: []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "agent-backend"}}},
		Duties: []config.DutyConfig{{Name: "d1", Backend: &domain.BackendRef{Name: "duty-backend"}}},
		Assignments: []config.AssignmentConfig{
			{Agent: "a1", Duty: "d1", Backend: &domain.BackendRef{Name: "assign-backend"}},
		},
	}
	b, _, err := config.ResolveBackend(cfg, cfg.Assignments[0])
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "assign-backend" {
		t.Fatalf("expected assign-backend to win, got %q", b.Name)
	}
}

func TestResolveBackend_FallsBackToAgent(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "agent-backend", Kind: "claude", DefaultEffort: "high"}},
		Agents:   []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "agent-backend"}}},
		Duties:   []config.DutyConfig{{Name: "d1"}},
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}},
	}
	b, _, err := config.ResolveBackend(cfg, cfg.Assignments[0])
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "agent-backend" {
		t.Fatalf("expected agent-backend as fallback, got %q", b.Name)
	}
}
