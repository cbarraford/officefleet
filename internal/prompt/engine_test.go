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
		Secrets:    map[string]string{},
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

// TEST 1 - json helper (gap M15)
func TestRender_JSONHelper(t *testing.T) {
	ctx := testCtx()
	ctx.Event["data"] = map[string]any{"repo": "myorg/myrepo", "count": 7}
	out, err := prompt.Render(`{{json .Event.data}}`, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "repo") {
		t.Fatalf("json output missing key 'repo': %q", out)
	}
	if !strings.Contains(out, "myorg/myrepo") {
		t.Fatalf("json output missing value 'myorg/myrepo': %q", out)
	}
}

// TEST 2 - secret helper success (gap m8)
func TestRender_SecretSuccess(t *testing.T) {
	ctx := testCtx()
	ctx.Secrets["gitlab_token"] = "tok-abc"
	out, err := prompt.Render(`{{secret "gitlab_token"}}`, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out != "tok-abc" {
		t.Fatalf("expected 'tok-abc', got %q", out)
	}
}

// TEST 3 - secret helper missing key returns error (gap m8)
func TestRender_SecretMissingKey(t *testing.T) {
	ctx := testCtx()
	// Secrets is empty map — key does not exist
	_, err := prompt.Render(`{{secret "missing"}}`, ctx)
	if err == nil {
		t.Fatal("expected error for missing secret key, got nil")
	}
}

// TEST 4 - fetch helper is a stub returning error
func TestRender_FetchStubReturnsError(t *testing.T) {
	ctx := testCtx()
	_, err := prompt.Render(`{{fetch "gitlab" "get_mr" nil}}`, ctx)
	if err == nil {
		t.Fatal("expected error from fetch stub, got nil")
	}
}
