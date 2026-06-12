# SP3b — Plugin Breadth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add four integration plugins — Slack/Discord/Email (actions only) and GitHub (full webhook + poll events + PR-comment action) — against the frozen SP3 framework.

**Architecture:** Each plugin is a self-registering package under `internal/plugins/` following the GitLab template. GitHub mirrors GitLab structurally (core+action in `github.go`, event source in `events.go`) with HMAC-SHA256 webhook auth and a client-side-filtered poll. The framework (`internal/plugin`, `internal/events`, `internal/server`, serve daemon) is NOT modified; `fleet serve` picks up GitHub's PollSource automatically via the existing type-assertion loop.

**Tech Stack:** Go 1.26 stdlib only (net/http, crypto/hmac, crypto/sha256, net/smtp). No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-06-10-sp3b-plugin-breadth-design.md`

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `internal/plugins/slack/slack.go` | Create | Slack plugin: send_message via chat.postMessage (ok:false contract) |
| `internal/plugins/slack/slack_test.go` | Create | httptest action tests |
| `internal/plugins/discord/discord.go` | Create | Discord plugin: send_message via webhook-URL secrets |
| `internal/plugins/discord/discord_test.go` | Create | httptest + fake-secrets tests |
| `internal/plugins/email/email.go` | Create | Email plugin: send_email via net/smtp (send seam) |
| `internal/plugins/email/email_test.go` | Create | Validation/seam tests + in-process fake SMTP |
| `internal/plugins/github/github.go` | Create | GitHub plugin core + post_pr_comment action |
| `internal/plugins/github/github_test.go` | Create | Action httptest tests |
| `internal/plugins/github/events.go` | Create | Webhook (HMAC) + poll + shared PR normalization |
| `internal/plugins/github/events_test.go` | Create | Webhook/poll suites mirroring GitLab |
| `cmd/fleet/main.go` | Modify | Four blank imports |
| `configs/fleet.yaml` | Modify | Commented sample blocks |
| `internal/run/event_github_integration_test.go` | Create | GitHub webhook → Run through the existing framework |

Notes that apply to every task:
- Run all commands from the repo root. Commit directly to `master` (consented project convention). Do not push until Task 6.
- The reference implementation is `internal/plugins/gitlab/` (gitlab.go + events.go + tests) — read it before starting.
- Each plugin duplicates the tiny `paramToString` helper from gitlab.go locally (per-plugin isolation; the framework is frozen, so no shared helper package this round).
- **GitHub API paths use LITERAL slashes in `owner/repo` (e.g. `/repos/org/repo/issues/42/comments`) — do NOT percent-encode the slash. This differs from GitLab's `%2F`.**

---

### Task 1: Slack plugin

**Files:**
- Create: `internal/plugins/slack/slack.go`
- Test: `internal/plugins/slack/slack_test.go`

- [x] **Step 1: Write the failing tests** — `internal/plugins/slack/slack_test.go`:

```go
package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func initPlugin(t *testing.T, baseURL, token string) *SlackPlugin {
	t.Helper()
	p := &SlackPlugin{}
	secrets := func(name string) (string, error) {
		if name == "slack_bot_token" {
			return token, nil
		}
		return "", nil
	}
	cfg := map[string]any{}
	if baseURL != "" {
		cfg["base_url"] = baseURL
	}
	if err := p.Init(context.Background(), cfg, secrets); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSendMessage_Success(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true, "ts": "1718000000.000100", "channel": "C123"}`))
	}))
	defer srv.Close()

	p := initPlugin(t, srv.URL, "xoxb-test")
	result, err := p.Do(context.Background(), "send_message",
		map[string]any{"channel": "#general", "text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer xoxb-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/chat.postMessage" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["channel"] != "#general" || gotBody["text"] != "hello" {
		t.Errorf("body = %v", gotBody)
	}
	if result["ts"] != "1718000000.000100" || result["channel"] != "C123" {
		t.Errorf("result = %v", result)
	}
}

func TestSendMessage_OkFalse(t *testing.T) {
	// Slack signals failure with HTTP 200 + ok:false.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": false, "error": "channel_not_found"}`))
	}))
	defer srv.Close()

	p := initPlugin(t, srv.URL, "xoxb-test")
	_, err := p.Do(context.Background(), "send_message",
		map[string]any{"channel": "#nope", "text": "hello"})
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("err = %v, want channel_not_found", err)
	}
}

func TestSendMessage_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	p := initPlugin(t, srv.URL, "xoxb-test")
	_, err := p.Do(context.Background(), "send_message",
		map[string]any{"channel": "#general", "text": "hello"})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want 500 mention", err)
	}
}

func TestSendMessage_Validation(t *testing.T) {
	p := initPlugin(t, "http://unused", "xoxb-test")
	if _, err := p.Do(context.Background(), "send_message", map[string]any{"text": "x"}); err == nil {
		t.Error("missing channel: expected error")
	}
	if _, err := p.Do(context.Background(), "send_message", map[string]any{"channel": "#g"}); err == nil {
		t.Error("missing text: expected error")
	}
	if _, err := p.Do(context.Background(), "nope", map[string]any{}); err == nil {
		t.Error("unknown action: expected error")
	}
}

func TestSendMessage_NoToken(t *testing.T) {
	p := initPlugin(t, "http://unused", "")
	_, err := p.Do(context.Background(), "send_message",
		map[string]any{"channel": "#g", "text": "x"})
	if err == nil || !strings.Contains(err.Error(), "slack_bot_token") {
		t.Fatalf("err = %v, want missing-token error", err)
	}
}

