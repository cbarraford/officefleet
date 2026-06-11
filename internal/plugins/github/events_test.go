package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

const prWebhookFixture = `{
  "action": "opened",
  "pull_request": {
    "number": 42,
    "title": "Add feature",
    "merged": false,
    "html_url": "https://github.com/org/repo/pull/42",
    "head": {"ref": "feat/x", "sha": "abc123def"},
    "base": {"ref": "main"},
    "user": {"login": "alice"}
  },
  "repository": {"full_name": "org/repo"}
}`

func sign(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func eventsPlugin(t *testing.T, webhookSecret string) *GitHubPlugin {
	t.Helper()
	return initPlugin(t, map[string]any{}, map[string]string{
		"github_token":          "ghp_x",
		"github_webhook_secret": webhookSecret,
	})
}

func webhookReq(body, signature, eventName string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	if signature != "" {
		r.Header.Set("X-Hub-Signature-256", signature)
	}
	r.Header.Set("X-GitHub-Event", eventName)
	return r
}

func TestHandleWebhook_ValidPR(t *testing.T) {
	g := eventsPlugin(t, "s3cret")
	evs, err := g.HandleWebhook(context.Background(),
		webhookReq(prWebhookFixture, sign(prWebhookFixture, "s3cret"), "pull_request"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.SourcePlugin != "github" || ev.EventType != "pr_opened" {
		t.Errorf("envelope = %s/%s", ev.SourcePlugin, ev.EventType)
	}
	if ev.Identity != "alice" {
		t.Errorf("identity = %q", ev.Identity)
	}
	if ev.DedupKey != "pr:org/repo:42:abc123def" {
		t.Errorf("dedup_key = %q", ev.DedupKey)
	}
	for k, want := range map[string]any{
		"repo": "org/repo", "pr_number": 42, "title": "Add feature", "action": "opened",
		"source_branch": "feat/x", "target_branch": "main",
		"head_sha": "abc123def", "author": "alice",
		"url": "https://github.com/org/repo/pull/42",
	} {
		if ev.PayloadNorm[k] != want {
			t.Errorf("payload_norm[%q] = %v (%T), want %v", k, ev.PayloadNorm[k], ev.PayloadNorm[k], want)
		}
	}
	if len(ev.PayloadRaw) == 0 {
		t.Error("payload_raw empty")
	}
}

func TestHandleWebhook_ActionMapping(t *testing.T) {
	g := eventsPlugin(t, "s3cret")
	cases := []struct {
		action   string
		merged   bool
		wantType string // "" = ignored
	}{
		{"opened", false, "pr_opened"},
		{"synchronize", false, "pr_updated"},
		{"closed", true, "pr_merged"},
		{"closed", false, "pr_closed"},
		{"reopened", false, ""},
		{"labeled", false, ""},
	}
	for _, c := range cases {
		body := strings.Replace(prWebhookFixture, `"action": "opened"`, `"action": "`+c.action+`"`, 1)
		if c.merged {
			body = strings.Replace(body, `"merged": false`, `"merged": true`, 1)
		}
		evs, err := g.HandleWebhook(context.Background(),
			webhookReq(body, sign(body, "s3cret"), "pull_request"))
		if err != nil {
			t.Fatalf("action %s: %v", c.action, err)
		}
		if c.wantType == "" {
			if len(evs) != 0 {
				t.Errorf("action %s: events = %d, want ignored", c.action, len(evs))
			}
			continue
		}
		if len(evs) != 1 || evs[0].EventType != c.wantType {
			t.Errorf("action %s (merged=%v) -> %v, want %s", c.action, c.merged, evs, c.wantType)
		}
	}
}

func TestHandleWebhook_AuthFailures(t *testing.T) {
	g := eventsPlugin(t, "s3cret")
	cases := map[string]string{
		"wrong secret": sign(prWebhookFixture, "wrong"),
		"garbage":      "sha256=deadbeef",
		"missing":      "",
		"no prefix":    strings.TrimPrefix(sign(prWebhookFixture, "s3cret"), "sha256="),
	}
	for name, sig := range cases {
		_, err := g.HandleWebhook(context.Background(),
			webhookReq(prWebhookFixture, sig, "pull_request"))
		var ae *plugin.AuthError
		if err == nil || !asAuthError(err, &ae) {
			t.Errorf("%s: err = %v, want *plugin.AuthError", name, err)
		}
	}
	// Unset secret rejects everything, even validly-signed requests.
	g2 := eventsPlugin(t, "")
	_, err := g2.HandleWebhook(context.Background(),
		webhookReq(prWebhookFixture, sign(prWebhookFixture, ""), "pull_request"))
	var ae *plugin.AuthError
	if err == nil || !asAuthError(err, &ae) {
		t.Errorf("unset secret: err = %v, want *plugin.AuthError", err)
	}
}

func TestHandleWebhook_IgnoredEventAndBadJSON(t *testing.T) {
	g := eventsPlugin(t, "s3cret")
	body := `{"zen": "Keep it logically awesome."}`
	evs, err := g.HandleWebhook(context.Background(),
		webhookReq(body, sign(body, "s3cret"), "ping"))
	if err != nil || len(evs) != 0 {
		t.Errorf("ping event: evs=%d err=%v, want 0/nil", len(evs), err)
	}
	bad := `{not json`
	_, err = g.HandleWebhook(context.Background(),
		webhookReq(bad, sign(bad, "s3cret"), "pull_request"))
	if err == nil {
		t.Error("bad JSON: expected parse error")
	}
}

const pollPRFixture = `[
  {"number": 42, "title": "Add feature",
   "updated_at": "2026-06-10T12:00:00Z",
   "html_url": "https://github.com/org/repo/pull/42",
   "head": {"ref": "feat/x", "sha": "abc123def"},
   "base": {"ref": "main"},
   "user": {"login": "alice"}},
  {"number": 41, "title": "Old PR",
   "updated_at": "2026-06-09T00:00:00Z",
   "html_url": "https://github.com/org/repo/pull/41",
   "head": {"ref": "feat/old", "sha": "0ld5ha"},
   "base": {"ref": "main"},
   "user": {"login": "bob"}}
]`

func TestPoll_ParityCursorAndClientSideFilter(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if !strings.Contains(r.URL.Path, "/repos/org/repo/pulls") {
			t.Errorf("path = %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("state") != "open" || q.Get("sort") != "updated" || q.Get("direction") != "asc" {
			t.Errorf("query = %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pollPRFixture))
	}))
	defer srv.Close()

	g := initPlugin(t, map[string]any{"base_url": srv.URL},
		map[string]string{"github_token": "ghp_x"})
	g.pollRepos = []string{"org/repo"}

	// Cursor sits between the two fixtures: PR 41 (older) must be filtered
	// out CLIENT-SIDE (GitHub has no updated_after param).
	evs, newCursor, err := g.Poll(context.Background(), "2026-06-10T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer ghp_x" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1 (older PR filtered client-side)", len(evs))
	}
	ev := evs[0]
	if ev.DedupKey != "pr:org/repo:42:abc123def" {
		t.Errorf("dedup_key = %q (must equal webhook's for same PR+SHA)", ev.DedupKey)
	}
	if ev.EventType != "pr_updated" {
		t.Errorf("event type = %q, want pr_updated", ev.EventType)
	}
	if newCursor != "2026-06-10T12:00:00Z" {
		t.Errorf("cursor = %q, want max updated_at", newCursor)
	}
}

func TestPoll_PartialFailureKeepsCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/repos/bad/repo/") {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pollPRFixture))
	}))
	defer srv.Close()

	g := initPlugin(t, map[string]any{"base_url": srv.URL},
		map[string]string{"github_token": "ghp_x"})
	g.pollRepos = []string{"org/repo", "bad/repo"}

	evs, newCursor, err := g.Poll(context.Background(), "2026-06-10T00:00:00Z")
	if err != nil {
		t.Fatalf("partial failure must not error: %v", err)
	}
	if len(evs) != 1 {
		t.Errorf("events = %d, want 1 from healthy repo", len(evs))
	}
	if newCursor != "2026-06-10T00:00:00Z" {
		t.Errorf("cursor = %q, want UNCHANGED", newCursor)
	}
}

