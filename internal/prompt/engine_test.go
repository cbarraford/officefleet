package prompt_test

import (
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/prompt"
)

func testCtx() prompt.Context {
	return prompt.Context{
		Event:      map[string]any{"mr_iid": "42", "title": "Fix the bug"},
		Agent:      map[string]any{"name": "dev-1", "role": "developer"},
		Duty:       map[string]any{"name": "mr-reviewer"},
		Assignment: map[string]any{"project": "myorg/myrepo"},
		State:      map[string]any{},
		Now:        time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC),
	}
}

func TestRender_Basic(t *testing.T) {
	out, err := prompt.Render("Review MR #{{.Event.mr_iid}} in {{.Assignment.project}}", testCtx())
	if err != nil {
		t.Fatal(err)
	}
	if out != "Review MR #42 in myorg/myrepo" {
		t.Fatalf("unexpected: %q", out)
	}
}

func TestComposePrompts_ThreeLayers(t *testing.T) {
	sys := "You are a {{.Agent.role}} named {{.Agent.name}}."
	task := "Review MR #{{.Event.mr_iid}}."
	extra := "Focus on error handling."
	system, taskOut, err := prompt.ComposePrompts(sys, task, extra, testCtx())
	if err != nil {
		t.Fatal(err)
	}
	if system != "You are a developer named dev-1." {
		t.Fatalf("system: %q", system)
	}
	if !strings.HasPrefix(taskOut, "Review MR #42.") {
		t.Fatalf("task: %q", taskOut)
	}
	if !strings.Contains(taskOut, "Focus on error handling.") {
		t.Fatalf("extra instructions missing: %q", taskOut)
	}
}

func TestComposePrompts_TaskPromptOverride(t *testing.T) {
	sys := "You are a {{.Agent.role}}."
	override := "Perform a security audit of MR #{{.Event.mr_iid}}."
	_, taskOut, err := prompt.ComposePrompts(sys, override, "", testCtx())
	if err != nil {
		t.Fatal(err)
	}
	if taskOut != "Perform a security audit of MR #42." {
		t.Fatalf("override not applied: %q", taskOut)
	}
}

func TestComposePrompts_NoExtra(t *testing.T) {
	sys := "You are a {{.Agent.role}}."
	task := "Review MR."
	system, taskOut, err := prompt.ComposePrompts(sys, task, "", testCtx())
	if err != nil {
		t.Fatal(err)
	}
	if system != "You are a developer." {
		t.Fatalf("system: %q", system)
	}
	if taskOut != "Review MR." {
		t.Fatalf("task: %q", taskOut)
	}
}

func TestRender_Truncate(t *testing.T) {
	out, err := prompt.Render("{{truncate .Event.title 6}}", testCtx())
	if err != nil {
		t.Fatal(err)
	}
	if out != "Fix th..." {
		t.Fatalf("truncate: %q", out)
	}
}
