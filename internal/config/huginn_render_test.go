// internal/config/huginn_render_test.go
package config_test

import (
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/forge"
	"github.com/cbarraford/office-fleet/internal/prompt"
)

func skillPrompt(t *testing.T, name string) string {
	t.Helper()
	t.Setenv("FLEET_DATABASE_DSN", "postgres://test")
	cfg, err := config.Load("../../configs/huginn.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range cfg.Skills {
		if s.Name == name {
			return s.Prompt
		}
	}
	t.Fatalf("skill %q not found", name)
	return ""
}

func TestIssueImplementPrompt_RendersBothForges(t *testing.T) {
	tmpl := skillPrompt(t, "issue-implement")
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantCreate, wantHost, wantLimit string }{
		{"gitlab", "glab mr create --source-branch", "gitlab.com", "--per-page"},
		{"github", "gh pr create --head", "github.com", "--limit"},
	}
	for _, tc := range cases {
		fp, _ := forge.Profile(tc.forge)
		ctx := prompt.Context{
			Assignment: map[string]any{
				"project": "o/r", "base_branch": "main", "max_open_mrs": 3,
				"priority_label_prefix": "priority::", "batch_label": "batch",
				"extra_mr_labels": "", "qg_test_command": "", "qg_lint_command": "",
			},
			Forge: fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		for _, want := range []string{tc.wantCreate, tc.wantHost, tc.wantLimit} {
			if !strings.Contains(out, want) {
				t.Errorf("forge %s: rendered prompt missing %q", tc.forge, want)
			}
		}
	}
}

func codeReviewPrompt(t *testing.T) string {
	t.Helper()
	t.Setenv("FLEET_DATABASE_DSN", "postgres://test")
	cfg, err := config.Load("../../configs/huginn.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range cfg.Skills {
		if s.Name == "code-review" {
			return s.Prompt
		}
	}
	t.Fatal("code-review skill not found in configs/huginn.yaml")
	return ""
}

func TestIssueBatchImplementPrompt_RendersBothForges(t *testing.T) {
	tmpl := skillPrompt(t, "issue-batch-implement")
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantCreate, wantHost string }{
		{"gitlab", "glab mr create --source-branch", "gitlab.com"},
		{"github", "gh pr create --head", "github.com"},
	}
	for _, tc := range cases {
		fp, _ := forge.Profile(tc.forge)
		ctx := prompt.Context{
			Assignment: map[string]any{
				"project": "o/r", "base_branch": "main",
				"batch_label": "batch", "max_batch_issues": 10,
			},
			Forge: fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		if !strings.Contains(out, tc.wantCreate) || !strings.Contains(out, tc.wantHost) {
			t.Errorf("forge %s: missing %q/%q", tc.forge, tc.wantCreate, tc.wantHost)
		}
	}
}

func TestCodeAuditPrompt_RendersBothForges(t *testing.T) {
	tmpl := skillPrompt(t, "code-audit")
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantHost, wantList, wantLimit string }{
		{"gitlab", "gitlab.com", "glab issue list", "--per-page"},
		{"github", "github.com", "gh issue list", "--limit"},
	}
	for _, tc := range cases {
		fp, _ := forge.Profile(tc.forge)
		ctx := prompt.Context{
			Assignment: map[string]any{"project": "o/r", "category": "general-security"},
			Forge:      fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		for _, want := range []string{tc.wantHost, tc.wantList, tc.wantLimit} {
			if !strings.Contains(out, want) {
				t.Errorf("forge %s: missing %q", tc.forge, want)
			}
		}
	}
}

func TestCodeRebasePrompt_RendersBothForges(t *testing.T) {
	tmpl := skillPrompt(t, "code-rebase")
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantHost, wantList string }{
		{"gitlab", "gitlab.com", "glab mr list"},
		{"github", "github.com", "gh pr list"},
	}
	for _, tc := range cases {
		fp, _ := forge.Profile(tc.forge)
		ctx := prompt.Context{
			Assignment: map[string]any{"project": "o/r", "base_branch": "main"},
			Forge:      fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		if !strings.Contains(out, tc.wantHost) || !strings.Contains(out, tc.wantList) {
			t.Errorf("forge %s: missing %q/%q", tc.forge, tc.wantHost, tc.wantList)
		}
	}
}

func TestCIFixPrompt_RendersBothForges(t *testing.T) {
	tmpl := skillPrompt(t, "ci-fix")
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantHost, wantList, wantCI string }{
		{"gitlab", "gitlab.com", "glab mr list", "glab ci"},
		{"github", "github.com", "gh pr list", "gh run"},
	}
	for _, tc := range cases {
		fp, _ := forge.Profile(tc.forge)
		ctx := prompt.Context{
			Assignment: map[string]any{
				"project": "o/r", "base_branch": "main", "max_ci_fix_attempts": 5,
				"regression_command": "", "qg_test_command": "", "qg_lint_command": "",
			},
			Forge: fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		for _, want := range []string{tc.wantHost, tc.wantList, tc.wantCI} {
			if !strings.Contains(out, want) {
				t.Errorf("forge %s: missing %q", tc.forge, want)
			}
		}
	}
}

func TestCodeReviewPrompt_RendersBothForges(t *testing.T) {
	tmpl := codeReviewPrompt(t)
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantHost, wantNoun string }{
		{"gitlab", "gitlab.com", "merge request"},
		{"github", "github.com", "pull request"},
	}
	for _, tc := range cases {
		fp, ok := forge.Profile(tc.forge)
		if !ok {
			t.Fatalf("no profile for %s", tc.forge)
		}
		ctx := prompt.Context{
			Event: map[string]any{
				"mr_iid": 1, "title": "T", "author": "a",
				"source_branch": "s", "target_branch": "main",
			},
			Assignment: map[string]any{"project": "o/r", "min_confidence": 0.5},
			Forge:      fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		if !strings.Contains(out, tc.wantHost) {
			t.Errorf("forge %s: rendered prompt missing host %q", tc.forge, tc.wantHost)
		}
		if !strings.Contains(out, tc.wantNoun) {
			t.Errorf("forge %s: rendered prompt missing noun %q", tc.forge, tc.wantNoun)
		}
	}
}
