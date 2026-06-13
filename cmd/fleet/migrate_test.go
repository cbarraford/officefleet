package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMigrateConfigMissingFileSkipsSeed(t *testing.T) {
	oldConfig := flagConfig
	t.Cleanup(func() { flagConfig = oldConfig })
	flagConfig = filepath.Join(t.TempDir(), "missing-fleet.yaml")

	cfg, haveConfig, err := loadMigrateConfig()
	if err != nil {
		t.Fatalf("loadMigrateConfig: %v", err)
	}
	if haveConfig {
		t.Fatal("haveConfig = true, want false for missing file")
	}
	if cfg != nil {
		t.Fatalf("cfg = %#v, want nil", cfg)
	}
}

func TestLoadMigrateConfigLoadErrorFails(t *testing.T) {
	oldConfig := flagConfig
	t.Cleanup(func() { flagConfig = oldConfig })

	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("database:\n  dsn: ${env:OFFICEFLEET_TEST_MISSING_DSN}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFICEFLEET_TEST_MISSING_DSN", "")
	if err := os.Unsetenv("OFFICEFLEET_TEST_MISSING_DSN"); err != nil {
		t.Fatal(err)
	}
	flagConfig = path

	_, haveConfig, err := loadMigrateConfig()
	if err == nil {
		t.Fatal("expected config load error")
	}
	if haveConfig {
		t.Fatal("haveConfig = true, want false on load error")
	}
	if !strings.Contains(err.Error(), "unset environment") {
		t.Fatalf("err = %v, want unset environment context", err)
	}
}
