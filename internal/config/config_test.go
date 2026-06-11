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
		Backends:    []config.Backend{{Name: "b1", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:      []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "b1"}}},
		Duties:      []config.DutyConfig{{Name: "d1"}},
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
		Backends:    []config.Backend{{Name: "agent-backend", Kind: "claude", DefaultEffort: "high"}},
		Agents:      []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "agent-backend"}}},
		Duties:      []config.DutyConfig{{Name: "d1"}},
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
		Agents:      []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "agent-backend"}}},
		Duties:      []config.DutyConfig{{Name: "d1", Backend: &domain.BackendRef{Name: "duty-backend"}}},
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
		Backends:    []config.Backend{{Name: "b1", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:      []config.AgentConfig{{Name: "a1"}},                   // no DefaultBackend
		Duties:      []config.DutyConfig{{Name: "d1"}},                    // no Backend
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
		Backends:    []config.Backend{{Name: "b1", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:      []config.AgentConfig{{Name: "a1"}}, // no DefaultBackend
		Duties:      []config.DutyConfig{{Name: "d1", Backend: &domain.BackendRef{Name: "b1"}}},
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
		Agents:      []config.AgentConfig{{Name: "a1"}},                   // no DefaultBackend
		Duties:      []config.DutyConfig{{Name: "d1"}},                    // no Backend
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}}, // no Backend
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

// --- SP2 validation tests ---

func validEndpointBackend() config.Backend {
	return config.Backend{
		Name:    "local-ollama",
		Kind:    "openai-compatible",
		BaseURI: "http://localhost:11434/v1",
		Model:   "llama3.1:70b",
		Auth:    config.BackendAuth{Mode: "none"},
	}
}

func validVoterConfig() *config.Config {
	return &config.Config{
		Backends: []config.Backend{
			{Name: "claude-sub", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}},
			validEndpointBackend(),
			{Name: "panel-1", Kind: "voter", Strategy: "first_success", Panel: []string{"claude-sub", "local-ollama"}},
		},
	}
}

func errorsContain(t *testing.T, errs []error, substr string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return
		}
	}
	t.Errorf("expected a validation error containing %q, got %v", substr, errs)
}

func TestValidate_EndpointBackendValid(t *testing.T) {
	cfg := &config.Config{Backends: []config.Backend{validEndpointBackend()}}
	if errs := config.Validate(cfg); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_EndpointRequiresBaseURI(t *testing.T) {
	b := validEndpointBackend()
	b.BaseURI = ""
	errs := config.Validate(&config.Config{Backends: []config.Backend{b}})
	errorsContain(t, errs, "base_uri")
}

func TestValidate_EndpointRequiresModel(t *testing.T) {
	b := validEndpointBackend()
	b.Model = ""
	errs := config.Validate(&config.Config{Backends: []config.Backend{b}})
	errorsContain(t, errs, "model")
}

func TestValidate_EndpointRejectsSubscription(t *testing.T) {
	b := validEndpointBackend()
	b.Auth = config.BackendAuth{Mode: "subscription"}
	errs := config.Validate(&config.Config{Backends: []config.Backend{b}})
	errorsContain(t, errs, "subscription")
}

func TestValidate_EndpointBadCommandTimeout(t *testing.T) {
	b := validEndpointBackend()
	b.CommandTimeout = "two minutes"
	errs := config.Validate(&config.Config{Backends: []config.Backend{b}})
	errorsContain(t, errs, "command_timeout")
}

func TestValidate_VoterValid(t *testing.T) {
	if errs := config.Validate(validVoterConfig()); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_VoterBadStrategy(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends[2].Strategy = "consensus"
	errs := config.Validate(cfg)
	errorsContain(t, errs, "strategy")
}

func TestValidate_VoterEmptyPanel(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends[2].Panel = nil
	errs := config.Validate(cfg)
	errorsContain(t, errs, "panel")
}

func TestValidate_VoterUnknownMember(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends[2].Panel = []string{"claude-sub", "ghost"}
	errs := config.Validate(cfg)
	errorsContain(t, errs, "ghost")
}

func TestValidate_VoterNoNesting(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends = append(cfg.Backends, config.Backend{
		Name: "panel-2", Kind: "voter", Strategy: "majority", Panel: []string{"panel-1"},
	})
	errs := config.Validate(cfg)
	errorsContain(t, errs, "voter")
}

func TestValidate_VoterRejectsEndpointFields(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Backends[2].Model = "llama3.1"
	errs := config.Validate(cfg)
	errorsContain(t, errs, "must not set")
}

func TestValidate_VoterRefModelOverrideRejected(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Agents = []config.AgentConfig{
		{Name: "a1", DefaultBackend: domain.BackendRef{Name: "panel-1", Model: "llama3.1"}},
	}
	errs := config.Validate(cfg)
	errorsContain(t, errs, "override")
}

func TestValidate_VoterRefEffortOverrideRejected(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Duties = []config.DutyConfig{
		{Name: "d1", Backend: &domain.BackendRef{Name: "panel-1", Effort: "high"}},
	}
	errs := config.Validate(cfg)
	errorsContain(t, errs, "override")
}

func TestValidate_VoterRefOverrideOnAssignment(t *testing.T) {
	cfg := validVoterConfig()
	cfg.Agents = []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "claude-sub"}}}
	cfg.Duties = []config.DutyConfig{{Name: "d1"}}
	cfg.Assignments = []config.AssignmentConfig{{
		Agent: "a1", Duty: "d1",
		Backend: &domain.BackendRef{Name: "panel-1", Model: "llama3.1"},
	}}
	errs := config.Validate(cfg)
	errorsContain(t, errs, "override")
}

