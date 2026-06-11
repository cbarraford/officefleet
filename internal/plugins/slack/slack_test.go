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
