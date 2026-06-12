package executor

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/cbarraford/office-fleet/internal/config"
)

// SecretLookup resolves a named secret to its plaintext value.
type SecretLookup func(ctx context.Context, name string) (string, error)

// FromBackend builds the Executor for a resolved, validated backend.
// This is the single dispatch point the CLI uses; voter panels recurse.
func FromBackend(cfg *config.Config, b *config.Backend) (Executor, error) {
	return fromBackend(context.Background(), cfg, b, nil)
}

// FromBackendWithSecrets builds an Executor after resolving backend api_key
// ${secret:name} references through lookup. The config object is not mutated.
func FromBackendWithSecrets(ctx context.Context, cfg *config.Config, b *config.Backend, lookup SecretLookup) (Executor, error) {
	return fromBackend(ctx, cfg, b, lookup)
}

func fromBackend(ctx context.Context, cfg *config.Config, b *config.Backend, lookup SecretLookup) (Executor, error) {
	b, err := resolveBackendSecrets(ctx, b, lookup)
	if err != nil {
		return nil, err
	}
	switch b.Kind {
	case "claude":
		var apiKey string
		if b.Auth.Mode == "api_key" {
			apiKey = b.Auth.APIKey
		}
		return NewClaudeExecutor(apiKey), nil
	case "openai-compatible":
		return NewEndpointExecutor(b)
	case "voter":
		if b.Strategy != "first_success" && b.Strategy != "majority" {
			return nil, fmt.Errorf("voter %q: unknown strategy %q (want first_success or majority)", b.Name, b.Strategy)
		}
		panel := make([]PanelMember, 0, len(b.Panel))
		for _, name := range b.Panel {
			mb := findBackend(cfg, name)
			if mb == nil {
				return nil, fmt.Errorf("voter %q: panel member %q not found in config", b.Name, name)
			}
			member, err := fromBackend(ctx, cfg, mb, lookup)
			if err != nil {
				return nil, fmt.Errorf("voter %q: panel member %q: %w", b.Name, name, err)
			}
			panel = append(panel, PanelMember{
				Name:   mb.Name,
				Exec:   member,
				Model:  mb.Model,
				Effort: mb.DefaultEffort,
			})
		}
		return NewVotingExecutor(panel, b.Strategy), nil
	default:
		return nil, fmt.Errorf("unsupported backend kind %q", b.Kind)
	}
}

// BackendUsesSecretRefs reports whether a backend or any voter panel member has
// an api_key configured as a ${secret:name} reference.
func BackendUsesSecretRefs(cfg *config.Config, b *config.Backend) bool {
	return backendUsesSecretRefs(cfg, b, map[string]bool{})
}

func backendUsesSecretRefs(cfg *config.Config, b *config.Backend, seen map[string]bool) bool {
	if b == nil {
		return false
	}
	if b.Auth.Mode == "api_key" {
		if _, ok := parseSecretRef(b.Auth.APIKey); ok {
			return true
		}
	}
	if b.Kind != "voter" {
		return false
	}
	for _, name := range b.Panel {
		if seen[name] {
			continue
		}
		seen[name] = true
		if backendUsesSecretRefs(cfg, findBackend(cfg, name), seen) {
			return true
		}
	}
	return false
}

// findBackend returns the named backend from config, or nil.
func findBackend(cfg *config.Config, name string) *config.Backend {
	for i := range cfg.Backends {
		if cfg.Backends[i].Name == name {
			return &cfg.Backends[i]
		}
	}
	return nil
}

func resolveBackendSecrets(ctx context.Context, b *config.Backend, lookup SecretLookup) (*config.Backend, error) {
	if b == nil {
		return nil, nil
	}
	resolved := *b
	if resolved.Auth.Mode != "api_key" {
		return &resolved, nil
	}
	name, ok := parseSecretRef(resolved.Auth.APIKey)
	if !ok {
		if strings.Contains(resolved.Auth.APIKey, "${secret:") {
			return nil, fmt.Errorf("backend %q: malformed api_key secret reference", resolved.Name)
		}
		return &resolved, nil
	}
	if lookup == nil {
		return nil, fmt.Errorf("backend %q: api_key secret %q requires a secret lookup", resolved.Name, name)
	}
	value, err := lookup(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("backend %q: resolve api_key secret %q: %w", resolved.Name, name, err)
	}
	if value == "" {
		return nil, fmt.Errorf("backend %q: api_key secret %q resolved to an empty value", resolved.Name, name)
	}
	resolved.Auth.APIKey = value
	return &resolved, nil
}

var secretRefRe = regexp.MustCompile(`^\$\{secret:([^}]+)\}$`)

func parseSecretRef(value string) (string, bool) {
	m := secretRefRe.FindStringSubmatch(strings.TrimSpace(value))
	if len(m) != 2 || m[1] == "" {
		return "", false
	}
	return m[1], true
}
