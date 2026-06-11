package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestPlugin returns a GitLabPlugin pointed at srvURL with a test token.
func newTestPlugin(srvURL string) *GitLabPlugin {
	return &GitLabPlugin{token: "tok", baseURL: srvURL}
}

func TestPostInlineComment(t *testing.T) {
	var discussionBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{proj}/merge_requests/42/versions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"head_committed_sha": "h", "base_commit_sha": "b", "start_commit_sha": "s",
		}})
	})
	mux.HandleFunc("POST /api/v4/projects/{proj}/merge_requests/42/discussions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&discussionBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": "d1"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	out, err := g.Do(context.Background(), "post_inline_comment", map[string]any{
		"project": "org/repo", "mr_iid": "42", "path": "a.go", "line": float64(7), "body": "nil deref",
	})
	if err != nil {
		t.Fatal(err)
	}
	pos, _ := discussionBody["position"].(map[string]any)
	if pos == nil {
		t.Fatalf("no position in discussion payload: %#v", discussionBody)
	}
	if pos["new_path"] != "a.go" || pos["new_line"] != "7" && pos["new_line"] != float64(7) && pos["new_line"] != 7 {
		t.Errorf("position = %#v", pos)
	}
	if pos["head_sha"] != "h" || pos["base_sha"] != "b" || pos["start_sha"] != "s" {
		t.Errorf("position SHAs = %#v", pos)
	}
	if discussionBody["body"] != "nil deref" {
		t.Errorf("body = %v", discussionBody["body"])
	}
	if out["fallback"] != nil {
		t.Errorf("happy path must not record a fallback: %#v", out)
	}
}

func TestPostInlineCommentFallsBackToNote(t *testing.T) {
	var noteBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{proj}/merge_requests/42/versions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"head_committed_sha": "h", "base_commit_sha": "b", "start_commit_sha": "s"}})
	})
	mux.HandleFunc("POST /api/v4/projects/{proj}/merge_requests/42/discussions", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message": "line_code not found"}`, http.StatusBadRequest) // stale position
	})
	mux.HandleFunc("POST /api/v4/projects/{proj}/merge_requests/42/notes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&noteBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 9}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	out, err := g.Do(context.Background(), "post_inline_comment", map[string]any{
		"project": "org/repo", "mr_iid": "42", "path": "a.go", "line": "7", "body": "nil deref",
	})
	if err != nil {
		t.Fatalf("fallback path must succeed: %v", err)
	}
	body, _ := noteBody["body"].(string)
	if !strings.Contains(body, "a.go:7") || !strings.Contains(body, "nil deref") {
		t.Errorf("fallback note body = %q, want path:line prefix + original body", body)
	}
	if out["fallback"] != "note" {
		t.Errorf("result must record the fallback: %#v", out)
	}
}

func TestPostInlineCommentBothFail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{proj}/merge_requests/42/versions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"head_committed_sha": "h", "base_commit_sha": "b", "start_commit_sha": "s"}})
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	if _, err := g.Do(context.Background(), "post_inline_comment", map[string]any{
		"project": "org/repo", "mr_iid": "42", "path": "a.go", "line": "7", "body": "x",
	}); err == nil {
		t.Fatal("expected error when both inline and fallback fail")
	}
}

func TestCreateIssue(t *testing.T) {
	var issueBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v4/projects/{proj}/issues", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&issueBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"iid": 17}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	out, err := g.Do(context.Background(), "create_issue", map[string]any{
		"project": "org/repo", "title": "SQL injection in search", "description": "evidence...", "labels": "code-audit,security",
	})
	if err != nil {
		t.Fatal(err)
	}
	if issueBody["title"] != "SQL injection in search" || issueBody["labels"] != "code-audit,security" {
		t.Errorf("issue payload = %#v", issueBody)
	}
	if out["iid"] != float64(17) {
		t.Errorf("result = %#v", out)
	}
}

func TestCreateIssueRequiresFields(t *testing.T) {
	g := newTestPlugin("http://unused")
	if _, err := g.Do(context.Background(), "create_issue", map[string]any{"project": "p"}); err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestReplyToDiscussion(t *testing.T) {
	var replyBody map[string]any
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&replyBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 3}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestPlugin(srv.URL)
	if _, err := g.Do(context.Background(), "reply_to_discussion", map[string]any{
		"project": "org/repo", "mr_iid": "42", "discussion_id": "abc123", "body": "fixed in rev 2",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotPath, "/merge_requests/42/discussions/abc123/notes") {
		t.Errorf("path = %s", gotPath)
	}
	if replyBody["body"] != "fixed in rev 2" {
		t.Errorf("reply body = %#v", replyBody)
	}
}