func TestDefaults(t *testing.T) {
	p := initPlugin(t, "", "tok") // no base_url config
	if p.baseURL != "https://slack.com/api" {
		t.Errorf("baseURL = %q", p.baseURL)
	}
	if p.Name() != "slack" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.EventSources() != nil {
		t.Error("EventSources must be nil (actions-only plugin)")
	}
	if len(p.Actions()) != 1 || p.Actions()[0].Name != "send_message" {
		t.Errorf("Actions = %v", p.Actions())
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/plugins/slack/ -v`
Expected: FAIL (package missing).

- [x] **Step 3: Implement** — `internal/plugins/slack/slack.go`:

```go
// Package slack provides the Slack integration plugin: the send_message
// action via chat.postMessage. Actions only in SP3b — the Events API needs a
// challenge-response framework extension and is deferred (SP3b spec §9).
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

func init() {
	plugin.Register(&SlackPlugin{})
}

// SlackPlugin posts messages with a bot token (secret slack_bot_token).
type SlackPlugin struct {
	botToken string
	baseURL  string
}

func (s *SlackPlugin) Name() string { return "slack" }

func (s *SlackPlugin) EventSources() []plugin.EventSource { return nil }

func (s *SlackPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "send_message", Description: "Post a plain-text message to a channel via chat.postMessage"},
	}
}

func (s *SlackPlugin) ConfigSchema() plugin.Schema {
	return plugin.Schema{
		"type": "object",
		"properties": map[string]any{
			"base_url": map[string]any{"type": "string", "default": "https://slack.com/api"},
		},
	}
}

func (s *SlackPlugin) Init(_ context.Context, cfg map[string]any, secrets plugin.SecretLookup) error {
	tok, err := secrets("slack_bot_token")
	if err != nil {
		return fmt.Errorf("slack: resolve secret slack_bot_token: %w", err)
	}
	s.botToken = tok // may be empty; send_message errors at call time
	if u, ok := cfg["base_url"].(string); ok && u != "" {
		s.baseURL = strings.TrimRight(u, "/")
	} else {
		s.baseURL = "https://slack.com/api"
	}
	return nil
}

func (s *SlackPlugin) Do(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "send_message":
		return s.sendMessage(ctx, params)
	default:
		return nil, fmt.Errorf("slack: unknown action %q", action)
	}
}

func (s *SlackPlugin) sendMessage(ctx context.Context, params map[string]any) (map[string]any, error) {
	channel := paramToString(params["channel"])
	text := paramToString(params["text"])
	if channel == "" || text == "" {
		return nil, fmt.Errorf("slack send_message: channel and text are required")
	}
	if s.botToken == "" {
		return nil, fmt.Errorf("slack send_message: secret slack_bot_token is not configured")
	}

	payload, _ := json.Marshal(map[string]string{"channel": channel, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("slack: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack: post message: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("slack: post message returned %d: %s", resp.StatusCode, truncateForErr(respBody))
	}

	// Slack's failure contract: HTTP 200 with {"ok": false, "error": "..."}.
	var decoded struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		TS      string `json:"ts"`
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("slack: parse response: %w", err)
	}
	if !decoded.OK {
		return nil, fmt.Errorf("slack: post message failed: %s", decoded.Error)
	}
	return map[string]any{"ok": true, "ts": decoded.TS, "channel": decoded.Channel}, nil
}

// paramToString converts a parameter value to a string (string/int/float64).
func paramToString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case float64:
		return strconv.FormatInt(int64(val), 10)
	default:
		return ""
	}
}

func truncateForErr(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}
```

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/plugins/slack/ -v && gofmt -l internal/plugins/slack/ && go vet ./internal/plugins/slack/`
Expected: 6 tests PASS; gofmt empty; vet clean.

- [x] **Step 5: Commit**

```bash
git add internal/plugins/slack/
git commit -m "feat(sp3b): slack plugin — send_message action"
```

---

### Task 2: Discord plugin

**Files:**
- Create: `internal/plugins/discord/discord.go`
- Test: `internal/plugins/discord/discord_test.go`

- [x] **Step 1: Write the failing tests** — `internal/plugins/discord/discord_test.go`:

```go
package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func initPlugin(t *testing.T, secrets map[string]string) *DiscordPlugin {
	t.Helper()
	p := &DiscordPlugin{}
	lookup := func(name string) (string, error) { return secrets[name], nil }
	if err := p.Init(context.Background(), map[string]any{}, lookup); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSendMessage_DefaultWebhook(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent) // Discord returns 204
	}))
	defer srv.Close()

	p := initPlugin(t, map[string]string{"discord_webhook_url": srv.URL})
	result, err := p.Do(context.Background(), "send_message", map[string]any{"content": "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["content"] != "ping" {
		t.Errorf("body = %v", gotBody)
	}
	if result["status"] != 204 {
		t.Errorf("result = %v", result)
	}
}

func TestSendMessage_WebhookOverride(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := initPlugin(t, map[string]string{
		"discord_webhook_url": "http://127.0.0.1:1/unreachable-default",
		"alerts_webhook":      srv.URL,
	})
	_, err := p.Do(context.Background(), "send_message",
		map[string]any{"content": "alert!", "webhook": "alerts_webhook"})
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("override webhook hits = %d, want 1", hits)
	}
}

func TestSendMessage_MissingSecretErrorsAtDo(t *testing.T) {
	// Init succeeds without any Discord secret; Do fails.
	p := initPlugin(t, map[string]string{})
	_, err := p.Do(context.Background(), "send_message", map[string]any{"content": "x"})
	if err == nil || !strings.Contains(err.Error(), "discord_webhook_url") {
		t.Fatalf("err = %v, want missing-secret error naming the secret", err)
	}
}

func TestSendMessage_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"message": "Cannot send an empty message"}`))
	}))
	defer srv.Close()

	p := initPlugin(t, map[string]string{"discord_webhook_url": srv.URL})
	_, err := p.Do(context.Background(), "send_message", map[string]any{"content": "x"})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v, want 400 mention", err)
	}
}

func TestSendMessage_Validation(t *testing.T) {
	p := initPlugin(t, map[string]string{"discord_webhook_url": "http://unused"})
	if _, err := p.Do(context.Background(), "send_message", map[string]any{}); err == nil {
		t.Error("missing content: expected error")
	}
	if _, err := p.Do(context.Background(), "nope", map[string]any{}); err == nil {
		t.Error("unknown action: expected error")
	}
}

