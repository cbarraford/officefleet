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
