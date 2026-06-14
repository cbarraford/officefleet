package plugin

import (
	"context"
	"net/http"

	"github.com/cbarraford/office-fleet/internal/domain"
)

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

// WebhookSource is implemented by plugins that accept push ingestion.
// HandleWebhook authenticates and parses one inbound HTTP request and returns
// normalized events. The PLATFORM persists them and writes the HTTP response:
// a returned *AuthError -> 401; any other error -> 400; storage failures -> 500.
type WebhookSource interface {
	HandleWebhook(ctx context.Context, r *http.Request) ([]domain.Event, error)
}

// AuthError marks a webhook authentication failure (-> 401).
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return e.Msg }

// PollSource is implemented by plugins that support interval polling.
// Poll returns events newer than cursor plus the new cursor. An empty cursor
// means "first poll"; the plugin decides its own cursor encoding. On partial
// failure (some sub-sources succeeded) a plugin returns the gathered events
// with the UNCHANGED cursor and a nil error; total failure returns an error.
type PollSource interface {
	Poll(ctx context.Context, cursor string) ([]domain.Event, string, error)
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

// initErrors records the outcome of each plugin's most recent Init call.
var initErrors = map[string]error{}

// RecordInit stores the outcome of a plugin's Init call; a nil err clears any
// previously recorded failure. The run pipeline consults InitError to fail fast
// before an expensive LLM call when an assignment's output plugin is unusable,
// while the daemon itself keeps running — one broken, unused plugin must not
// take down `fleet serve`.
func RecordInit(name string, err error) {
	if err != nil {
		initErrors[name] = err
	} else {
		delete(initErrors, name)
	}
}

// InitError returns the recorded Init failure for a plugin, or nil if it
// initialized successfully or was never initialized.
func InitError(name string) error {
	return initErrors[name]
}
