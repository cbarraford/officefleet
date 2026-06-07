package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/cbarraford/office-fleet/internal/domain"
	"gopkg.in/yaml.v3"
)

// BackendAuth describes how a backend authenticates with its provider.
type BackendAuth struct {
	Mode   string `yaml:"mode"`             // subscription | api_key | none
	APIKey string `yaml:"api_key,omitempty"` // may be ${secret:name} reference
}

// Backend is a named, configured LLM provider instance.
type Backend struct {
	Name          string            `yaml:"name"`
	Kind          string            `yaml:"kind"` // claude | openai-compatible
	Auth          BackendAuth       `yaml:"auth"`
	BaseURI       string            `yaml:"base_uri,omitempty"`
	Model         string            `yaml:"model,omitempty"`
	DefaultEffort string            `yaml:"default_effort,omitempty"`
	Params        map[string]any    `yaml:"params,omitempty"`
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
	Name          string                     `yaml:"name"`
	Role          string                     `yaml:"role"`
	Description   string                     `yaml:"description"`
	TriggerKinds  []string                   `yaml:"trigger_kinds"`
	Prompt        string                     `yaml:"prompt"`
	RequiredTools []string                   `yaml:"required_tools"`
	OutputActions []domain.OutputActionType  `yaml:"output_actions"`
	ConfigSchema  map[string]any             `yaml:"config_schema,omitempty"`
	Backend       *domain.BackendRef         `yaml:"backend,omitempty"`
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
	expanded := expandEnvRefs(string(data))

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
	for _, b := range cfg.Backends {
		if b.Name == "" {
			errs = append(errs, fmt.Errorf("backend missing name"))
			continue
		}
		if backendNames[b.Name] {
			errs = append(errs, fmt.Errorf("duplicate backend name: %q", b.Name))
		}
		backendNames[b.Name] = true
		switch b.Kind {
		case "claude", "openai-compatible":
		default:
			errs = append(errs, fmt.Errorf("backend %q: unknown kind %q", b.Name, b.Kind))
		}
		switch b.Auth.Mode {
		case "subscription", "api_key", "none", "":
		default:
			errs = append(errs, fmt.Errorf("backend %q: unknown auth mode %q", b.Name, b.Auth.Mode))
		}
	}

	agentNames := map[string]bool{}
	for _, a := range cfg.Agents {
		if a.Name == "" {
			errs = append(errs, fmt.Errorf("agent missing name"))
			continue
		}
		agentNames[a.Name] = true
		if a.DefaultBackend.Name != "" && !backendNames[a.DefaultBackend.Name] {
			errs = append(errs, fmt.Errorf("agent %q: default_backend %q not defined", a.Name, a.DefaultBackend.Name))
		}
	}

	dutyNames := map[string]bool{}
	for _, d := range cfg.Duties {
		if d.Name == "" {
			errs = append(errs, fmt.Errorf("duty missing name"))
			continue
		}
		dutyNames[d.Name] = true
		if d.Backend != nil && !backendNames[d.Backend.Name] {
			errs = append(errs, fmt.Errorf("duty %q: backend %q not defined", d.Name, d.Backend.Name))
		}
	}

	for i, a := range cfg.Assignments {
		if !agentNames[a.Agent] {
			errs = append(errs, fmt.Errorf("assignment[%d]: agent %q not defined", i, a.Agent))
		}
		if !dutyNames[a.Duty] {
			errs = append(errs, fmt.Errorf("assignment[%d]: duty %q not defined", i, a.Duty))
		}
		if a.Backend != nil && !backendNames[a.Backend.Name] {
			errs = append(errs, fmt.Errorf("assignment[%d]: backend %q not defined", i, a.Backend.Name))
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

// expandEnvRefs replaces ${env:VAR} with the corresponding environment variable.
func expandEnvRefs(s string) string {
	for {
		start := strings.Index(s, "${env:")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end == -1 {
			break
		}
		placeholder := s[start : start+end+1]
		varName := s[start+6 : start+end]
		s = strings.ReplaceAll(s, placeholder, os.Getenv(varName))
	}
	return s
}
