package plugin

import "context"

// SecretLookup resolves a named secret to its plaintext value.
type SecretLookup func(name string) (string, error)

// Schema describes a JSON schema for plugin or assignment config.
type Schema map[string]any

// EventSource describes a named inbound event surface a plugin can emit.
type EventSource struct {
	Name        string
	Description string
}

// Action describes a named output capability a plugin provides.
type Action struct {
	Name        string
	Description string
}

// Plugin is the interface every integration plugin must implement.
// Plugins self-register via Register() at init time.
type Plugin interface {
	Name() string
	EventSources() []EventSource
	Actions() []Action
	ConfigSchema() Schema
	Init(ctx context.Context, cfg map[string]any, secrets SecretLookup) error
	Do(ctx context.Context, action string, params map[string]any) (map[string]any, error)
}

var registry = map[string]Plugin{}

// Register adds a plugin to the global registry. Called from each plugin's init().
func Register(p Plugin) {
	registry[p.Name()] = p
}

// Get returns a registered plugin by name.
func Get(name string) (Plugin, bool) {
	p, ok := registry[name]
	return p, ok
}

// All returns all registered plugins.
func All() []Plugin {
	out := make([]Plugin, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	return out
}
