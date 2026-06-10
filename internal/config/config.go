package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"gopkg.in/yaml.v3"
)

// BackendAuth describes how a backend authenticates with its provider.
type BackendAuth struct {
	Mode   string `yaml:"mode"`              // subscription | api_key | none
	APIKey string `yaml:"api_key,omitempty"` // may be ${secret:name} reference
}

// Backend is a named, configured LLM provider instance.
type Backend struct {
	Name          string         `yaml:"name"`
	Kind          string         `yaml:"kind"` // claude | openai-compatible | voter
	Auth          BackendAuth    `yaml:"auth"`
	BaseURI       string         `yaml:"base_uri,omitempty"`
	Model         string         `yaml:"model,omitempty"`
	DefaultEffort string         `yaml:"default_effort,omitempty"`
	Params        map[string]any `yaml:"params,omitempty"`

	// Generic agent loop limits (openai-compatible only).
	MaxIterations    int      `yaml:"max_iterations,omitempty"`    // default 25
	CommandTimeout   string   `yaml:"command_timeout,omitempty"`   // Go duration, e.g. "120s"; default 120s
	MaxOutputBytes   int      `yaml:"max_output_bytes,omitempty"`  // default 65536
	CommandAllowlist []string `yaml:"command_allowlist,omitempty"` // empty = allow all

	// Voter (kind: voter only).
	Strategy string   `yaml:"strategy,omitempty"` // first_success | majority
	Panel    []string `yaml:"panel,omitempty"`    // names of non-voter backends
}

// AgentConfig configures one agent (mirrors domain.Agent plus YAML BackendRef).
type AgentConfig struct {
	Name           string            `yaml:"name"`
	Role           string            `yaml:"role"`
	SystemPrompt   string            `yaml:"system_prompt"`
	DefaultBackend domain.BackendRef `yaml:"default_backend"`
	Enabled        bool              `yaml:"enabled"`
}

// DutyConfig configures one duty.
type DutyConfig struct {
	Name          string                    `yaml:"name"`
	Role          string                    `yaml:"role"`
	Description   string                    `yaml:"description"`
	TriggerKinds  []string                  `yaml:"trigger_kinds"`
	Prompt        string                    `yaml:"prompt"`
	RequiredTools []string                  `yaml:"required_tools"`
	OutputActions []domain.OutputActionType `yaml:"output_actions"`
	ConfigSchema  map[string]any            `yaml:"config_schema,omitempty"`
	Backend       *domain.BackendRef        `yaml:"backend,omitempty"`
}

// AssignmentConfig configures one assignment.
type AssignmentConfig struct {
	Agent              string                 `yaml:"agent"`
	Duty               string                 `yaml:"duty"`
	Enabled            bool                   `yaml:"enabled"`
	Trigger            domain.TriggerConfig   `yaml:"trigger"`
	Outputs            []domain.OutputBinding `yaml:"outputs"`
	Config             map[string]any         `yaml:"config,omitempty"`
	Backend            *domain.BackendRef     `yaml:"backend,omitempty"`
	TaskPromptOverride *string                `yaml:"task_prompt_override,omitempty"`
	ExtraInstructions  *string                `yaml:"extra_instructions,omitempty"`
}

// PluginConfig holds per-plugin configuration.
type PluginConfig struct {
	Name   string         `yaml:"name"`
	Config map[string]any `yaml:"config"`
}

// DatabaseConfig holds the Postgres connection settings.
type DatabaseConfig struct {
	DSN string `yaml:"dsn"`
}

// Config is the root fleet.yaml configuration.
type Config struct {
	Database    DatabaseConfig     `yaml:"database"`
	Backends    []Backend          `yaml:"backends"`
	Plugins     []PluginConfig     `yaml:"plugins"`
	Agents      []AgentConfig      `yaml:"agents"`
	Duties      []DutyConfig       `yaml:"duties"`
	Assignments []AssignmentConfig `yaml:"assignments"`
}

