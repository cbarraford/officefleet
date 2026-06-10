package executor

import (
	"fmt"

	"github.com/cbarraford/office-fleet/internal/config"
)

// FromBackend builds the Executor for a resolved, validated backend.
// This is the single dispatch point the CLI uses; voter panels recurse.
func FromBackend(cfg *config.Config, b *config.Backend) (Executor, error) {
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
		panel := make([]PanelMember, 0, len(b.Panel))
		for _, name := range b.Panel {
			mb := findBackend(cfg, name)
			if mb == nil {
				return nil, fmt.Errorf("voter %q: panel member %q not found in config", b.Name, name)
			}
			member, err := FromBackend(cfg, mb)
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

// findBackend returns the named backend from config, or nil.
func findBackend(cfg *config.Config, name string) *config.Backend {
	for i := range cfg.Backends {
		if cfg.Backends[i].Name == name {
			return &cfg.Backends[i]
		}
	}
	return nil
}
