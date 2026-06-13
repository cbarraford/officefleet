package gitlab

import (
	"testing"
	"time"
)

func TestGitLabHTTPClientHasTimeout(t *testing.T) {
	if gitlabHTTPClient.Timeout != 30*time.Second {
		t.Fatalf("Timeout = %s, want 30s", gitlabHTTPClient.Timeout)
	}
}