// --- SP3 validation tests ---

func eventSubConfig() *config.Config {
	return &config.Config{
		Backends: []config.Backend{{Name: "b", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
		Agents:   []config.AgentConfig{{Name: "a1", DefaultBackend: domain.BackendRef{Name: "b"}}},
		Duties: []config.DutyConfig{{
			Name: "d1", TriggerKinds: []string{"manual", "event-subscription"},
		}},
		Assignments: []config.AssignmentConfig{{
			Agent: "a1", Duty: "d1",
			Trigger: domain.TriggerConfig{
				Kind:   "event-subscription",
				Filter: map[string]any{"source": "gitlab", "event_type": "mr_opened"},
			},
		}},
	}
}

func TestValidate_EventSubscriptionValid(t *testing.T) {
	if errs := config.Validate(eventSubConfig()); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_EventSubscriptionMissingSource(t *testing.T) {
	cfg := eventSubConfig()
	cfg.Assignments[0].Trigger.Filter = map[string]any{"event_type": "mr_opened"}
	errorsContain(t, config.Validate(cfg), "source")
}

func TestValidate_EventSubscriptionMissingEventType(t *testing.T) {
	cfg := eventSubConfig()
	cfg.Assignments[0].Trigger.Filter = map[string]any{"source": "gitlab"}
	errorsContain(t, config.Validate(cfg), "event_type")
}

func TestValidate_EventSubscriptionDutyKindMismatch(t *testing.T) {
	cfg := eventSubConfig()
	cfg.Duties[0].TriggerKinds = []string{"manual", "cron"}
	errorsContain(t, config.Validate(cfg), "trigger_kinds")
}

func TestValidateImageBackends(t *testing.T) {
	base := func() *config.Config {
		return &config.Config{
			ImageBackends: []config.ImageBackend{{
				Name: "dalle", Kind: "openai-image-compatible",
				BaseURI: "https://api.openai.com/v1", Model: "gpt-image-1",
				Auth: config.BackendAuth{Mode: "api_key", APIKey: "k"},
			}},
		}
	}

	t.Run("valid passes", func(t *testing.T) {
		if errs := config.Validate(base()); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
	})

	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantSub string
	}{
		{"missing name", func(c *config.Config) { c.ImageBackends[0].Name = "" }, "image backend missing name"},
		{"duplicate name", func(c *config.Config) {
			c.ImageBackends = append(c.ImageBackends, c.ImageBackends[0])
		}, "duplicate image backend name"},
		{"unknown kind", func(c *config.Config) { c.ImageBackends[0].Kind = "stable-diffusion" }, "unknown kind"},
		{"missing base_uri", func(c *config.Config) { c.ImageBackends[0].BaseURI = "" }, "requires base_uri"},
		{"missing model", func(c *config.Config) { c.ImageBackends[0].Model = "" }, "requires model"},
		{"bad auth mode", func(c *config.Config) { c.ImageBackends[0].Auth.Mode = "subscription" }, "auth mode"},
		{"api_key mode without key", func(c *config.Config) {
			c.ImageBackends[0].Auth = config.BackendAuth{Mode: "api_key"}
		}, "requires api_key"},
		{"avatar_backend undefined", func(c *config.Config) { c.Serve.AvatarBackend = "missing" }, "avatar_backend"},
		{"avatar_prompt unparsable", func(c *config.Config) { c.Serve.AvatarPrompt = "{{.Name" }, "avatar_prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			errs := config.Validate(cfg)
			found := false
			for _, e := range errs {
				if strings.Contains(e.Error(), tc.wantSub) {
					found = true
				}
			}
			if !found {
				t.Errorf("Validate() = %v, want an error containing %q", errs, tc.wantSub)
			}
		})
	}

	t.Run("avatar_backend referencing a defined backend passes", func(t *testing.T) {
		cfg := base()
		cfg.Serve.AvatarBackend = "dalle"
		if errs := config.Validate(cfg); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
	})

	t.Run("auth mode none is valid and empty mode normalizes to none", func(t *testing.T) {
		cfg := base()
		cfg.ImageBackends[0].Auth = config.BackendAuth{}
		if errs := config.Validate(cfg); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if cfg.ImageBackends[0].Auth.Mode != "none" {
			t.Errorf("mode = %q, want normalized none", cfg.ImageBackends[0].Auth.Mode)
		}
	})
}

// TestValidateForEach (SP5 Task 5): for_each must be a bare output key —
// letters, digits, underscore — never a template expression or path.
func TestValidateForEach(t *testing.T) {
	base := func(forEach string) *config.Config {
		return &config.Config{
			Backends: []config.Backend{{Name: "b", Kind: "claude", Auth: config.BackendAuth{Mode: "subscription"}}},
			Agents:   []config.AgentConfig{{Name: "a", Enabled: true, DefaultBackend: domain.BackendRef{Name: "b"}}},
			Duties:   []config.DutyConfig{{Name: "d", TriggerKinds: []string{"manual"}}},
			Assignments: []config.AssignmentConfig{{
				Agent: "a", Duty: "d",
				Trigger: domain.TriggerConfig{Kind: "manual"},
				Outputs: []domain.OutputBinding{{Plugin: "gitlab", Action: "create_issue", ForEach: forEach}},
			}},
		}
	}
	if errs := config.Validate(base("issues")); len(errs) != 0 {
		t.Fatalf("bare key must validate: %v", errs)
	}
	for _, bad := range []string{"{{.Event.x}}", "issues[0]", "a b"} {
		errs := config.Validate(base(bad))
		found := false
		for _, e := range errs {
			if strings.Contains(e.Error(), "for_each") {
				found = true
			}
		}
		if !found {
			t.Errorf("for_each %q: expected a validation error, got %v", bad, errs)
		}
	}
}

func TestValidate_ServeBlock(t *testing.T) {
	cfg := eventSubConfig()
	cfg.Serve = config.ServeConfig{Workers: -1}
	errorsContain(t, config.Validate(cfg), "workers")

	cfg = eventSubConfig()
	cfg.Serve = config.ServeConfig{RescanInterval: "soonish"}
	errorsContain(t, config.Validate(cfg), "rescan_interval")

	cfg = eventSubConfig()
	cfg.Serve = config.ServeConfig{Addr: ":9090", Workers: 8, RescanInterval: "45s", SecureCookies: true}
	if errs := config.Validate(cfg); len(errs) != 0 {
		t.Errorf("valid serve block rejected: %v", errs)
	}
}