func TestShape(t *testing.T) {
	p := &DiscordPlugin{}
	if p.Name() != "discord" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.EventSources() != nil {
		t.Error("EventSources must be nil")
	}
	if len(p.Actions()) != 1 || p.Actions()[0].Name != "send_message" {
		t.Errorf("Actions = %v", p.Actions())
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/plugins/discord/ -v`
Expected: FAIL (package missing).

- [x] **Step 3: Implement** — `internal/plugins/discord/discord.go`:

```go
// Package discord provides the Discord integration plugin: the send_message
// action via incoming webhook URLs stored as secrets. Message EVENTS require
// a Gateway WebSocket connection and are deferred (SP3b spec §9).
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

func init() {
	plugin.Register(&DiscordPlugin{})
}

// defaultWebhookSecret names the secret holding the default incoming-webhook URL.
const defaultWebhookSecret = "discord_webhook_url"

// DiscordPlugin posts messages to incoming webhooks. Webhook URLs live ONLY in
// the secret store: action params name a secret, never carry the URL, so URLs
// stay out of run records.
type DiscordPlugin struct {
	secrets plugin.SecretLookup
}

func (d *DiscordPlugin) Name() string { return "discord" }

func (d *DiscordPlugin) EventSources() []plugin.EventSource { return nil }

func (d *DiscordPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "send_message", Description: "Post a message via an incoming webhook (param 'webhook' names an alternative secret)"},
	}
}

func (d *DiscordPlugin) ConfigSchema() plugin.Schema {
	return plugin.Schema{"type": "object", "properties": map[string]any{}}
}

// Init stores the secret lookup; webhook URLs resolve at Do time so a fleet
// without Discord configured still initializes.
func (d *DiscordPlugin) Init(_ context.Context, _ map[string]any, secrets plugin.SecretLookup) error {
	d.secrets = secrets
	return nil
}

func (d *DiscordPlugin) Do(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "send_message":
		return d.sendMessage(ctx, params)
	default:
		return nil, fmt.Errorf("discord: unknown action %q", action)
	}
}

func (d *DiscordPlugin) sendMessage(ctx context.Context, params map[string]any) (map[string]any, error) {
	content, _ := params["content"].(string)
	if content == "" {
		return nil, fmt.Errorf("discord send_message: content is required")
	}
	secretName := defaultWebhookSecret
	if w, _ := params["webhook"].(string); w != "" {
		secretName = w
	}
	if d.secrets == nil {
		return nil, fmt.Errorf("discord: plugin not initialized")
	}
	url, err := d.secrets(secretName)
	if err != nil {
		return nil, fmt.Errorf("discord: resolve secret %q: %w", secretName, err)
	}
	if url == "" {
		return nil, fmt.Errorf("discord send_message: secret %q is not configured", secretName)
	}

	payload, _ := json.Marshal(map[string]string{"content": content})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("discord: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discord: post message: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("discord: post message returned %d: %s", resp.StatusCode, truncateForErr(respBody))
	}
	return map[string]any{"status": resp.StatusCode}, nil
}

func truncateForErr(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}
```

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/plugins/discord/ -v && gofmt -l internal/plugins/discord/ && go vet ./internal/plugins/discord/`
Expected: 6 tests PASS; clean.

- [x] **Step 5: Commit**

```bash
git add internal/plugins/discord/
git commit -m "feat(sp3b): discord plugin — send_message via webhook-URL secrets"
```

---

### Task 3: Email plugin

**Files:**
- Create: `internal/plugins/email/email.go`
- Test: `internal/plugins/email/email_test.go`

- [x] **Step 1: Write the failing tests** — `internal/plugins/email/email_test.go`:

