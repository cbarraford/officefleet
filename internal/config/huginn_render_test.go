// internal/config/huginn_render_test.go
package config_test

import (
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/forge"
	"github.com/cbarraford/office-fleet/internal/prompt"
)

func codeReviewPrompt(t *testing.T) string {
	t.Helper()
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
