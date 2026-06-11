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

func TestSendMessage_TransportErrorDoesNotLeakURL(t *testing.T) {
	// The webhook URL is a secret (it embeds the token); transport errors
	// must never echo it into run records.
	secretURL := "http://127.0.0.1:1/api/webhooks/123/SECRETTOKEN"
	p := initPlugin(t, map[string]string{"discord_webhook_url": secretURL})
	_, err := p.Do(context.Background(), "send_message", map[string]any{"content": "x"})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") || strings.Contains(err.Error(), secretURL) {
		t.Fatalf("error leaks the webhook URL: %v", err)
	}
	if !strings.Contains(err.Error(), "discord_webhook_url") {
		t.Errorf("error should name the secret: %v", err)
	}
}

func TestSendMessage_BadURLDoesNotLeak(t *testing.T) {
	// "://not-a-url-SECRETPART" causes http.NewRequestWithContext to return a
	// parse error; the fix must not echo the URL (and thus the SECRETPART) back.
	bad := "://not-a-url-SECRETPART"
	p := initPlugin(t, map[string]string{"discord_webhook_url": bad})
	_, err := p.Do(context.Background(), "send_message", map[string]any{"content": "x"})
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
	if strings.Contains(err.Error(), "SECRETPART") {
		t.Fatalf("error leaks the malformed URL: %v", err)
	}
}