```go
package email

import (
	"bufio"
	"context"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
)

func initPlugin(t *testing.T, cfg map[string]any, password string) *EmailPlugin {
	t.Helper()
	p := &EmailPlugin{}
	lookup := func(name string) (string, error) {
		if name == "smtp_password" {
			return password, nil
		}
		return "", nil
	}
	if err := p.Init(context.Background(), cfg, lookup); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseCfg() map[string]any {
	return map[string]any{"smtp_host": "mail.example.com", "from": "fleet@example.com"}
}

func TestInit_Validation(t *testing.T) {
	p := &EmailPlugin{}
	lookup := func(string) (string, error) { return "", nil }
	if err := p.Init(context.Background(), map[string]any{"from": "a@b.c"}, lookup); err == nil {
		t.Error("missing smtp_host: expected Init error")
	}
	if err := p.Init(context.Background(), map[string]any{"smtp_host": "h"}, lookup); err == nil {
		t.Error("missing from: expected Init error")
	}
}

func TestInit_Defaults(t *testing.T) {
	p := initPlugin(t, baseCfg(), "")
	if p.port != "587" {
		t.Errorf("port = %q, want 587", p.port)
	}
	if p.username != "fleet@example.com" {
		t.Errorf("username = %q, want from", p.username)
	}
	if p.Name() != "email" || p.EventSources() != nil {
		t.Error("shape wrong")
	}
}

func TestSendEmail_SeamAndMessage(t *testing.T) {
	p := initPlugin(t, baseCfg(), "s3cret")
	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg []byte
	var gotAuth smtp.Auth
	p.send = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr, gotAuth, gotFrom, gotTo, gotMsg = addr, a, from, to, msg
		return nil
	}

	_, err := p.Do(context.Background(), "send_email", map[string]any{
		"to":      "a@x.com, b@y.com",
		"subject": "Run report",
		"body":    "All good.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAddr != "mail.example.com:587" {
		t.Errorf("addr = %q", gotAddr)
	}
	if gotFrom != "fleet@example.com" {
		t.Errorf("from = %q", gotFrom)
	}
	if len(gotTo) != 2 || gotTo[0] != "a@x.com" || gotTo[1] != "b@y.com" {
		t.Errorf("to = %v", gotTo)
	}
	if gotAuth == nil {
		t.Error("expected PlainAuth when smtp_password set")
	}
	msg := string(gotMsg)
	for _, want := range []string{
		"From: fleet@example.com\r\n",
		"To: a@x.com, b@y.com\r\n",
		"Subject: Run report\r\n",
		"\r\n\r\nAll good.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestSendEmail_NoAuthWhenNoPassword(t *testing.T) {
	p := initPlugin(t, baseCfg(), "")
	var gotAuth smtp.Auth = smtp.PlainAuth("", "x", "y", "z") // sentinel non-nil
	p.send = func(_ string, a smtp.Auth, _ string, _ []string, _ []byte) error {
		gotAuth = a
		return nil
	}
	if _, err := p.Do(context.Background(), "send_email",
		map[string]any{"to": "a@x.com", "subject": "s", "body": "b"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != nil {
		t.Error("expected nil auth when smtp_password empty")
	}
}

func TestSendEmail_Validation(t *testing.T) {
	p := initPlugin(t, baseCfg(), "")
	p.send = func(string, smtp.Auth, string, []string, []byte) error { return nil }
	cases := []map[string]any{
		{"subject": "s", "body": "b"},               // no to
		{"to": "a@x.com", "body": "b"},              // no subject
		{"to": "a@x.com", "subject": "s"},           // no body
		{"to": " , ,", "subject": "s", "body": "b"}, // only empty recipients
	}
	for i, params := range cases {
		if _, err := p.Do(context.Background(), "send_email", params); err == nil {
			t.Errorf("case %d: expected error for %v", i, params)
		}
	}
	if _, err := p.Do(context.Background(), "nope", map[string]any{}); err == nil {
		t.Error("unknown action: expected error")
	}
}

// fakeSMTP is a minimal in-process SMTP server (plaintext, no auth, no
// STARTTLS advertised) capturing one message.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	from string
	to   []string
	data string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)
	say := func(s string) { _, _ = w.WriteString(s + "\r\n"); _ = w.Flush() }
	say("220 fake ESMTP")
	inData := false
	var dataLines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				f.mu.Lock()
				f.data = strings.Join(dataLines, "\n")
				f.mu.Unlock()
				say("250 ok")
				continue
			}
			dataLines = append(dataLines, line)
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			say("250 fake") // no extensions: no STARTTLS, no AUTH
		case strings.HasPrefix(upper, "MAIL FROM:"):
			f.mu.Lock()
			f.from = line[len("MAIL FROM:"):]
			f.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			f.mu.Lock()
			f.to = append(f.to, line[len("RCPT TO:"):])
			f.mu.Unlock()
			say("250 ok")
		case upper == "DATA":
			inData = true
			say("354 go ahead")
		case upper == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func TestSendEmail_RealSMTPPath(t *testing.T) {
	fake := newFakeSMTP(t)
	host, port, _ := net.SplitHostPort(fake.ln.Addr().String())

	p := initPlugin(t, map[string]any{
		"smtp_host": host,
		"smtp_port": port,
		"from":      "fleet@example.com",
	}, "") // no password -> no auth -> plaintext OK
	// NOTE: p.send keeps its default (real net/smtp.SendMail).

	_, err := p.Do(context.Background(), "send_email", map[string]any{
		"to": "dev@example.com", "subject": "smoke", "body": "hello smtp",
	})
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !strings.Contains(fake.from, "fleet@example.com") {
		t.Errorf("MAIL FROM = %q", fake.from)
	}
	if len(fake.to) != 1 || !strings.Contains(fake.to[0], "dev@example.com") {
		t.Errorf("RCPT TO = %v", fake.to)
	}
	if !strings.Contains(fake.data, "hello smtp") {
		t.Errorf("DATA = %q", fake.data)
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/plugins/email/ -v`
Expected: FAIL (package missing).

- [x] **Step 3: Implement** — `internal/plugins/email/email.go`:

```go
// Package email provides the Email integration plugin: the send_email action
// over stdlib net/smtp (STARTTLS negotiated automatically when the server
// advertises it). Inbound email (IMAP) is deferred (SP3b spec §9).
package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

func init() {
	plugin.Register(&EmailPlugin{})
}

// EmailPlugin sends plain-text email via SMTP.
type EmailPlugin struct {
	host     string
	port     string
	from     string
	username string
	password string
	// send is a test seam; defaults to smtp.SendMail.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

func (e *EmailPlugin) Name() string { return "email" }

func (e *EmailPlugin) EventSources() []plugin.EventSource { return nil }

func (e *EmailPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "send_email", Description: "Send a plain-text email (to is a comma-separated list)"},
	}
}

func (e *EmailPlugin) ConfigSchema() plugin.Schema {
	return plugin.Schema{
		"type": "object",
		"properties": map[string]any{
			"smtp_host":     map[string]any{"type": "string"},
			"smtp_port":     map[string]any{"type": "string", "default": "587"},
			"from":          map[string]any{"type": "string"},
			"smtp_username": map[string]any{"type": "string", "description": "defaults to from"},
		},
		"required": []string{"smtp_host", "from"},
	}
}

func (e *EmailPlugin) Init(_ context.Context, cfg map[string]any, secrets plugin.SecretLookup) error {
	host, _ := cfg["smtp_host"].(string)
	if host == "" {
		return fmt.Errorf("email: smtp_host is required")
	}
	from, _ := cfg["from"].(string)
	if from == "" {
		return fmt.Errorf("email: from is required")
	}
	e.host = host
	e.from = from
	e.port = "587"
	if p, ok := cfg["smtp_port"].(string); ok && p != "" {
		e.port = p
	}
	e.username = from
	if u, ok := cfg["smtp_username"].(string); ok && u != "" {
		e.username = u
	}
	pw, err := secrets("smtp_password")
	if err != nil {
		return fmt.Errorf("email: resolve secret smtp_password: %w", err)
	}
	e.password = pw // empty = unauthenticated relay
	if e.send == nil {
		e.send = smtp.SendMail
	}
	return nil
}

func (e *EmailPlugin) Do(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "send_email":
		return e.sendEmail(ctx, params)
	default:
		return nil, fmt.Errorf("email: unknown action %q", action)
	}
}

func (e *EmailPlugin) sendEmail(_ context.Context, params map[string]any) (map[string]any, error) {
	toRaw, _ := params["to"].(string)
	subject, _ := params["subject"].(string)
	body, _ := params["body"].(string)
	if toRaw == "" || subject == "" || body == "" {
		return nil, fmt.Errorf("email send_email: to, subject, and body are required")
	}
	var recipients []string
	for _, addr := range strings.Split(toRaw, ",") {
		if a := strings.TrimSpace(addr); a != "" {
			recipients = append(recipients, a)
		}
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("email send_email: no valid recipients in %q", toRaw)
	}

	var auth smtp.Auth
	if e.password != "" {
		auth = smtp.PlainAuth("", e.username, e.password, e.host)
	}
	msg := []byte("From: " + e.from + "\r\n" +
		"To: " + strings.Join(recipients, ", ") + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"\r\n" + body)

	if err := e.send(e.host+":"+e.port, auth, e.from, recipients, msg); err != nil {
		return nil, fmt.Errorf("email: send: %w", err)
	}
	return map[string]any{"recipients": len(recipients)}, nil
}
```

