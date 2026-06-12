package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

func init() {
	plugin.Register(&GitLabPlugin{})
}

// GitLabPlugin is the GitLab integration plugin: actions (post_mr_comment,
// post_inline_comment, create_issue, reply_to_discussion) plus the mr_events
// and mr_notes sources (webhook push + poll).
type GitLabPlugin struct {
	token         string
	baseURL       string
	webhookSecret string
	botUsername   string
	pollProjects  []string
	pollInterval  time.Duration
}

func (g *GitLabPlugin) Name() string { return "gitlab" }

func (g *GitLabPlugin) EventSources() []plugin.EventSource {
	return []plugin.EventSource{
		{Name: "mr_events", Description: "Merge request opened/updated/merged/closed events (webhook + poll)"},
		{Name: "mr_notes", Description: "Merge request comment (note) events (webhook only)"},
	}
}

func (g *GitLabPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "post_mr_comment", Description: "Post a comment on a merge request"},
		{Name: "post_inline_comment", Description: "Post a positioned diff comment (falls back to a plain note on stale positions)"},
		{Name: "create_issue", Description: "Create a GitLab issue"},
		{Name: "reply_to_discussion", Description: "Reply to a merge request discussion thread"},
		{Name: "resolve_discussion", Description: "Resolve a discussion thread (stub)"},
	}
}

func (g *GitLabPlugin) ConfigSchema() plugin.Schema {
	return plugin.Schema{
		"type": "object",
		"properties": map[string]any{
			"base_url":      map[string]any{"type": "string", "default": "https://gitlab.com"},
			"poll_interval": map[string]any{"type": "string", "default": "60s"},
			"poll_projects": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"bot_username":  map[string]any{"type": "string", "description": "The fleet's own GitLab username; its notes are dropped at ingestion to prevent reply loops"},
		},
	}
}

func (g *GitLabPlugin) Init(_ context.Context, cfg map[string]any, secrets plugin.SecretLookup) error {
	tok, err := secrets("gitlab_token")
	if err != nil {
		return fmt.Errorf("gitlab: resolve secret gitlab_token: %w", err)
	}
	if tok == "" {
		return fmt.Errorf("gitlab: secret gitlab_token is not configured")
	}
	g.token = tok
	if u, ok := cfg["base_url"].(string); ok && u != "" {
		g.baseURL = strings.TrimRight(u, "/")
	} else {
		g.baseURL = "https://gitlab.com"
	}

	// Webhook secret: optional at init; the webhook handler rejects all
	// requests when it is unset (push ingestion requires it).
	ws, err := secrets("gitlab_webhook_secret")
	if err != nil && !plugin.IsSecretNotFound(err) {
		return fmt.Errorf("gitlab: resolve secret gitlab_webhook_secret: %w", err)
	}
	g.webhookSecret = ws

	g.pollInterval = time.Minute
	if v, ok := cfg["poll_interval"].(string); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("gitlab: invalid poll_interval %q: %w", v, err)
		}
		g.pollInterval = d
	}
	g.pollProjects = nil
	if list, ok := cfg["poll_projects"].([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok && s != "" {
				g.pollProjects = append(g.pollProjects, s)
			}
		}
	}
	if v, ok := cfg["bot_username"].(string); ok {
		g.botUsername = v
	}
	return nil
}

func (g *GitLabPlugin) Do(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "post_mr_comment":
		return g.postMRComment(ctx, params)
	case "post_inline_comment":
		return g.postInlineComment(ctx, params)
	case "create_issue":
		return g.createIssue(ctx, params)
	case "reply_to_discussion":
		return g.replyToDiscussion(ctx, params)
	case "resolve_discussion":
		return nil, fmt.Errorf("gitlab: %s not yet implemented", action)
	default:
		return nil, fmt.Errorf("gitlab: unknown action %q", action)
	}
}

// paramToString converts a parameter value to a string, handling string, int, and float64 types.
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

// encodeProject URL-encodes a GitLab project path (replaces / with %2F).
// GitLab's API requires literal %2F in the path, not url.PathEscape which
// would double-encode it in some contexts.
func encodeProject(project string) string {
	return strings.ReplaceAll(project, "/", "%2F")
}

// apiJSON sends a JSON request to the GitLab API and decodes the JSON response.
func (g *GitLabPlugin) apiJSON(ctx context.Context, method, path string, payload any) (map[string]any, int, error) {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("gitlab: marshal payload: %w", err)
		}
		bodyReader = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("gitlab: create request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("gitlab: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBody))
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("gitlab: %s %s returned %d: %s", method, path, resp.StatusCode, truncateForErr(respBody))
	}
	var result map[string]any
	_ = json.Unmarshal(respBody, &result)
	return result, resp.StatusCode, nil
}