// Load reads and parses a fleet.yaml file, expanding ${secret:name} and ${env:name} references.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	// Expand ${env:VAR} references before YAML parsing.
	expanded, err := expandEnvRefs(string(data))
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// Validate checks that the config is self-consistent.
func Validate(cfg *Config) []error {
	var errs []error

	backendNames := map[string]bool{}
	backendKind := map[string]string{}
	for i := range cfg.Backends {
		b := &cfg.Backends[i]
		if b.Name == "" {
			errs = append(errs, fmt.Errorf("backend missing name"))
			continue
		}
		if backendNames[b.Name] {
			errs = append(errs, fmt.Errorf("duplicate backend name: %q", b.Name))
		}
		backendNames[b.Name] = true
		backendKind[b.Name] = b.Kind
		switch b.Kind {
		case "claude", "openai-compatible", "voter":
		default:
			errs = append(errs, fmt.Errorf("backend %q: unknown kind %q", b.Name, b.Kind))
		}
		// Normalize an omitted auth mode to "none" so an unset field has an
		// explicit, unambiguous meaning rather than silently passing as "".
		if b.Auth.Mode == "" {
			b.Auth.Mode = "none"
		}
		switch b.Auth.Mode {
		case "subscription", "api_key", "none":
		default:
			errs = append(errs, fmt.Errorf("backend %q: unknown auth mode %q", b.Name, b.Auth.Mode))
		}
		if b.Auth.Mode == "api_key" && strings.TrimSpace(b.Auth.APIKey) == "" {
			errs = append(errs, fmt.Errorf("backend %s: auth mode api_key requires api_key to be set", b.Name))
		}
		switch b.Kind {
		case "openai-compatible":
			if b.BaseURI == "" {
				errs = append(errs, fmt.Errorf("backend %q: kind openai-compatible requires base_uri", b.Name))
			}
			if b.Model == "" {
				errs = append(errs, fmt.Errorf("backend %q: kind openai-compatible requires model", b.Name))
			}
			if b.Auth.Mode == "subscription" {
				errs = append(errs, fmt.Errorf("backend %q: subscription auth is not supported for endpoint backends (use api_key or none)", b.Name))
			}
		case "voter":
			if b.Strategy != "first_success" && b.Strategy != "majority" {
				errs = append(errs, fmt.Errorf("backend %q: voter strategy must be first_success or majority, got %q", b.Name, b.Strategy))
			}
			if len(b.Panel) == 0 {
				errs = append(errs, fmt.Errorf("backend %q: voter requires a non-empty panel", b.Name))
			}
			if b.BaseURI != "" || b.Model != "" || b.MaxIterations != 0 ||
				b.CommandTimeout != "" || b.MaxOutputBytes != 0 || len(b.CommandAllowlist) != 0 {
				errs = append(errs, fmt.Errorf("backend %q: voter backends must not set base_uri/model/loop-limit fields", b.Name))
			}
		}
		if b.CommandTimeout != "" {
			if _, err := time.ParseDuration(b.CommandTimeout); err != nil {
				errs = append(errs, fmt.Errorf("backend %q: invalid command_timeout %q: %v", b.Name, b.CommandTimeout, err))
			}
		}
	}

	// Voter panels: every member must be a defined, non-voter backend.
	for i := range cfg.Backends {
		b := &cfg.Backends[i]
		if b.Kind != "voter" {
			continue
		}
		for _, member := range b.Panel {
			if !backendNames[member] {
				errs = append(errs, fmt.Errorf("backend %q: panel member %q not defined", b.Name, member))
				continue
			}
			if backendKind[member] == "voter" {
				errs = append(errs, fmt.Errorf("backend %q: panel member %q is a voter; voter nesting is not supported", b.Name, member))
			}
		}
	}

	// rejectVoterOverride flags model/effort overrides on refs that point at voters.
	rejectVoterOverride := func(where string, ref domain.BackendRef) {
		if ref.Name == "" || backendKind[ref.Name] != "voter" {
			return
		}
		if ref.Model != "" || ref.Effort != "" {
			errs = append(errs, fmt.Errorf("%s: model/effort override on voter backend %q is not supported (each panel member resolves its own)", where, ref.Name))
		}
	}

	agentNames := map[string]bool{}
	for _, a := range cfg.Agents {
		if a.Name == "" {
			errs = append(errs, fmt.Errorf("agent missing name"))
			continue
		}
		if agentNames[a.Name] {
			errs = append(errs, fmt.Errorf("duplicate agent name: %q", a.Name))
		}
		agentNames[a.Name] = true
		if a.DefaultBackend.Name != "" && !backendNames[a.DefaultBackend.Name] {
			errs = append(errs, fmt.Errorf("agent %q: default_backend %q not defined", a.Name, a.DefaultBackend.Name))
		}
		rejectVoterOverride(fmt.Sprintf("agent %q", a.Name), a.DefaultBackend)
	}

	dutyNames := map[string]bool{}
	for _, d := range cfg.Duties {
		if d.Name == "" {
			errs = append(errs, fmt.Errorf("duty missing name"))
			continue
		}
		if dutyNames[d.Name] {
			errs = append(errs, fmt.Errorf("duplicate duty name: %q", d.Name))
		}
		dutyNames[d.Name] = true
		if d.Backend != nil && !backendNames[d.Backend.Name] {
			errs = append(errs, fmt.Errorf("duty %q: backend %q not defined", d.Name, d.Backend.Name))
		}
		if d.Backend != nil {
			rejectVoterOverride(fmt.Sprintf("duty %q", d.Name), *d.Backend)
		}
	}

	// Build lookup maps for duty and agent configs so we can simulate
	// three-tier backend resolution during assignment validation.
	dutyByName := map[string]DutyConfig{}
	for _, d := range cfg.Duties {
		dutyByName[d.Name] = d
	}
	agentByName := map[string]AgentConfig{}
	for _, ag := range cfg.Agents {
		agentByName[ag.Name] = ag
	}

	for i, a := range cfg.Assignments {
		agentOK := agentNames[a.Agent]
		dutyOK := dutyNames[a.Duty]
		if !agentOK {
			errs = append(errs, fmt.Errorf("assignment[%d]: agent %q not defined", i, a.Agent))
		}
		if !dutyOK {
			errs = append(errs, fmt.Errorf("assignment[%d]: duty %q not defined", i, a.Duty))
		}
		if a.Backend != nil && !backendNames[a.Backend.Name] {
			errs = append(errs, fmt.Errorf("assignment[%d]: backend %q not defined", i, a.Backend.Name))
		}
		if a.Backend != nil {
			rejectVoterOverride(fmt.Sprintf("assignment[%d]", i), *a.Backend)
		}
		// Simulate three-tier resolution: if no backend can be resolved at
		// any tier the assignment will fail at runtime, so reject it here.
		if agentOK && dutyOK && a.Backend == nil {
			duty := dutyByName[a.Duty]
			if duty.Backend == nil {
				agent := agentByName[a.Agent]
				if agent.DefaultBackend.Name == "" {
					errs = append(errs, fmt.Errorf("assignment[%d] (agent=%q duty=%q): no backend resolved — assignment, duty, and agent default_backend are all unset", i, a.Agent, a.Duty))
				}
			}
		}
	}

	return errs
}