func TestPoll_TotalFailureErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	g := initPlugin(t, map[string]any{"base_url": srv.URL},
		map[string]string{"github_token": "ghp_x"})
	g.pollRepos = []string{"org/repo"}
	if _, _, err := g.Poll(context.Background(), "2026-06-10T00:00:00Z"); err == nil {
		t.Fatal("expected error when every repo fails")
	}
}

func TestPoll_EmptyCursorWindowAndNoRepos(t *testing.T) {
	g := eventsPlugin(t, "s3cret")
	evs, cursor, err := g.Poll(context.Background(), "c0")
	if err != nil || len(evs) != 0 || cursor != "c0" {
		t.Errorf("no-repos poll: evs=%d cursor=%q err=%v", len(evs), cursor, err)
	}

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	g2 := initPlugin(t, map[string]any{"base_url": srv.URL, "poll_interval": "1m"},
		map[string]string{"github_token": "ghp_x"})
	g2.pollRepos = []string{"org/repo"}
	_, newCursor, err := g2.Poll(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath == "" {
		t.Fatal("no request made on empty cursor")
	}
	// Empty page + empty cursor: cursor must not regress to zero time.
	if parsed, perr := time.Parse(time.RFC3339, newCursor); newCursor != "" && (perr != nil || parsed.Year() < 2000) {
		t.Errorf("cursor = %q, must be empty or a sane RFC3339 time", newCursor)
	}
}

// asAuthError mirrors errors.As for the concrete *plugin.AuthError.
func asAuthError(err error, target **plugin.AuthError) bool {
	ae, ok := err.(*plugin.AuthError)
	if ok {
		*target = ae
	}
	return ok
}