NOTE: the test asserts the message contains `"\r\n\r\nAll good."` — the blank line between headers and body. The construction above yields `Subject: ...\r\n\r\nAll good.` — correct.

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/plugins/email/ -race -v && gofmt -l internal/plugins/email/ && go vet ./internal/plugins/email/`
Expected: 6 tests PASS (incl. the real-SMTP-path test against the in-process fake); clean.

- [x] **Step 5: Commit**

```bash
git add internal/plugins/email/
git commit -m "feat(sp3b): email plugin — send_email via stdlib SMTP"
```

---

### Task 4: GitHub plugin core + action

**Files:**
- Create: `internal/plugins/github/github.go`
- Test: `internal/plugins/github/github_test.go`

- [x] **Step 1: Write the failing tests** — `internal/plugins/github/github_test.go`:

```go
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
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/plugins/github/ -v`
Expected: FAIL (package missing).

- [x] **Step 3: Implement** — `internal/plugins/github/github.go`:

```go
// Package github provides the GitHub integration plugin: the pr_events source
// (webhook + poll, see events.go) and the post_pr_comment action. GitHub
// Enterprise is supported via the base_url config.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

func init() {
	plugin.Register(&GitHubPlugin{})
}

// GitHubPlugin integrates GitHub: PR events in, PR comments out.
type GitHubPlugin struct {
	token         string
	baseURL       string
	webhookSecret string
	pollRepos     []string
	pollInterval  time.Duration
}

func (g *GitHubPlugin) Name() string { return "github" }

func (g *GitHubPlugin) EventSources() []plugin.EventSource {
	return []plugin.EventSource{
		{Name: "pr_events", Description: "Pull request opened/updated/merged/closed events (webhook + poll)"},
	}
}

func (g *GitHubPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "post_pr_comment", Description: "Post a comment on a pull request"},
	}
}

func (g *GitHubPlugin) ConfigSchema() plugin.Schema {
	return plugin.Schema{
		"type": "object",
		"properties": map[string]any{
			"base_url":      map[string]any{"type": "string", "default": "https://api.github.com"},
			"poll_interval": map[string]any{"type": "string", "default": "60s"},
			"poll_repos":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}

func (g *GitHubPlugin) Init(_ context.Context, cfg map[string]any, secrets plugin.SecretLookup) error {
	tok, err := secrets("github_token")
	if err != nil {
		return fmt.Errorf("github: resolve secret github_token: %w", err)
	}
	g.token = tok
	ws, err := secrets("github_webhook_secret")
	if err != nil {
		return fmt.Errorf("github: resolve secret github_webhook_secret: %w", err)
	}
	g.webhookSecret = ws // empty => webhook handler rejects all requests

	if u, ok := cfg["base_url"].(string); ok && u != "" {
		g.baseURL = strings.TrimRight(u, "/")
	} else {
		g.baseURL = "https://api.github.com"
	}

	g.pollInterval = time.Minute
	if v, ok := cfg["poll_interval"].(string); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("github: invalid poll_interval %q: %w", v, err)
		}
		g.pollInterval = d
	}
	g.pollRepos = nil
	if list, ok := cfg["poll_repos"].([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok && s != "" {
				g.pollRepos = append(g.pollRepos, s)
			}
		}
	}
	return nil
}

func (g *GitHubPlugin) Do(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "post_pr_comment":
		return g.postPRComment(ctx, params)
	default:
		return nil, fmt.Errorf("github: unknown action %q", action)
	}
}

func (g *GitHubPlugin) postPRComment(ctx context.Context, params map[string]any) (map[string]any, error) {
	repo := paramToString(params["repo"])
	prNumber := paramToString(params["pr_number"])
	body := paramToString(params["body"])
	if repo == "" || prNumber == "" || body == "" {
		return nil, fmt.Errorf("github post_pr_comment: repo, pr_number, and body are required")
	}
	// GitHub repo paths keep the literal slash: /repos/owner/repo/... (NOT %2F).
	// The issues comments endpoint is the canonical way to comment on a PR.
	url := fmt.Sprintf("%s/repos/%s/issues/%s/comments", g.baseURL, repo, prNumber)
	payload, _ := json.Marshal(map[string]string{"body": body})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("github: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: post comment: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github: post comment returned %d: %s", resp.StatusCode, truncateForErr(respBody))
	}
	var result map[string]any
	_ = json.Unmarshal(respBody, &result)
	return result, nil
}

// paramToString converts a parameter value to a string (string/int/float64).
func paramToString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case float64:
		return strconv.FormatInt(int64(val), 10)
	default:
		return ""
	}
}

func truncateForErr(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}
```

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/plugins/github/ -v && gofmt -l internal/plugins/github/ && go vet ./internal/plugins/github/`
Expected: 3 tests PASS; clean.

- [x] **Step 5: Commit**

```bash
git add internal/plugins/github/
git commit -m "feat(sp3b): github plugin core and post_pr_comment action"
```

---

### Task 5: GitHub events (webhook + poll)

**Files:**
- Create: `internal/plugins/github/events.go`
- Test: `internal/plugins/github/events_test.go`

Reference: `internal/plugins/gitlab/events.go` — read it first; this mirrors it with HMAC auth and client-side cursor filtering.

- [x] **Step 1: Write the failing tests** — `internal/plugins/github/events_test.go`:

