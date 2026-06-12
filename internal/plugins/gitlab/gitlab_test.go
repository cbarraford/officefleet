package gitlab_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbarraford/office-fleet/internal/plugin"
	_ "github.com/cbarraford/office-fleet/internal/plugins/gitlab"
)

func TestGitLabPlugin_InitSecretError(t *testing.T) {
	p, ok := plugin.Get("gitlab")
	if !ok {
		t.Fatal("gitlab plugin not registered")
	}
	secrets := func(name string) (string, error) { return "", errors.New("secret not found") }
	err := p.Init(context.Background(), nil, secrets)
	if err == nil {
		t.Fatal("expected error when SecretLookup fails, got nil")
	}
}

func TestGitLabPlugin_InitRejectsEmptyToken(t *testing.T) {
	p, ok := plugin.Get("gitlab")
	if !ok {
		t.Fatal("gitlab plugin not registered")
	}
	secrets := func(name string) (string, error) { return "", nil }
	err := p.Init(context.Background(), nil, secrets)
	if err == nil {
		t.Fatal("expected error when gitlab_token is empty")
	}
	if !strings.Contains(err.Error(), "gitlab_token") {
		t.Fatalf("err = %v, want gitlab_token context", err)
	}
}

func TestGitLabPlugin_InitAllowsMissingWebhookSecret(t *testing.T) {
	p, ok := plugin.Get("gitlab")
	if !ok {
		t.Fatal("gitlab plugin not registered")
	}
	secrets := func(name string) (string, error) {
		switch name {
		case "gitlab_token":
			return "test-token", nil
		case "gitlab_webhook_secret":
			return "", &plugin.SecretNotFoundError{Name: name}
		}
		return "", nil
	}
	if err := p.Init(context.Background(), nil, secrets); err != nil {
		t.Fatalf("Init returned error for missing optional webhook secret: %v", err)
	}
}

func TestGitLabPlugin_PostMRComment(t *testing.T) {
	var capturedBody string
	var capturedPath string
	var capturedToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.RawPath
		capturedToken = r.Header.Get("PRIVATE-TOKEN")
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		capturedBody = payload["body"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 99, "body": capturedBody})
	}))
	defer srv.Close()

	p, ok := plugin.Get("gitlab")
	if !ok {
		t.Fatal("gitlab plugin not registered")
	}
	secrets := func(name string) (string, error) { return "test-token", nil }
	if err := p.Init(context.Background(), map[string]any{"base_url": srv.URL}, secrets); err != nil {
		t.Fatal(err)
	}
	result, err := p.Do(context.Background(), "post_mr_comment", map[string]any{
		"project": "myorg/myrepo",
		"mr_iid":  "42",
		"body":    "LGTM",
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedBody != "LGTM" {
		t.Fatalf("body not sent: %q", capturedBody)
	}
	if capturedToken != "test-token" {
		t.Fatalf("token not sent: %q", capturedToken)
	}
	if capturedPath != "/api/v4/projects/myorg%2Fmyrepo/merge_requests/42/notes" {
		t.Fatalf("unexpected path: %q", capturedPath)
	}
	if result["id"].(float64) != 99 {
		t.Fatalf("unexpected result: %v", result)
	}
}

func TestGitLabPlugin_MissingParams(t *testing.T) {
	p, _ := plugin.Get("gitlab")
	secrets := func(name string) (string, error) { return "tok", nil }
	_ = p.Init(context.Background(), nil, secrets)
	_, err := p.Do(context.Background(), "post_mr_comment", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing params")
	}
}

func TestGitLabPlugin_UnknownAction(t *testing.T) {
	p, _ := plugin.Get("gitlab")
	secrets := func(name string) (string, error) { return "tok", nil }
	_ = p.Init(context.Background(), nil, secrets)
	_, err := p.Do(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestGitLabPlugin_PartialMissingParams(t *testing.T) {
	p, _ := plugin.Get("gitlab")
	secrets := func(name string) (string, error) { return "tok", nil }
	_ = p.Init(context.Background(), nil, secrets)

	tests := []struct {
		name   string
		params map[string]any
	}{
		{
			name:   "missing project",
			params: map[string]any{"mr_iid": "42", "body": "hello"},
		},
		{
			name:   "missing mr_iid",
			params: map[string]any{"project": "myorg/myrepo", "body": "hello"},
		},
		{
			name:   "missing body",
			params: map[string]any{"project": "myorg/myrepo", "mr_iid": "42"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Do(context.Background(), "post_mr_comment", tc.params)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestGitLabPlugin_NumericMRIID(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.RawPath
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 42})
	}))
	defer srv.Close()

	p, _ := plugin.Get("gitlab")
	secrets := func(name string) (string, error) { return "tok", nil }
	if err := p.Init(context.Background(), map[string]any{"base_url": srv.URL}, secrets); err != nil {
		t.Fatal(err)
	}

	t.Run("float64 mr_iid", func(t *testing.T) {
		_, err := p.Do(context.Background(), "post_mr_comment", map[string]any{
			"project": "myorg/myrepo",
			"mr_iid":  float64(42),
			"body":    "LGTM",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(capturedPath, "/projects/myorg%2Fmyrepo/merge_requests/42/notes") {
			t.Fatalf("unexpected path: %q", capturedPath)
		}
	})

	t.Run("int mr_iid", func(t *testing.T) {
		_, err := p.Do(context.Background(), "post_mr_comment", map[string]any{
			"project": "myorg/myrepo",
			"mr_iid":  int(42),
			"body":    "LGTM",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(capturedPath, "/projects/myorg%2Fmyrepo/merge_requests/42/notes") {
			t.Fatalf("unexpected path: %q", capturedPath)
		}
	})
}

func TestGitLabPlugin_StubActions(t *testing.T) {
	p, _ := plugin.Get("gitlab")
	secrets := func(name string) (string, error) { return "tok", nil }
	_ = p.Init(context.Background(), nil, secrets)

	actions := []string{"resolve_discussion"}
	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			_, err := p.Do(context.Background(), action, nil)
			if err == nil {
				t.Fatalf("expected error for stub action %q", action)
			}
		})
	}
}
