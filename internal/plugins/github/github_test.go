package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func initPlugin(t *testing.T, cfg map[string]any, secrets map[string]string) *GitHubPlugin {
	t.Helper()
	p := &GitHubPlugin{}
	lookup := func(name string) (string, error) { return secrets[name], nil }
	if err := p.Init(context.Background(), cfg, lookup); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInit_DefaultsAndConfig(t *testing.T) {
	p := initPlugin(t, map[string]any{}, map[string]string{"github_token": "ghp_x"})
	if p.baseURL != "https://api.github.com" {
		t.Errorf("baseURL = %q", p.baseURL)
	}
	if p.pollInterval != time.Minute {
		t.Errorf("pollInterval = %v", p.pollInterval)
	}
	if p.Name() != "github" {
		t.Errorf("Name = %q", p.Name())
	}

	p2 := initPlugin(t, map[string]any{
		"base_url":      "https://ghe.example.com/api/v3/",
		"poll_interval": "30s",
		"poll_repos":    []any{"org/a", "org/b"},
	}, map[string]string{})
	if p2.baseURL != "https://ghe.example.com/api/v3" {
		t.Errorf("baseURL = %q (trailing slash must be trimmed)", p2.baseURL)
	}
	if p2.pollInterval != 30*time.Second {
		t.Errorf("pollInterval = %v", p2.pollInterval)
	}
	if len(p2.pollRepos) != 2 || p2.pollRepos[0] != "org/a" {
		t.Errorf("pollRepos = %v", p2.pollRepos)
	}

	p3 := &GitHubPlugin{}
	err := p3.Init(context.Background(), map[string]any{"poll_interval": "soon"},
		func(string) (string, error) { return "", nil })
	if err == nil {
		t.Error("invalid poll_interval: expected Init error")
	}
}

func TestPostPRComment(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 1, "html_url": "https://github.com/org/repo/pull/42#issuecomment-1"}`))
	}))
	defer srv.Close()

	p := initPlugin(t, map[string]any{"base_url": srv.URL},
		map[string]string{"github_token": "ghp_x"})
	result, err := p.Do(context.Background(), "post_pr_comment", map[string]any{
		"repo": "org/repo", "pr_number": 42, "body": "LGTM",
	})
	if err != nil {
		t.Fatal(err)
	}
	// GitHub paths use LITERAL slashes in owner/repo (no %2F).
	if gotPath != "/repos/org/repo/issues/42/comments" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer ghp_x" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if gotBody["body"] != "LGTM" {
		t.Errorf("body = %v", gotBody)
	}
	if result["id"] != float64(1) {
		t.Errorf("result = %v", result)
	}
}

func TestPostPRComment_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message": "Not Found"}`))
	}))
	defer srv.Close()
	p := initPlugin(t, map[string]any{"base_url": srv.URL},
		map[string]string{"github_token": "ghp_x"})

	if _, err := p.Do(context.Background(), "post_pr_comment", map[string]any{
		"repo": "org/repo", "pr_number": 42, "body": "x",
	}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want 404 mention", err)
	}
	if _, err := p.Do(context.Background(), "post_pr_comment",
		map[string]any{"repo": "org/repo"}); err == nil {
		t.Error("missing params: expected error")
	}
	if _, err := p.Do(context.Background(), "nope", map[string]any{}); err == nil {
		t.Error("unknown action: expected error")
	}
}