```go
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
		"wrong secret":  sign(prWebhookFixture, "wrong"),
		"garbage":       "sha256=deadbeef",
		"missing":       "",
		"no prefix":     strings.TrimPrefix(sign(prWebhookFixture, "s3cret"), "sha256="),
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
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/plugins/github/ -v`
Expected: FAIL (`HandleWebhook`, `Poll` undefined).

- [x] **Step 3: Implement** — `internal/plugins/github/events.go`:

```go
package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/plugin"
)

var (
	_ plugin.WebhookSource = (*GitHubPlugin)(nil)
	_ plugin.PollSource    = (*GitHubPlugin)(nil)
)

const maxWebhookBody = 1 << 20 // 1 MiB

// actionToEventType maps pull_request webhook actions to envelope event types.
// closed is handled separately (merged flag splits pr_merged/pr_closed).
// Unlisted actions (reopened, edited, labeled, ...) are ignored in SP3b.
func actionToEventType(action string, merged bool) (string, bool) {
	switch action {
	case "opened":
		return "pr_opened", true
	case "synchronize":
		return "pr_updated", true
	case "closed":
		if merged {
			return "pr_merged", true
		}
		return "pr_closed", true
	}
	return "", false
}

type webhookPRPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Merged  bool   `json:"merged"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// HandleWebhook implements plugin.WebhookSource for GitHub webhooks.
// Auth: X-Hub-Signature-256 = "sha256=" + hex HMAC-SHA256(raw body, secret).
func (g *GitHubPlugin) HandleWebhook(_ context.Context, r *http.Request) ([]domain.Event, error) {
	if g.webhookSecret == "" {
		return nil, &plugin.AuthError{Msg: "github: webhook secret not configured (set secret github_webhook_secret)"}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		return nil, fmt.Errorf("github: read webhook body: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(g.webhookSecret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	got := r.Header.Get("X-Hub-Signature-256")
	if !hmac.Equal([]byte(got), []byte(want)) {
		return nil, &plugin.AuthError{Msg: "github: invalid webhook signature"}
	}

	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		return nil, nil // not a PR event; acknowledged and ignored
	}
	var payload webhookPRPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("github: parse webhook: %w", err)
	}
	eventType, ok := actionToEventType(payload.Action, payload.PullRequest.Merged)
	if !ok {
		return nil, nil // unhandled action; acknowledged and ignored
	}

	pr := payload.PullRequest
	ev := normalizePR(eventType, payload.Repository.FullName, pr.Number, pr.Title, payload.Action,
		pr.Head.Ref, pr.Base.Ref, pr.Head.SHA, pr.User.Login, pr.HTMLURL, body)
	return []domain.Event{ev}, nil
}

// normalizePR builds the shared envelope both ingestion surfaces emit.
// The dedup key changes only when the PR head SHA changes.
func normalizePR(eventType, repo string, number int, title, action, sourceBranch, targetBranch, sha, author, htmlURL string, raw []byte) domain.Event {
	return domain.Event{
		SourcePlugin: "github",
		EventType:    eventType,
		PayloadRaw:   json.RawMessage(raw),
		PayloadNorm: map[string]any{
			"repo":          repo,
			"pr_number":     number,
			"title":         title,
			"action":        action,
			"source_branch": sourceBranch,
			"target_branch": targetBranch,
			"head_sha":      sha,
			"author":        author,
			"url":           htmlURL,
		},
		Identity: author,
		DedupKey: fmt.Sprintf("pr:%s:%d:%s", repo, number, sha),
	}
}

type pollPR struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

// Poll implements plugin.PollSource over the pulls list API. GitHub has no
// updated_after server-side filter, so results are filtered CLIENT-SIDE to
// updated_at > cursor. Cursor discipline matches the GitLab plugin: a single
// RFC3339 cursor advancing only when every repo polled successfully and never
// from a zero time; partial failure returns gathered events with the
// unchanged cursor; total failure errors. Poll-discovered PRs emit pr_updated.
func (g *GitHubPlugin) Poll(ctx context.Context, cursor string) ([]domain.Event, string, error) {
	if len(g.pollRepos) == 0 {
		return nil, cursor, nil
	}
	since := cursor
	if since == "" {
		since = time.Now().Add(-g.pollInterval).UTC().Format(time.RFC3339)
	}
	sinceT, _ := time.Parse(time.RFC3339, since) // zero time on parse failure: filter passes everything; dedup absorbs

	var events []domain.Event
	maxUpdated := sinceT
	allOK := true
	failures := 0
	for _, repo := range g.pollRepos {
		prs, err := g.fetchOpenPRs(ctx, repo)
		if err != nil {
			allOK = false
			failures++
			continue
		}
		for _, pr := range prs {
			if !pr.UpdatedAt.After(sinceT) {
				continue // client-side cursor filter
			}
			raw, _ := json.Marshal(pr)
			events = append(events, normalizePR("pr_updated", repo, pr.Number, pr.Title, "synchronize",
				pr.Head.Ref, pr.Base.Ref, pr.Head.SHA, pr.User.Login, pr.HTMLURL, raw))
			if pr.UpdatedAt.After(maxUpdated) {
				maxUpdated = pr.UpdatedAt
			}
		}
	}
	if failures == len(g.pollRepos) {
		return nil, cursor, fmt.Errorf("github poll: all %d repos failed", failures)
	}
	newCursor := cursor
	// Never rewrite the cursor from a zero time (unparseable external cursor
	// + empty pages would otherwise force a full-history re-scan).
	if allOK && !maxUpdated.IsZero() {
		newCursor = maxUpdated.UTC().Format(time.RFC3339)
	}
	return events, newCursor, nil
}

