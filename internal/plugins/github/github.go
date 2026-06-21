// Package github provides the GitHub integration plugin: the pr_events source
// (webhook + poll, see events.go) and the post_change_comment and
// post_inline_comment actions (post_pr_comment is kept as a back-compat alias).
// GitHub Enterprise is supported via the base_url config.
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

// httpClient bounds every GitHub API call so a stalled server cannot hang a run
// (issue #8).
var httpClient = &http.Client{Timeout: 30 * time.Second}

// GitHubPlugin integrates GitHub: PR events in, PR comments out.
type GitHubPlugin struct {
	token         string
	baseURL       string
	webhookSecret string
	pollRepos     []string
	pollInterval  time.Duration
	botUsername   string
}

func (g *GitHubPlugin) Name() string { return "github" }

func (g *GitHubPlugin) EventSources() []plugin.EventSource {
	return []plugin.EventSource{
		{Name: "pr_events", Description: "Pull request opened/updated/merged/closed events (webhook + poll)"},
	}
}

func (g *GitHubPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "post_change_comment", Description: "Post a comment on a pull request"},
		{Name: "post_inline_comment", Description: "Post a positioned PR review comment (falls back to a plain comment on stale positions)"},
		{Name: "create_issue", Description: "Create a GitHub issue"},
	}
}

func (g *GitHubPlugin) ConfigSchema() plugin.Schema {
	return plugin.Schema{
		"type": "object",
		"properties": map[string]any{
			"base_url":      map[string]any{"type": "string", "default": "https://api.github.com"},
			"poll_interval": map[string]any{"type": "string", "default": "60s"},
			"poll_repos":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"bot_username":  map[string]any{"type": "string", "description": "The fleet's own GitHub username; its comments are dropped at ingestion to prevent reply loops"},
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
	if v, ok := cfg["bot_username"].(string); ok {
		g.botUsername = v
	}
	return nil
}

func (g *GitHubPlugin) Do(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "post_pr_comment", "post_change_comment": // post_pr_comment kept as a back-compat alias
		return g.postPRComment(ctx, params)
	case "post_inline_comment":
		return g.postInlineComment(ctx, params)
	case "create_issue":
		return g.createIssue(ctx, params)
	default:
		return nil, fmt.Errorf("github: unknown action %q", action)
	}
}

func (g *GitHubPlugin) postPRComment(ctx context.Context, params map[string]any) (map[string]any, error) {
	repo := firstParam(params, "project", "repo")
	prNumber := firstParam(params, "mr_iid", "pr_number")
	body := paramToString(params["body"])
	if repo == "" || prNumber == "" || body == "" {
		return nil, fmt.Errorf("github post_change_comment: project/repo, mr_iid/pr_number, and body are required")
	}
	// The issues/comments endpoint is the canonical way to comment on a PR.
	url := fmt.Sprintf("%s/repos/%s/issues/%s/comments", g.baseURL, repo, prNumber)
	result, _, err := g.apiJSON(ctx, http.MethodPost, url, map[string]string{"body": body})
	return result, err
}

func (g *GitHubPlugin) postInlineComment(ctx context.Context, params map[string]any) (map[string]any, error) {
	repo := firstParam(params, "project", "repo")
	prNumber := firstParam(params, "mr_iid", "pr_number")
	path := paramToString(params["path"])
	line := paramToString(params["line"])
	body := paramToString(params["body"])
	if repo == "" || prNumber == "" || path == "" || line == "" || body == "" {
		return nil, fmt.Errorf("github post_inline_comment: project, mr_iid, path, line, and body are required")
	}
	// The review-comment API positions against the PR's head commit SHA.
	sha, err := g.prHeadSHA(ctx, repo, prNumber)
	if err != nil {
		return nil, err
	}
	var newLine any = line
	if n, err := strconv.Atoi(line); err == nil {
		newLine = n
	}
	url := fmt.Sprintf("%s/repos/%s/pulls/%s/comments", g.baseURL, repo, prNumber)
	payload := map[string]any{
		"body": body, "commit_id": sha, "path": path, "line": newLine, "side": "RIGHT",
	}
	result, status, err := g.apiJSON(ctx, http.MethodPost, url, payload)
	if err == nil {
		return result, nil
	}
	// Stale line numbers are routine (the diff moved): fall back to a plain PR
	// comment carrying the location so the finding is never lost (mirrors GitLab).
	if status == http.StatusUnprocessableEntity || status == http.StatusBadRequest {
		note, nErr := g.postPRComment(ctx, map[string]any{
			"project": repo, "mr_iid": prNumber,
			"body": fmt.Sprintf("**%s:%s** — %s", path, line, body),
		})
		if nErr != nil {
			return nil, fmt.Errorf("github: inline position rejected (%v) and note fallback failed: %w", err, nErr)
		}
		if note == nil {
			note = map[string]any{}
		}
		note["fallback"] = "note"
		return note, nil
	}
	return nil, err
}

func (g *GitHubPlugin) createIssue(ctx context.Context, params map[string]any) (map[string]any, error) {
	repo := firstParam(params, "project", "repo")
	title := paramToString(params["title"])
	description := paramToString(params["description"])
	labels := paramToString(params["labels"]) // comma-separated, optional
	if repo == "" || title == "" {
		return nil, fmt.Errorf("github create_issue: project and title are required")
	}
	payload := map[string]any{"title": title, "body": description}
	var labelList []string
	for _, l := range strings.Split(labels, ",") {
		if t := strings.TrimSpace(l); t != "" {
			labelList = append(labelList, t)
		}
	}
	if len(labelList) > 0 {
		payload["labels"] = labelList
	}
	url := fmt.Sprintf("%s/repos/%s/issues", g.baseURL, repo)
	result, _, err := g.apiJSON(ctx, http.MethodPost, url, payload)
	return result, err
}

// prHeadSHA fetches the PR's current head commit SHA, required to position a
// review comment.
func (g *GitHubPlugin) prHeadSHA(ctx context.Context, repo, prNumber string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/pulls/%s", g.baseURL, repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("github: create request: %w", err)
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github: fetch PR: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("github: fetch PR returned %d: %s", resp.StatusCode, truncateForErr(b))
	}
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(b, &pr); err != nil || pr.Head.SHA == "" {
		return "", fmt.Errorf("github: PR %s has no head sha", prNumber)
	}
	return pr.Head.SHA, nil
}

// firstParam returns the first non-empty stringified value among keys. It lets
// GitHub actions accept lingua-franca keys (project/mr_iid) or the native ones
// (repo/pr_number), so one outputs block works on both forges.
func firstParam(params map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := paramToString(params[k]); s != "" {
			return s
		}
	}
	return ""
}

// apiJSON sends a JSON request to the GitHub API and returns the decoded body
// plus the HTTP status (callers branch on 422/400 for the inline fallback).
func (g *GitHubPlugin) apiJSON(ctx context.Context, method, url string, payload any) (map[string]any, int, error) {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("github: marshal payload: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("github: create request: %w", err)
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("github: %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("github: %s returned %d: %s", method, resp.StatusCode, truncateForErr(respBody))
	}
	var result map[string]any
	_ = json.Unmarshal(respBody, &result)
	return result, resp.StatusCode, nil
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
