package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCmdPrintsBuildMetadata(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := version, commit, buildDate
	version, commit, buildDate = "v1.2.3", "abc1234", "2026-06-12T10:00:00Z"
	t.Cleanup(func() {
		version, commit, buildDate = oldVersion, oldCommit, oldBuildDate
	})

	cmd := versionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	for _, want := range []string{"version: v1.2.3", "commit: abc1234", "build_date: 2026-06-12T10:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("version output %q missing %q", got, want)
		}
	}
}