func (g *GitLabPlugin) postMRComment(ctx context.Context, params map[string]any) (map[string]any, error) {
	project := paramToString(params["project"])
	mrIID := paramToString(params["mr_iid"])
	body := paramToString(params["body"])
	if project == "" || mrIID == "" || body == "" {
		return nil, fmt.Errorf("gitlab post_mr_comment: project, mr_iid, and body are required")
	}
	notesPath := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/notes", encodeProject(project), mrIID)
	result, _, err := g.apiJSON(ctx, http.MethodPost, notesPath, map[string]string{"body": body})
	return result, err
}

func (g *GitLabPlugin) postInlineComment(ctx context.Context, params map[string]any) (map[string]any, error) {
	project := paramToString(params["project"])
	mrIID := paramToString(params["mr_iid"])
	path := paramToString(params["path"])
	line := paramToString(params["line"])
	body := paramToString(params["body"])
	if project == "" || mrIID == "" || path == "" || line == "" || body == "" {
		return nil, fmt.Errorf("gitlab post_inline_comment: project, mr_iid, path, line, and body are required")
	}

	// Latest diff version supplies the position SHAs.
	versionsPath := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/versions", encodeProject(project), mrIID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+versionsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("gitlab: create request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: fetch versions: %w", err)
	}
	defer resp.Body.Close()
	vBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBody))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gitlab: versions returned %d: %s", resp.StatusCode, truncateForErr(vBody))
	}
	var versions []struct {
		HeadSHA  string `json:"head_commit_sha"`
		BaseSHA  string `json:"base_commit_sha"`
		StartSHA string `json:"start_commit_sha"`
	}
	if err := json.Unmarshal(vBody, &versions); err != nil || len(versions) == 0 {
		return nil, fmt.Errorf("gitlab: no diff versions for MR %s", mrIID)
	}
	v := versions[0] // GitLab returns newest first

	discussionsPath := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/discussions", encodeProject(project), mrIID)
	// GitLab's JSON API expects new_line as an integer; params arrive as
	// strings from templates ({{.Item.line}}) — coerce when numeric.
	var newLine any = line
	if n, err := strconv.Atoi(line); err == nil {
		newLine = n
	}
	payload := map[string]any{
		"body": body,
		"position": map[string]any{
			"position_type": "text",
			"head_sha":      v.HeadSHA,
			"base_sha":      v.BaseSHA,
			"start_sha":     v.StartSHA,
			"new_path":      path,
			"new_line":      newLine,
		},
	}
	result, status, err := g.apiJSON(ctx, http.MethodPost, discussionsPath, payload)
	if err == nil {
		return result, nil
	}
	// Stale line numbers are routine (the diff moved): fall back to a plain
	// note carrying the location so the finding is never lost.
	if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
		note, nErr := g.postMRComment(ctx, map[string]any{
			"project": project, "mr_iid": mrIID,
			"body": fmt.Sprintf("**%s:%s** — %s", path, line, body),
		})
		if nErr != nil {
			return nil, fmt.Errorf("gitlab: inline position rejected (%v) and note fallback failed: %w", err, nErr)
		}
		if note == nil {
			note = map[string]any{}
		}
		note["fallback"] = "note"
		return note, nil
	}
	return nil, err
}

func (g *GitLabPlugin) createIssue(ctx context.Context, params map[string]any) (map[string]any, error) {
	project := paramToString(params["project"])
	title := paramToString(params["title"])
	description := paramToString(params["description"])
	labels := paramToString(params["labels"]) // optional, comma-separated
	if project == "" || title == "" {
		return nil, fmt.Errorf("gitlab create_issue: project and title are required")
	}
	payload := map[string]any{"title": title, "description": description}
	if labels != "" {
		payload["labels"] = labels
	}
	result, _, err := g.apiJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v4/projects/%s/issues", encodeProject(project)), payload)
	return result, err
}

func (g *GitLabPlugin) replyToDiscussion(ctx context.Context, params map[string]any) (map[string]any, error) {
	project := paramToString(params["project"])
	mrIID := paramToString(params["mr_iid"])
	discussionID := paramToString(params["discussion_id"])
	body := paramToString(params["body"])
	if project == "" || mrIID == "" || discussionID == "" || body == "" {
		return nil, fmt.Errorf("gitlab reply_to_discussion: project, mr_iid, discussion_id, and body are required")
	}
	result, _, err := g.apiJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/discussions/%s/notes", encodeProject(project), mrIID, url.PathEscape(discussionID)),
		map[string]any{"body": body})
	return result, err
}