func (g *GitHubPlugin) fetchOpenPRs(ctx context.Context, repo string) ([]pollPR, error) {
	// No pagination: only the first page (GitHub default 30) is read per tick.
	// sort=updated&direction=asc makes this self-healing — the cursor advances
	// only to the last RETURNED item's updated_at; later items arrive next tick.
	endpoint := fmt.Sprintf("%s/repos/%s/pulls?state=open&sort=updated&direction=asc&per_page=30",
		g.baseURL, repo)
	if _, err := url.Parse(endpoint); err != nil {
		return nil, fmt.Errorf("github: bad poll endpoint: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("github: build poll request: %w", err)
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: poll %s: %w", repo, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*maxWebhookBody))
	if err != nil {
		return nil, fmt.Errorf("github: read poll response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: poll %s returned %d: %s", repo, resp.StatusCode, truncateForErr(body))
	}
	var prs []pollPR
	if err := json.Unmarshal(body, &prs); err != nil {
		return nil, fmt.Errorf("github: parse poll response: %w", err)
	}
	return prs, nil
}
```

- [x] **Step 4: Run to verify pass**

Run: `go test ./internal/plugins/github/ -race -count=2 -v && gofmt -l internal/plugins/github/ && go vet ./internal/plugins/github/`
Expected: all tests PASS twice; clean.

- [x] **Step 5: Commit**

```bash
git add internal/plugins/github/
git commit -m "feat(sp3b): github pr_events source — HMAC webhook and client-filtered poll"
```

---

### Task 6: Wiring, sample config, integration test, final verification

**Files:**
- Modify: `cmd/fleet/main.go` (blank imports)
- Modify: `configs/fleet.yaml` (commented samples)
- Create: `internal/run/event_github_integration_test.go`

- [x] **Step 1: Add the blank imports.** In `cmd/fleet/main.go`, the import block ends with:

```go
	// Register all plugins via init().
	_ "github.com/cbarraford/office-fleet/internal/plugins/gitlab"
```

Extend it to:

```go
	// Register all plugins via init().
	_ "github.com/cbarraford/office-fleet/internal/plugins/discord"
	_ "github.com/cbarraford/office-fleet/internal/plugins/email"
	_ "github.com/cbarraford/office-fleet/internal/plugins/github"
	_ "github.com/cbarraford/office-fleet/internal/plugins/gitlab"
	_ "github.com/cbarraford/office-fleet/internal/plugins/slack"
```

- [x] **Step 2: Add commented samples to `configs/fleet.yaml`.** In the `plugins:` section, after the gitlab entry's commented poll lines, add:

```yaml
# Example: GitHub integration (SP3b) — pr_events source + post_pr_comment action.
# Secrets: github_token, github_webhook_secret. Webhook path: /webhooks/github
#  - name: github
#    config:
#      base_url: "https://api.github.com"
#      poll_interval: 60s
#      poll_repos: ["owner/repo"]

# Example: Slack notifications (SP3b) — send_message action. Secret: slack_bot_token
#  - name: slack
#    config:
#      base_url: "https://slack.com/api"

# Example: Discord notifications (SP3b) — send_message action.
# Secret discord_webhook_url holds the incoming-webhook URL (param 'webhook'
# can name an alternative secret per channel).
#  - name: discord

# Example: Email notifications (SP3b) — send_email action. Secret: smtp_password (optional)
#  - name: email
#    config:
#      smtp_host: smtp.example.com
#      smtp_port: "587"
#      from: fleet@example.com
```

And at the end of the `assignments:` section, add a commented example showing a GitHub event subscription plus a Slack output:

```yaml
# Example (SP3b): review GitHub PRs on open, notify Slack when done.
#  - agent: dev-1
#    duty: mr-reviewer
#    enabled: true
#    trigger:
#      kind: event-subscription
#      filter:
#        source: github
#        event_type: pr_opened
#        repo: "owner/repo"
#    config:
#      project: "owner/repo"
#    outputs:
#      - plugin: github
#        action: post_pr_comment
#        params:
#          repo: "{{.Event.repo}}"
#          pr_number: "{{.Event.pr_number}}"
#          body: "{{.Event.llm_summary}}"
#      - plugin: slack
#        action: send_message
#        params:
#          channel: "#code-reviews"
#          text: "Reviewed {{.Event.url}}"
```

(No `${env:` patterns anywhere in the comments.)

- [x] **Step 3: Write the integration test** — `internal/run/event_github_integration_test.go` (package `run`, mirroring the SP3 vertical; reuses `fakeRunRepo`, `fakeAssignmentGetter`/`fakeAgentLister`/`fakeDutyLister`, `deliveryRecorder`, `staticAssignmentLister`, `waitForCondition` — all already in this package; do NOT redefine them):

```go
package run

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

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/events"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/server"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/google/uuid"

	// Registers the github plugin.
	_ "github.com/cbarraford/office-fleet/internal/plugins/github"
)

const githubIntegrationFixture = `{
  "action": "opened",
  "pull_request": {
    "number": 9,
    "title": "Integration PR",
    "merged": false,
    "html_url": "https://github.com/org/repo/pull/9",
    "head": {"ref": "feat/z", "sha": "cafef00d"},
    "base": {"ref": "main"},
    "user": {"login": "carol"}
  },
  "repository": {"full_name": "org/repo"}
}`

func TestEventVertical_GitHubWebhookToRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gh, ok := plugin.Get("github")
	if !ok {
		t.Fatal("github plugin not registered")
	}
	secrets := func(name string) (string, error) {
		if name == "github_webhook_secret" {
			return "gh-integration-secret", nil
		}
		return "", nil
	}
	if err := gh.Init(ctx, map[string]any{}, secrets); err != nil {
		t.Fatal(err)
	}

	recorder := &deliveryRecorder{name: "sp3b-recorder-plugin"}
	plugin.Register(recorder)

	backendName := "sp3b-backend"
	agentID, dutyID, assignmentID := uuid.New(), uuid.New(), uuid.New()
	cfg := &config.Config{Backends: []config.Backend{{
		Name: backendName, Kind: "claude", Model: "m",
		DefaultEffort: "normal", Auth: config.BackendAuth{Mode: "subscription"},
	}}}
	rr := newFakeRunRepo()
	pipeline := &Pipeline{cfg: cfg, runRepo: rr, store: state.NewMemStore()}
	fakeExec := executor.NewFakeExecutor(domain.LLMResult{Status: 0, Summary: "pr-reviewed"})

	assignment := &domain.Assignment{
		ID: assignmentID, AgentID: agentID, DutyID: dutyID, Enabled: true,
		Backend: &domain.BackendRef{Name: backendName},
		Config:  map[string]any{},
		Trigger: domain.TriggerConfig{Kind: "event-subscription", Filter: map[string]any{
			"source": "github", "event_type": "pr_opened", "repo": "org/repo",
		}},
		Outputs: []domain.OutputBinding{{
			Plugin: "sp3b-recorder-plugin", Action: "post",
			Params: map[string]any{"body": "{{.Event.llm_summary}}", "pr": "{{.Event.pr_number}}"},
		}},
	}
	inv := &Invoker{
		cfg: cfg, pipeline: pipeline,
		assignments: &fakeAssignmentGetter{byID: map[uuid.UUID]*domain.Assignment{assignmentID: assignment}},
		agents: &fakeAgentLister{agents: []*domain.Agent{{
			ID: agentID, Name: "sp3b-agent", Role: "dev", SystemPrompt: "reviewer",
			DefaultBackend: domain.BackendRef{Name: backendName}, Enabled: true,
		}}},
		duties: &fakeDutyLister{duties: []*domain.Duty{{
			ID: dutyID, Name: "sp3b-duty", Role: "dev", Description: "d",
			Prompt: "Review PR #{{.Event.pr_number}} by {{.Event.author}}",
		}}},
		buildExecutor: func(_ *config.Config, _ *config.Backend) (executor.Executor, error) {
			return fakeExec, nil
		},
	}

	store := events.NewMemStore()
	dispatcher := events.NewDispatcher(store, &staticAssignmentLister{list: []*domain.Assignment{assignment}}, inv, 2, 50*time.Millisecond)
	ingestor := events.NewIngestor(store, dispatcher.Notify)
	go dispatcher.Run(ctx)

	httpSrv := httptest.NewServer(server.New(ingestor).Handler())
	defer httpSrv.Close()

	mac := hmac.New(sha256.New, []byte("gh-integration-secret"))
	mac.Write([]byte(githubIntegrationFixture))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/webhooks/github",
		strings.NewReader(githubIntegrationFixture))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "pull_request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook status = %d", resp.StatusCode)
	}

	waitForCondition(t, 3*time.Second, func() bool {
		return len(rr.snapshot()) >= 1
	}, "no run recorded")

	var run domain.Run
	for _, r := range rr.snapshot() {
		run = r
	}
	if run.Status != domain.RunStatusSucceeded {
		t.Errorf("run status = %q", run.Status)
	}
	if run.EventID == nil {
		t.Fatal("run.EventID not stamped")
	}
	if !strings.Contains(run.RenderedPrompt, "#9") || !strings.Contains(run.RenderedPrompt, "carol") {
		t.Errorf("rendered prompt = %q, want PR fields", run.RenderedPrompt)
	}
	if recorder.params["body"] != "pr-reviewed" || recorder.params["pr"] != "9" {
		t.Errorf("delivered params = %v", recorder.params)
	}

	evID := uuid.MustParse(*run.EventID)
	waitForCondition(t, 2*time.Second, func() bool {
		ev, err := store.GetByID(ctx, evID)
		return err == nil && ev.Status == domain.EventStatusDispatched
	}, "event not marked dispatched")
}
```

NOTE: check `rr.snapshot()`'s actual return type in pipeline_test.go (`map[uuid.UUID]domain.Run` — value copies) and adapt the `var run domain.Run` loop if it differs. `deliveryRecorder.params` may race with the assertion if delivery happens concurrently — the waitForCondition on `len(rr.snapshot()) >= 1` only guarantees Insert happened, not delivery completion. To be safe, ALSO wait for the recorder:

```go
	waitForCondition(t, 3*time.Second, func() bool {
		return recorder.params != nil
	}, "no output delivered")
