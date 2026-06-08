package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

// TestResolveBackend_DutyWinsOverAgent (gap M12): duty backend takes precedence over agent default when no
// assignment-level backend is set.
func TestResolveBackend_DutyWinsOverAgent(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "agent-backend", Kind: "claude", DefaultEffort: "low"},
			{Name: "duty-backend", Kind: "claude", DefaultEffort: "medium"},
		},
		Agents: []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "agent-backend"}}},
		Duties: []config.DutyConfig{{Name: "d1", Backend: &domain.BackendRef{Name: "duty-backend"}}},
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}},
	}
	b, _, err := config.ResolveBackend(cfg, cfg.Assignments[0])
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "duty-backend" {
		t.Fatalf("expected duty-backend to win over agent-backend, got %q", b.Name)
	}
}

// TestResolveBackend_BackendRefOverrides (gap M13): model and effort fields on a BackendRef override the
// backend's own defaults.
func TestResolveBackend_BackendRefOverrides(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "base-backend", Kind: "claude", Model: "base-model", DefaultEffort: "low"},
		},
		Agents: []config.AgentConfig{{
			Name: "a1",
			DefaultBackend: domain.BackendRef{
				Name:   "base-backend",
				Model:  "override-model",
				Effort: "high",
			},
		}},
		Duties:      []config.DutyConfig{{Name: "d1"}},
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}},
	}
	b, _, err := config.ResolveBackend(cfg, cfg.Assignments[0])
	if err != nil {
		t.Fatal(err)
	}
	if b.Model != "override-model" {
		t.Fatalf("expected Model to be overridden to %q, got %q", "override-model", b.Model)
	}
	if b.DefaultEffort != "high" {
		t.Fatalf("expected DefaultEffort to be overridden to %q, got %q", "high", b.DefaultEffort)
	}
}

// TestValidate_APIKeyEmptyRejected (gap M14): api_key auth mode with an empty key must produce a validation error.
func TestValidate_APIKeyEmptyRejected(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{
			Name: "b1",
			Kind: "claude",
			Auth: config.BackendAuth{Mode: "api_key", APIKey: ""},
		}},
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for empty api_key")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "api_key") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one error mentioning api_key, got: %v", errs)
	}
}

// TestValidate_DuplicateBackendName (gap config-6): two backends with the same name must produce a validation
// error mentioning "duplicate" so that the uniqueness check cannot be silently removed.
func TestValidate_DuplicateBackendName(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "shared-name", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}},
			{Name: "shared-name", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}},
		},
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for duplicate backend name, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "duplicate") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one error mentioning \"duplicate\", got: %v", errs)
	}
}

// TestValidate_AssignmentNoBackendAtAnyTier (gap config-3): an assignment where all three backend tiers are
// unset must produce a validation error so it fails at config time rather than runtime.
func TestValidate_AssignmentNoBackendAtAnyTier(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "b1", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:   []config.AgentConfig{{Name: "a1"}},                      // no DefaultBackend
		Duties:   []config.DutyConfig{{Name: "d1"}},                       // no Backend
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}}, // no Backend
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error when all three backend tiers are unset")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "no backend resolved") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error mentioning 'no backend resolved', got: %v", errs)
	}
}

// TestValidate_AssignmentNoBackendAtAnyTier_DutyCoversIt (gap config-3 negative): when the duty provides a
// backend, the assignment is valid even if the agent has no default.
func TestValidate_AssignmentNoBackendAtAnyTier_DutyCoversIt(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "b1", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:   []config.AgentConfig{{Name: "a1"}}, // no DefaultBackend
		Duties:   []config.DutyConfig{{Name: "d1", Backend: &domain.BackendRef{Name: "b1"}}},
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}},
	}
	errs := config.Validate(cfg)
	for _, e := range errs {
		if strings.Contains(e.Error(), "no backend resolved") {
			t.Fatalf("unexpected 'no backend resolved' error when duty provides backend: %v", e)
		}
	}
}

// TestValidate_UnknownKind (gap config-7): an unrecognised backend kind must produce a validation error
// mentioning the unknown kind so that a regression accepting arbitrary kinds would be detected.
func TestValidate_UnknownKind(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{
			Name: "bad-backend",
			Kind: "gpt-99",
			Auth: config.BackendAuth{Mode: "subscription"},
		}},
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for unknown backend kind, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "gpt-99") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one error mentioning the unknown kind %q, got: %v", "gpt-99", errs)
	}
}

// TestValidate_DutyMissingBackend (gap config-9): a duty whose backend field references a non-existent backend
// name must produce a validation error.
func TestValidate_DutyMissingBackend(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "real-backend", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Duties: []config.DutyConfig{{
			Name:    "d1",
			Backend: &domain.BackendRef{Name: "non-existent-backend"},
		}},
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for duty referencing non-existent backend, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "non-existent-backend") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error mentioning \"non-existent-backend\", got: %v", errs)
	}
}

// TestValidate_UnknownAuthMode (gap config-8): an unrecognised auth mode must produce a validation error
// mentioning "auth mode" so the switch at config.go lines 130-134 cannot be silently removed.
func TestValidate_UnknownAuthMode(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{
			Name: "b1",
			Kind: "claude",
			Auth: config.BackendAuth{Mode: "oauth"},
		}},
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for unknown auth mode, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "auth mode") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one error mentioning \"auth mode\", got: %v", errs)
	}
}

