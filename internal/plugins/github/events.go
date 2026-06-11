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
