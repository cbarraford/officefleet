package gitlab

import (
	"context"
	"crypto/subtle"
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
	_ plugin.WebhookSource = (*GitLabPlugin)(nil)
	_ plugin.PollSource    = (*GitLabPlugin)(nil)
)

const maxWebhookBody = 1 << 20 // 1 MiB

// actionToEventType maps GitLab MR webhook actions to envelope event types.
// Unlisted actions (approved, reopen, ...) are ignored in SP3.
func actionToEventType(action string) (string, bool) {
	switch action {
	case "open":
		return "mr_opened", true
	case "update":
		return "mr_updated", true
	case "merge":
		return "mr_merged", true
	case "close":
		return "mr_closed", true
	}
	return "", false
}

type webhookMRPayload struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Username string `json:"username"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		Action       string `json:"action"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		URL          string `json:"url"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
}

type webhookNotePayload struct {
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	MergeRequest struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		SourceBranch string `json:"source_branch"`
	} `json:"merge_request"`
	ObjectAttributes struct {
		ID           int    `json:"id"`
		DiscussionID string `json:"discussion_id"`
		Note         string `json:"note"`
		NoteableType string `json:"noteable_type"`
		URL          string `json:"url"`
	} `json:"object_attributes"`
}

// HandleWebhook implements plugin.WebhookSource for GitLab webhooks.
func (g *GitLabPlugin) HandleWebhook(_ context.Context, r *http.Request) ([]domain.Event, error) {
	if g.webhookSecret == "" {
		return nil, &plugin.AuthError{Msg: "gitlab: webhook secret not configured (set secret gitlab_webhook_secret)"}
	}
	token := r.Header.Get("X-Gitlab-Token")
	if subtle.ConstantTimeCompare([]byte(token), []byte(g.webhookSecret)) != 1 {
		return nil, &plugin.AuthError{Msg: "gitlab: invalid webhook token"}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		return nil, fmt.Errorf("gitlab: read webhook body: %w", err)
	}

	var kindProbe struct {
		ObjectKind string `json:"object_kind"`
	}
	if err := json.Unmarshal(body, &kindProbe); err != nil {
		return nil, fmt.Errorf("gitlab: parse webhook: %w", err)
	}
	switch kindProbe.ObjectKind {
	case "merge_request":
		var payload webhookMRPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("gitlab: parse webhook: %w", err)
		}
		eventType, ok := actionToEventType(payload.ObjectAttributes.Action)
		if !ok {
			return nil, nil // unhandled action; acknowledged and ignored
		}
		a := payload.ObjectAttributes
		ev := normalizeMR(eventType, payload.Project.PathWithNamespace, a.IID, a.Title, a.Action,
			a.SourceBranch, a.TargetBranch, a.LastCommit.ID, payload.User.Username, a.URL, body)
		return []domain.Event{ev}, nil
	case "note":
		return g.handleNoteWebhook(body)
	default:
		return nil, nil // not an event kind we ingest; acknowledged and ignored
	}
}

// handleNoteWebhook ingests MR comments as mr_note events. Notes by the
// configured bot_username are dropped here so the mr-feedback duty can never
// be triggered by its own replies (reply-loop protection).
func (g *GitLabPlugin) handleNoteWebhook(body []byte) ([]domain.Event, error) {
	var payload webhookNotePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("gitlab: parse note webhook: %w", err)
	}
	if payload.ObjectAttributes.NoteableType != "MergeRequest" {
		return nil, nil // issue/commit/snippet notes are out of scope
	}
	if g.botUsername != "" && payload.User.Username == g.botUsername {
		return nil, nil // our own note: drop to prevent reply loops
	}
	a := payload.ObjectAttributes
	return []domain.Event{{
		SourcePlugin: "gitlab",
		EventType:    "mr_note",
		PayloadRaw:   json.RawMessage(body),
		PayloadNorm: map[string]any{
			"project":          payload.Project.PathWithNamespace,
			"mr_iid":           payload.MergeRequest.IID,
			"mr_title":         payload.MergeRequest.Title,
			"mr_source_branch": payload.MergeRequest.SourceBranch,
			"note_id":          a.ID,
			"discussion_id":    a.DiscussionID,
			"note_body":        a.Note,
			"author":           payload.User.Username,
			"url":              a.URL,
		},
		Identity: payload.User.Username,
		DedupKey: fmt.Sprintf("note:%s:%d", payload.Project.PathWithNamespace, a.ID),
	}}, nil
}

// normalizeMR builds the shared envelope both ingestion surfaces emit.
// The dedup key changes only when the MR head SHA changes.
func normalizeMR(eventType, project string, iid int, title, action, sourceBranch, targetBranch, sha, author, mrURL string, raw []byte) domain.Event {
	return domain.Event{
		SourcePlugin: "gitlab",
		EventType:    eventType,
		PayloadRaw:   json.RawMessage(raw),
		PayloadNorm: map[string]any{
			"project":         project,
			"mr_iid":          iid,
			"title":           title,
			"action":          action,
			"source_branch":   sourceBranch,
			"target_branch":   targetBranch,
			"last_commit_sha": sha,
			"author":          author,
			"url":             mrURL,
		},
		Identity: author,
		DedupKey: fmt.Sprintf("mr:%s:%d:%s", project, iid, sha),
	}
}

type pollMR struct {
	IID          int       `json:"iid"`
	Title        string    `json:"title"`
	SHA          string    `json:"sha"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	WebURL       string    `json:"web_url"`
	UpdatedAt    time.Time `json:"updated_at"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
}

// Poll implements plugin.PollSource: it lists recently-updated open MRs per
// configured project. The cursor (RFC3339 of the max updated_at seen)
// advances only when EVERY project polled successfully — a partial failure
// returns the gathered events with the unchanged cursor (re-polling the
// healthy projects is harmless: event-level dedup collapses duplicates).
// Poll-discovered MRs emit mr_updated (poll cannot distinguish "opened").
func (g *GitLabPlugin) Poll(ctx context.Context, cursor string) ([]domain.Event, string, error) {
	if len(g.pollProjects) == 0 {
		return nil, cursor, nil
	}
	since := cursor
	if since == "" {
		since = time.Now().Add(-g.pollInterval).UTC().Format(time.RFC3339)
	}

	var events []domain.Event
	maxUpdated, _ := time.Parse(time.RFC3339, since)
	allOK := true
	failures := 0
	for _, project := range g.pollProjects {
		mrs, err := g.fetchUpdatedMRs(ctx, project, since)
		if err != nil {
			allOK = false
			failures++
			continue
		}
		for _, mr := range mrs {
			raw, _ := json.Marshal(mr)
			events = append(events, normalizeMR("mr_updated", project, mr.IID, mr.Title, "update",
				mr.SourceBranch, mr.TargetBranch, mr.SHA, mr.Author.Username, mr.WebURL, raw))
			if mr.UpdatedAt.After(maxUpdated) {
				maxUpdated = mr.UpdatedAt
			}
		}
	}
	if failures == len(g.pollProjects) {
		return nil, cursor, fmt.Errorf("gitlab poll: all %d projects failed", failures)
	}
	newCursor := cursor
	// Guard: never rewrite the cursor from a zero maxUpdated (possible only if
	// an externally-corrupted cursor failed to parse AND the page was empty) —
	// that would force a full-history re-scan.
	if allOK && !maxUpdated.IsZero() {
		newCursor = maxUpdated.UTC().Format(time.RFC3339)
	}
	return events, newCursor, nil
}

func (g *GitLabPlugin) fetchUpdatedMRs(ctx context.Context, project, since string) ([]pollMR, error) {
	// No pagination: only the first page (GitLab default 20) is read per tick.
	// sort=asc makes this self-healing — the cursor advances only to the last
	// RETURNED item's updated_at, so later items are picked up next tick.
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?state=opened&order_by=updated_at&sort=asc&updated_after=%s",
		g.baseURL, url.PathEscape(project), url.QueryEscape(since))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("gitlab: build poll request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	resp, err := gitlabHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: poll %s: %w", project, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*maxWebhookBody))
	if err != nil {
		return nil, fmt.Errorf("gitlab: read poll response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab: poll %s returned %d: %s", project, resp.StatusCode, truncateForErr(body))
	}
	var mrs []pollMR
	if err := json.Unmarshal(body, &mrs); err != nil {
		return nil, fmt.Errorf("gitlab: parse poll response: %w", err)
	}
	return mrs, nil
}

func truncateForErr(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}