// ResolveBackend returns the effective named backend for an assignment.
// Precedence: Assignment.Backend ?? Duty.Backend ?? Agent.DefaultBackend
func ResolveBackend(cfg *Config, assignment AssignmentConfig) (*Backend, domain.BackendRef, error) {
	var ref domain.BackendRef
	switch {
	case assignment.Backend != nil:
		ref = *assignment.Backend
	default:
		// Find the duty's backend, then fall back to the agent's.
		for _, d := range cfg.Duties {
			if d.Name == assignment.Duty {
				if d.Backend != nil {
					ref = *d.Backend
				}
				break
			}
		}
		if ref.Name == "" {
			for _, a := range cfg.Agents {
				if a.Name == assignment.Agent {
					ref = a.DefaultBackend
					break
				}
			}
		}
	}

	if ref.Name == "" {
		return nil, ref, fmt.Errorf("no backend resolved for assignment (agent=%q duty=%q)", assignment.Agent, assignment.Duty)
	}

	for i := range cfg.Backends {
		if cfg.Backends[i].Name == ref.Name {
			b := cfg.Backends[i]
			// Apply ref overrides.
			if ref.Model != "" {
				b.Model = ref.Model
			}
			if ref.Effort != "" {
				b.DefaultEffort = ref.Effort
			}
			return &b, ref, nil
		}
	}
	return nil, ref, fmt.Errorf("backend %q not found in config", ref.Name)
}

// envRefRe matches ${env:VAR_NAME} placeholders.
var envRefRe = regexp.MustCompile(`\$\{env:([^}]+)\}`)

// expandEnvRefs replaces ${env:VAR} with the corresponding environment variable.
// Each placeholder is processed exactly once; the substituted value is never re-scanned,
// preventing both infinite cycles (e.g. FOO=${env:FOO}) and silent injection of nested refs.
// Returns an error listing every variable name that is not set in the environment.
func expandEnvRefs(s string) (string, error) {
	var missing []string
	result := envRefRe.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[6 : len(match)-1] // strip "${env:" prefix and "}" suffix
		val, ok := os.LookupEnv(varName)
		if !ok {
			missing = append(missing, varName)
			return ""
		}
		return val
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("config references unset environment variable(s): %s", strings.Join(missing, ", "))
	}
	return result, nil
}
