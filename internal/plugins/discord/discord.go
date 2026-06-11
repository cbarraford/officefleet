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
