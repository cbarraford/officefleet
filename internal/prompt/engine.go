package prompt

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"time"
)

// Context is the data available inside a prompt template.
type Context struct {
	Event      map[string]any
	Agent      map[string]any
	Duty       map[string]any
	Assignment map[string]any
	State      map[string]any
	Now        time.Time
}

// Render executes a Go text/template with the given context.
func Render(tmpl string, ctx Context) (string, error) {
	t, err := template.New("prompt").Funcs(helpers()).Parse(tmpl)
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
//	task   = task_prompt_override ?? Duty.prompt (rendered)
//	add-on = extra_instructions appended after task (if set)
func ComposePrompts(
	systemTemplate string,
	taskTemplate string,
	extraInstructions string,
	ctx Context,
) (system, task string, err error) {
	system, err = Render(systemTemplate, ctx)
	if err != nil {
		return "", "", fmt.Errorf("render system prompt: %w", err)
	}
	task, err = Render(taskTemplate, ctx)
	if err != nil {
		return "", "", fmt.Errorf("render task prompt: %w", err)
	}
	if extra := strings.TrimSpace(extraInstructions); extra != "" {
		renderedExtra, err := Render(extra, ctx)
		if err != nil {
			return "", "", fmt.Errorf("render extra instructions: %w", err)
		}
		task = task + "\n\n" + renderedExtra
	}
	return system, task, nil
}

func helpers() template.FuncMap {
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
	}
}
