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

func TestPostChangeComment_LinguaFrancaParams(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	_, err := g.Do(context.Background(), "post_change_comment", map[string]any{
		"project": "org/repo", "mr_iid": "9", "body": "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/repos/org/repo/issues/9/comments" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["body"] != "hi" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestPostInlineComment_Github(t *testing.T) {
	var postPayload map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/pulls/9", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"head":{"sha":"abc123"}}`))
	})
	mux.HandleFunc("/repos/org/repo/pulls/9/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&postPayload)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":5}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	_, err := g.Do(context.Background(), "post_inline_comment", map[string]any{
		"project": "org/repo", "mr_iid": "9", "path": "main.go", "line": "12", "body": "bug",
	})
	if err != nil {
		t.Fatal(err)
	}
	if postPayload["commit_id"] != "abc123" || postPayload["path"] != "main.go" {
		t.Errorf("payload = %v", postPayload)
	}
	if postPayload["line"] != float64(12) { // JSON numbers decode to float64
		t.Errorf("line = %v (%T), want 12", postPayload["line"], postPayload["line"])
	}
	if postPayload["side"] != "RIGHT" {
		t.Errorf("side = %v, want RIGHT", postPayload["side"])
	}
}

func TestPostInlineComment_Github_FallbackOnStalePosition(t *testing.T) {
	var fellBack bool
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/pulls/9", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"head":{"sha":"abc123"}}`))
	})
	mux.HandleFunc("/repos/org/repo/pulls/9/comments", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity) // stale line position
		_, _ = w.Write([]byte(`{"message":"position invalid"}`))
	})
	mux.HandleFunc("/repos/org/repo/issues/9/comments", func(w http.ResponseWriter, r *http.Request) {
		fellBack = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":7}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	res, err := g.Do(context.Background(), "post_inline_comment", map[string]any{
		"project": "org/repo", "mr_iid": "9", "path": "main.go", "line": "12", "body": "bug",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fellBack {
		t.Error("expected fallback to the issue-comment endpoint")
	}
	if res["fallback"] != "note" {
		t.Errorf("res = %v, want fallback=note", res)
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

func TestCreateIssue_Github(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":42}`))
	}))
	defer srv.Close()

	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	_, err := g.Do(context.Background(), "create_issue", map[string]any{
		"project": "org/repo", "title": "[Security] bug", "description": "found at x.go:10",
		"labels": "security, huginn-code-audit, general-security",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/repos/org/repo/issues" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["title"] != "[Security] bug" || gotBody["body"] != "found at x.go:10" {
		t.Errorf("title/body = %v", gotBody)
	}
	labels, ok := gotBody["labels"].([]any)
	if !ok || len(labels) != 3 || labels[0] != "security" || labels[1] != "huginn-code-audit" {
		t.Errorf("labels not split into trimmed array: %v", gotBody["labels"])
	}
}

func TestReplyToDiscussion_Github_ReviewThread(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	_, err := g.Do(context.Background(), "reply_to_discussion", map[string]any{
		"project": "org/repo", "mr_iid": "12", "discussion_id": "777", "body": "thanks",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/repos/org/repo/pulls/12/comments/777/replies" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["body"] != "thanks" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestReplyToDiscussion_Github_NoThreadFallsBackToIssueComment(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	_, err := g.Do(context.Background(), "reply_to_discussion", map[string]any{
		"project": "org/repo", "mr_iid": "12", "discussion_id": "", "body": "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/repos/org/repo/issues/12/comments" {
		t.Errorf("path = %q (expected issue-comment fallback)", gotPath)
	}
}

func TestCreateIssue_Github_NoLabels(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer srv.Close()
	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	if _, err := g.Do(context.Background(), "create_issue", map[string]any{
		"project": "org/repo", "title": "t",
	}); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["labels"]; present {
		t.Errorf("labels should be omitted when empty, got %v", gotBody["labels"])
	}
}
