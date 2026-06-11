package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

const mrWebhookFixture = `{
  "object_kind": "merge_request",
  "user": {"username": "alice"},
  "project": {"path_with_namespace": "org/repo"},
  "object_attributes": {
    "iid": 42,
    "title": "Add feature",
    "action": "open",
    "source_branch": "feat/x",
    "target_branch": "main",
    "url": "https://gitlab.example.com/org/repo/-/merge_requests/42",
    "last_commit": {"id": "abc123def"}
  }
}`

func webhookPlugin(t *testing.T, secret string) *GitLabPlugin {
	t.Helper()
	g := &GitLabPlugin{}
	secrets := func(name string) (string, error) {
		switch name {
		case "gitlab_token":
			return "tok", nil
		case "gitlab_webhook_secret":
			return secret, nil
		}
		return "", nil
	}
	if err := g.Init(context.Background(), map[string]any{}, secrets); err != nil {
		t.Fatal(err)
	}
	return g
}

func webhookReq(body, token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	if token != "" {
		r.Header.Set("X-Gitlab-Token", token)
	}
	return r
}

func TestHandleWebhook_ValidMR(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	evs, err := g.HandleWebhook(context.Background(), webhookReq(mrWebhookFixture, "s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.SourcePlugin != "gitlab" || ev.EventType != "mr_opened" {
		t.Errorf("envelope = %s/%s", ev.SourcePlugin, ev.EventType)
	}
	if ev.Identity != "alice" {
		t.Errorf("identity = %q", ev.Identity)
	}
	if ev.DedupKey != "mr:org/repo:42:abc123def" {
		t.Errorf("dedup_key = %q", ev.DedupKey)
	}
	norm := ev.PayloadNorm
	for k, want := range map[string]any{
		"project": "org/repo", "mr_iid": 42, "title": "Add feature", "action": "open",
		"source_branch": "feat/x", "target_branch": "main",
		"last_commit_sha": "abc123def", "author": "alice",
		"url": "https://gitlab.example.com/org/repo/-/merge_requests/42",
	} {
		if norm[k] != want {
			t.Errorf("payload_norm[%q] = %v (%T), want %v", k, norm[k], norm[k], want)
		}
	}
	if len(ev.PayloadRaw) == 0 {
		t.Error("payload_raw empty")
	}
}

func TestHandleWebhook_ActionMapping(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	for action, wantType := range map[string]string{
		"open": "mr_opened", "update": "mr_updated", "merge": "mr_merged", "close": "mr_closed",
	} {
		body := strings.Replace(mrWebhookFixture, `"action": "open"`, `"action": "`+action+`"`, 1)
		evs, err := g.HandleWebhook(context.Background(), webhookReq(body, "s3cret"))
		if err != nil || len(evs) != 1 {
			t.Fatalf("action %s: evs=%d err=%v", action, len(evs), err)
		}
		if evs[0].EventType != wantType {
			t.Errorf("action %s -> %s, want %s", action, evs[0].EventType, wantType)
		}
	}
	// Unrecognized action: ignored.
	body := strings.Replace(mrWebhookFixture, `"action": "open"`, `"action": "approved"`, 1)
	evs, err := g.HandleWebhook(context.Background(), webhookReq(body, "s3cret"))
	if err != nil || len(evs) != 0 {
		t.Errorf("approved action: evs=%d err=%v, want 0/nil", len(evs), err)
	}
}

func TestHandleWebhook_AuthFailures(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	for name, token := range map[string]string{"wrong": "nope", "missing": ""} {
		_, err := g.HandleWebhook(context.Background(), webhookReq(mrWebhookFixture, token))
		var ae *plugin.AuthError
		if err == nil || !asAuthError(err, &ae) {
			t.Errorf("%s token: err = %v, want *plugin.AuthError", name, err)
		}
	}
	// No secret configured -> reject everything.
	g2 := webhookPlugin(t, "")
	_, err := g2.HandleWebhook(context.Background(), webhookReq(mrWebhookFixture, "anything"))
	var ae *plugin.AuthError
	if err == nil || !asAuthError(err, &ae) {
		t.Errorf("unconfigured secret: err = %v, want *plugin.AuthError", err)
	}
}

func TestHandleWebhook_IgnoredKindAndBadJSON(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	evs, err := g.HandleWebhook(context.Background(),
		webhookReq(`{"object_kind": "push"}`, "s3cret"))
	if err != nil || len(evs) != 0 {
		t.Errorf("push kind: evs=%d err=%v, want 0/nil", len(evs), err)
	}
	_, err = g.HandleWebhook(context.Background(), webhookReq(`{not json`, "s3cret"))
	if err == nil {
		t.Error("bad JSON: expected parse error")
	}
}

const pollMRFixture = `[
  {"iid": 42, "title": "Add feature", "sha": "abc123def",
   "source_branch": "feat/x", "target_branch": "main",
   "web_url": "https://gitlab.example.com/org/repo/-/merge_requests/42",
   "updated_at": "2026-06-10T12:00:00Z",
   "author": {"username": "alice"}}
]`

func TestPoll_NormalizationParityAndCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// NOTE: r.URL.Path is DECODED by net/http; the %2F survives only in
		// EscapedPath(). The GitLab API requires the encoded form on the wire.
		if !strings.Contains(r.URL.EscapedPath(), "/api/v4/projects/org%2Frepo/merge_requests") {
			t.Errorf("escaped path = %q", r.URL.EscapedPath())
		}
		q := r.URL.Query()
		if q.Get("state") != "opened" || q.Get("order_by") != "updated_at" || q.Get("sort") != "asc" {
			t.Errorf("query = %v", q)
		}
		if q.Get("updated_after") == "" {
			t.Error("updated_after missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pollMRFixture))
	}))
	defer srv.Close()

	g := webhookPlugin(t, "s3cret")
	g.baseURL = srv.URL
	g.pollProjects = []string{"org/repo"}

	evs, newCursor, err := g.Poll(context.Background(), "2026-06-10T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d", len(evs))
	}
	ev := evs[0]
	// Parity with the webhook envelope for the same MR state:
	if ev.DedupKey != "mr:org/repo:42:abc123def" {
		t.Errorf("dedup_key = %q (must equal webhook's for the same MR+SHA)", ev.DedupKey)
	}
	if ev.EventType != "mr_updated" {
		t.Errorf("event type = %q, want mr_updated (poll cannot distinguish opened)", ev.EventType)
	}
	if ev.PayloadNorm["author"] != "alice" || ev.PayloadNorm["project"] != "org/repo" {
		t.Errorf("norm = %v", ev.PayloadNorm)
	}
	if newCursor != "2026-06-10T12:00:00Z" {
		t.Errorf("cursor = %q, want max updated_at", newCursor)
	}
}

