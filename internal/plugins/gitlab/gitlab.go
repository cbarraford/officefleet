package gitlab

import (
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
	plugin.Register(&GitLabPlugin{})
}

// GitLabPlugin is the GitLab integration plugin (SP1 scope: post_mr_comment action).
type GitLabPlugin struct {
	token   string
	baseURL string
}

func (g *GitLabPlugin) Name() string { return "gitlab" }

func (g *GitLabPlugin) EventSources() []plugin.EventSource {
	return []plugin.EventSource{
		{Name: "mr_events", Description: "Merge request opened/updated events (wired in SP3)"},
	}
}

func (g *GitLabPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "post_mr_comment", Description: "Post a comment on a merge request"},
		{Name: "resolve_discussion", Description: "Resolve a discussion thread (stub, SP3)"},
		{Name: "create_issue", Description: "Create a GitLab issue (stub, SP3)"},
	}
}

func (g *GitLabPlugin) ConfigSchema() plugin.Schema {
	return plugin.Schema{
		"type": "object",
		"properties": map[string]any{
			"base_url": map[string]any{"type": "string", "default": "https://gitlab.com"},
		},
	}
}

func (g *GitLabPlugin) Init(_ context.Context, cfg map[string]any, secrets plugin.SecretLookup) error {
	tok, err := secrets("gitlab_token")
	if err != nil {
		return fmt.Errorf("gitlab: resolve secret gitlab_token: %w", err)
	}
	g.token = tok
	if u, ok := cfg["base_url"].(string); ok && u != "" {
		g.baseURL = strings.TrimRight(u, "/")
	} else {
		g.baseURL = "https://gitlab.com"
	}
	return nil
}

func (g *GitLabPlugin) Do(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "post_mr_comment":
		return g.postMRComment(ctx, params)
	case "resolve_discussion", "create_issue":
		return nil, fmt.Errorf("gitlab: %s not yet implemented (SP3)", action)
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

func (g *GitLabPlugin) postMRComment(ctx context.Context, params map[string]any) (map[string]any, error) {
	project := paramToString(params["project"])
	mrIID := paramToString(params["mr_iid"])
	body := paramToString(params["body"])
	if project == "" || mrIID == "" || body == "" {
		return nil, fmt.Errorf("gitlab post_mr_comment: project, mr_iid, and body are required")
	}
	encodedProject := strings.ReplaceAll(project, "/", "%2F")
	url := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/notes", g.baseURL, encodedProject, mrIID)
	payload, _ := json.Marshal(map[string]string{"body": body})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("gitlab: create request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: post comment: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gitlab: post comment returned %d: %s", resp.StatusCode, respBody)
	}
	var result map[string]any
	_ = json.Unmarshal(respBody, &result)
	return result, nil
}
