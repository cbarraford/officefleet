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
