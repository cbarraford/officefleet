package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"
)

// Context is the data available inside a prompt template. Secrets are
// deliberately NOT a field: they would render via {{.Secrets.x}} / {{json .}}
// and land verbatim in run records and delivered bodies (issue #4). Secret
// values are reachable ONLY through the `secret` helper, which Render is given
// out of band — pass nil to deny secret access entirely (output-param rendering).
type Context struct {
	Event      map[string]any
	Agent      map[string]any
	Skill       map[string]any
	Assignment map[string]any
	State      map[string]any
	// Forge is the static per-provider profile (internal/forge) selected by the
	// assignment's `forge` config key; prompts read it as {{.Forge.cli}} etc.
	Forge map[string]any
	Now   time.Time
	// Item is the current fan-out element during for_each output delivery
	// (nil outside fan-out rendering).
	Item map[string]any
}

// Render executes a Go text/template with the given context. secrets backs the
// `secret` helper; pass nil to make `secret` fail (deny secret access).
func Render(tmpl string, ctx Context, secrets map[string]string) (string, error) {
	t, err := template.New("prompt").Funcs(helpers(secrets)).Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// ComposePrompts renders the three-layer prompt composition:
//
//	system = Agent.system_prompt (rendered)
//	task   = task_prompt_override ?? Skill.prompt (rendered)
//	add-on = extra_instructions appended after task (if set)
func ComposePrompts(
	systemTemplate string,
	taskTemplate string,
	extraInstructions string,
	ctx Context,
	secrets map[string]string,
) (system, task string, err error) {
	system, err = Render(systemTemplate, ctx, secrets)
	if err != nil {
		return "", "", fmt.Errorf("render system prompt: %w", err)
	}
	task, err = Render(taskTemplate, ctx, secrets)
	if err != nil {
		return "", "", fmt.Errorf("render task prompt: %w", err)
	}
	if extra := strings.TrimSpace(extraInstructions); extra != "" {
		renderedExtra, err := Render(extra, ctx, secrets)
		if err != nil {
			return "", "", fmt.Errorf("render extra instructions: %w", err)
		}
		task = task + "\n\n" + renderedExtra
	}
	return system, task, nil
}

func helpers(secrets map[string]string) template.FuncMap {
	return template.FuncMap{
		"date": func() string { return time.Now().Format("2006-01-02") },
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"default": func(def, val string) string {
			if val == "" {
				return def
			}
			return val
		},
		"secret": func(name string) (string, error) {
			if secrets == nil {
				return "", fmt.Errorf("secrets not available in this execution context")
			}
			val, ok := secrets[name]
			if !ok {
				return "", fmt.Errorf("secret %q not found", name)
			}
			return val, nil
		},
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return "", fmt.Errorf("json marshal: %w", err)
			}
			return string(b), nil
		},
		"fetch": func(pluginName, action string, params map[string]any) (any, error) {
			return nil, fmt.Errorf("fetch helper not available in this execution context")
		},
	}
}
