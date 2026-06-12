package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendsLoginRejectsUnvalidatedBackendKind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(path, []byte(`
backends:
  - name: unsafe
    kind: sh
    auth:
      mode: subscription
`), 0600); err != nil {
		t.Fatal(err)
	}

	oldConfig := flagConfig
	flagConfig = path
	t.Cleanup(func() { flagConfig = oldConfig })

	cmd := backendsLoginCmd()
	cmd.SetArgs([]string{"unsafe"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid backend kind to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid config") && !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("error = %q, want validation error", err.Error())
	}
}
