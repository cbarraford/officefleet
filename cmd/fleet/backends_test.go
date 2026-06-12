package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendsLoginHelpOnlyNamesSupportedClaudeCLI(t *testing.T) {
	cmd := backendsLoginCmd()
	if strings.Contains(cmd.Short, "codex") || strings.Contains(cmd.Short, "gemini") {
		t.Fatalf("login help advertises unsupported backends: %q", cmd.Short)
	}
	if !strings.Contains(strings.ToLower(cmd.Short), "claude") {
		t.Fatalf("login help should mention the supported Claude CLI backend: %q", cmd.Short)
	}
}

func TestBackendsLoginRejectsUnsupportedSubscriptionKindBeforeExec(t *testing.T) {
	oldConfig := flagConfig
	t.Cleanup(func() { flagConfig = oldConfig })

	path := filepath.Join(t.TempDir(), "fleet.yaml")
	content := []byte(`
backends:
  - name: unsupported
    kind: unsupported-cli
    auth: {mode: subscription}
`)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	flagConfig = path

	cmd := backendsLoginCmd()
	err := cmd.RunE(cmd, []string{"unsupported"})
	if err == nil {
		t.Fatal("expected unsupported kind error")
	}
	if !strings.Contains(err.Error(), "only supported for claude CLI backends") {
		t.Fatalf("error = %q, want unsupported claude-only message", err)
	}
}