// TestResolveBackend_NoneConfigured (gap config-12): all three tiers are empty so ResolveBackend must return an
// error — the "no backend resolved" path.
func TestResolveBackend_NoneConfigured(t *testing.T) {
	cfg := &config.Config{
		Backends:    []config.Backend{{Name: "some-backend", Kind: "claude"}},
		Agents:      []config.AgentConfig{{Name: "a1"}},                        // no DefaultBackend
		Duties:      []config.DutyConfig{{Name: "d1"}},                         // no Backend
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}},      // no Backend
	}
	_, _, err := config.ResolveBackend(cfg, cfg.Assignments[0])
	if err == nil {
		t.Fatal("expected error when all three backend tiers are unset, got nil")
	}
}

// TestResolveBackend_BackendNotFound (gap config-12): the resolved ref points to a name absent from cfg.Backends
// so ResolveBackend must return an error — the "backend not found" path.
func TestResolveBackend_BackendNotFound(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "real-backend", Kind: "claude"}},
		Agents: []config.AgentConfig{{
			Name:           "a1",
			DefaultBackend: domain.BackendRef{Name: "ghost-backend"}, // not in Backends
		}},
		Duties:      []config.DutyConfig{{Name: "d1"}},
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}},
	}
	_, _, err := config.ResolveBackend(cfg, cfg.Assignments[0])
	if err == nil {
		t.Fatal("expected error when resolved backend name is not in cfg.Backends, got nil")
	}
}

// TestValidate_AssignmentBadAgent (gap config-10): an assignment referencing an undefined agent must produce a
// validation error mentioning the unknown agent name.
func TestValidate_AssignmentBadAgent(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "b1", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:   []config.AgentConfig{{Name: "real-agent", DefaultBackend: domain.BackendRef{Name: "b1"}}},
		Duties:   []config.DutyConfig{{Name: "d1"}},
		Assignments: []config.AssignmentConfig{{
			Agent: "no-such-agent",
			Duty:  "d1",
		}},
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for assignment referencing unknown agent, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "no-such-agent") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error mentioning \"no-such-agent\", got: %v", errs)
	}
}

// TestValidate_AssignmentBadDuty (gap config-10): an assignment referencing an undefined duty must produce a
// validation error mentioning the unknown duty name.
func TestValidate_AssignmentBadDuty(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "b1", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:   []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "b1"}}},
		Duties:   []config.DutyConfig{{Name: "real-duty"}},
		Assignments: []config.AssignmentConfig{{
			Agent: "a1",
			Duty:  "no-such-duty",
		}},
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for assignment referencing unknown duty, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "no-such-duty") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error mentioning \"no-such-duty\", got: %v", errs)
	}
}

// TestValidate_AssignmentBadBackend (gap config-10): an assignment referencing an undefined backend must produce
// a validation error mentioning the unknown backend name.
func TestValidate_AssignmentBadBackend(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "b1", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:   []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "b1"}}},
		Duties:   []config.DutyConfig{{Name: "d1"}},
		Assignments: []config.AssignmentConfig{{
			Agent:   "a1",
			Duty:    "d1",
			Backend: &domain.BackendRef{Name: "no-such-backend"},
		}},
	}
	errs := config.Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for assignment referencing unknown backend, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "no-such-backend") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error mentioning \"no-such-backend\", got: %v", errs)
	}
}

// TestValidate_APIKeySecretRefAccepted (gap M14 negative): a ${secret:...} reference is a valid api_key value.
func TestValidate_APIKeySecretRefAccepted(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{
			Name: "b1",
			Kind: "claude",
			Auth: config.BackendAuth{Mode: "api_key", APIKey: "${secret:anthropic_key}"},
		}},
	}
	errs := config.Validate(cfg)
	for _, e := range errs {
		if strings.Contains(e.Error(), "api_key") {
			t.Fatalf("unexpected api_key error for secret ref: %v", e)
		}
	}
}

// TestLoad_UnsetEnvRefReturnsError (gap config-5): Load must return an error when a ${env:VAR}
// placeholder refers to a variable that is not set in the environment.
func TestLoad_UnsetEnvRefReturnsError(t *testing.T) {
	const unsetVar = "FLEET_TEST_UNSET_VAR_XYZ"
	os.Unsetenv(unsetVar)

	content := []byte("database:\n  dsn: \"${env:" + unsetVar + "}\"\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for unset env var, got nil")
	}
	if !strings.Contains(err.Error(), unsetVar) {
		t.Fatalf("expected error to mention the variable name %q, got: %v", unsetVar, err)
	}
}

// TestLoad_SetEnvRefExpandsCorrectly (gap config-5 negative): Load must succeed and expand
// a ${env:VAR} placeholder when the variable is set.
func TestLoad_SetEnvRefExpandsCorrectly(t *testing.T) {
	const testVar = "FLEET_TEST_SET_VAR_XYZ"
	t.Setenv(testVar, "postgres://localhost/fleet_env_test")

	content := []byte("database:\n  dsn: \"${env:" + testVar + "}\"\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Database.DSN != "postgres://localhost/fleet_env_test" {
		t.Fatalf("expected DSN to be expanded, got %q", cfg.Database.DSN)
	}
}