```

placed before the recorder assertions (note: `deliveryRecorder.params` is written by the dispatcher worker goroutine — if the race detector flags it, add a small mutex to `deliveryRecorder` in pipeline_endpoint_test.go and a locked accessor, mirroring the fakeRunRepo treatment; report if you had to).

- [x] **Step 4: Run everything**

```bash
go test ./internal/run/ -run TestEventVertical_GitHub -race -count=2 -v
go test ./... -count=1
go test ./internal/... -race -count=1
gofmt -l . && go vet ./...
FLEET_DATABASE_DSN=postgres://localhost/x go run ./cmd/fleet --config configs/fleet.yaml config validate
```
Expected: all green; validate OK (new blocks are commented).

- [x] **Step 5: Framework-frozen check**

```bash
git diff HEAD --stat -- internal/plugin/ internal/events/ internal/server/ internal/run/pipeline.go internal/run/invoker.go internal/run/dispatcher*
git diff 790fad3 --stat -- internal/plugin/ internal/events/ internal/server/
```
Expected: NO production changes in framework packages across the whole SP3b range (test files in internal/run are the only additions there; a mutex addition to deliveryRecorder in pipeline_endpoint_test.go is acceptable).

- [x] **Step 6: Commit and push**

```bash
git add cmd/fleet/main.go configs/fleet.yaml internal/run/
git commit -m "feat(sp3b): register plugins, sample config, github webhook integration test"
git push origin master
```

---

## Acceptance criteria traceability (spec §8)

| Spec AC | Covered by |
|---|---|
| 1. GitHub webhook signed→dispatched end-to-end; bad/missing sig or unset secret → 401 | Task 5 (auth tests), Task 6 (vertical) |
| 2. Poll parity (same dedup_key), client-side filtering, cursor discipline, overlap one row | Task 5 (poll tests; overlap via SP3's event-level dedup already proven) |
| 3. Four actions deliver via outputs.Deliver with failures surfaced (Slack ok:false incl.) | Tasks 1–4 action tests; Task 6 vertical delivers through outputs |
| 4. Discord URLs only from secrets (default + override), never in params | Task 2 tests |
| 5. Email validates at Init; real net/smtp against in-process fake | Task 3 tests |
| 6. All four register + ConfigSchema; sample validates; serve picks up PollSource with zero serve changes | Task 6 (imports/sample/validate; serve's existing type-assertion loop) |
| 7. SP1–SP3 unchanged; no diffs outside plugins/main.go/fleet.yaml/tests; gofmt/vet clean | Task 6 steps 4–5 |