func TestPoll_PartialFailureKeepsCursor(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.Contains(r.URL.EscapedPath(), "bad%2Frepo") {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pollMRFixture))
	}))
	defer srv.Close()

	g := webhookPlugin(t, "s3cret")
	g.baseURL = srv.URL
	g.pollProjects = []string{"org/repo", "bad/repo"}

	evs, newCursor, err := g.Poll(context.Background(), "2026-06-10T00:00:00Z")
	if err != nil {
		t.Fatalf("partial failure must not error: %v", err)
	}
	if len(evs) != 1 {
		t.Errorf("events = %d, want 1 from the healthy project", len(evs))
	}
	if newCursor != "2026-06-10T00:00:00Z" {
		t.Errorf("cursor = %q, want UNCHANGED on partial failure", newCursor)
	}
	if calls != 2 {
		t.Errorf("API calls = %d", calls)
	}
}

func TestPoll_TotalFailureErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	g := webhookPlugin(t, "s3cret")
	g.baseURL = srv.URL
	g.pollProjects = []string{"org/repo"}
	_, _, err := g.Poll(context.Background(), "2026-06-10T00:00:00Z")
	if err == nil {
		t.Fatal("expected error when every project fails")
	}
}

func TestPoll_EmptyCursorUsesWindow(t *testing.T) {
	var gotAfter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAfter = r.URL.Query().Get("updated_after")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	g := webhookPlugin(t, "s3cret")
	g.baseURL = srv.URL
	g.pollProjects = []string{"org/repo"}
	g.pollInterval = time.Minute

	_, _, err := g.Poll(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	parsed, perr := time.Parse(time.RFC3339, gotAfter)
	if perr != nil {
		t.Fatalf("updated_after %q not RFC3339: %v", gotAfter, perr)
	}
	if since := time.Since(parsed); since < 30*time.Second || since > 5*time.Minute {
		t.Errorf("first-poll window = %v ago, want ~poll_interval", since)
	}
}

func TestPoll_NoProjectsNoop(t *testing.T) {
	g := webhookPlugin(t, "s3cret")
	evs, cursor, err := g.Poll(context.Background(), "c0")
	if err != nil || len(evs) != 0 || cursor != "c0" {
		t.Errorf("no-project poll: evs=%d cursor=%q err=%v", len(evs), cursor, err)
	}
}

// asAuthError mirrors errors.As without importing errors twice in assertions.
func asAuthError(err error, target **plugin.AuthError) bool {
	ae, ok := err.(*plugin.AuthError)
	if ok {
		*target = ae
	}
	return ok
}

const noteWebhookFixture = `{
  "object_kind": "note",
  "user": {"username": "alice"},
  "project": {"path_with_namespace": "org/repo"},
  "merge_request": {"iid": 42, "title": "Add rate limiter", "author_id": 7, "source_branch": "feat/limiter"},
  "object_attributes": {
    "id": 9001,
    "discussion_id": "abc123",
    "note": "Should this handle burst traffic?",
    "noteable_type": "MergeRequest",
    "url": "https://gitlab.com/org/repo/-/merge_requests/42#note_9001"
  }
}`

func TestHandleWebhookMRNote(t *testing.T) {
	g := &GitLabPlugin{webhookSecret: "s3cret"}
	req := webhookReq(noteWebhookFixture, "s3cret")
	events, err := g.HandleWebhook(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != "mr_note" {
		t.Errorf("event_type = %s", ev.EventType)
	}
	if ev.DedupKey != "note:org/repo:9001" {
		t.Errorf("dedup key = %s", ev.DedupKey)
	}
	if ev.Identity != "alice" {
		t.Errorf("identity = %s", ev.Identity)
	}
	for k, want := range map[string]any{
		"project": "org/repo", "mr_iid": 42, "note_id": 9001,
		"discussion_id": "abc123", "note_body": "Should this handle burst traffic?",
		"author": "alice", "mr_title": "Add rate limiter", "mr_source_branch": "feat/limiter",
	} {
		if fmt.Sprint(ev.PayloadNorm[k]) != fmt.Sprint(want) {
			t.Errorf("payload_norm[%s] = %v, want %v", k, ev.PayloadNorm[k], want)
		}
	}
}

func TestHandleWebhookNoteFromBotDropped(t *testing.T) {
	g := &GitLabPlugin{webhookSecret: "s3cret", botUsername: "fleet-bot"}
	fixture := strings.Replace(noteWebhookFixture, `"username": "alice"`, `"username": "fleet-bot"`, 1)
	req := webhookReq(fixture, "s3cret")
	events, err := g.HandleWebhook(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("bot's own note must be dropped, got %d events", len(events))
	}
}

func TestHandleWebhookNonMRNoteIgnored(t *testing.T) {
	g := &GitLabPlugin{webhookSecret: "s3cret"}
	fixture := strings.Replace(noteWebhookFixture, `"noteable_type": "MergeRequest"`, `"noteable_type": "Issue"`, 1)
	req := webhookReq(fixture, "s3cret")
	events, err := g.HandleWebhook(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("issue notes are out of scope, got %d events", len(events))
	}
}
