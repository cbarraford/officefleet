# Dual-forge Group E: code-feedback + review-finding-reply — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make `code-feedback` and `review-finding-reply` run on both forges. Both react to a comment on a bot change (GitLab `mr_note`; GitHub `pr_comment`), reply, and (review-finding-reply) optionally resolve the thread.

**Architecture:** Add a GitHub `pr_comment` event from TWO webhooks — `issue_comment` (PR conversation) and `pull_request_review_comment` (inline review threads) — normalized to GitLab's `mr_note` field names, with a bot-author drop (new github `bot_username` config) to prevent reply loops. Add a GitHub `reply_to_discussion` action (review-thread reply, falling back to a plain PR comment when there's no thread). Resolve stays in-prompt via `gh api graphql` (mirroring GitLab's in-prompt resolve), captured as a `resolveThreadHowto` Forge field. `issue_comment` payloads don't carry the PR branch, so the forge-ized prompts resolve the branch via the CLI when the event's `mr_source_branch` is empty.

**Tech Stack:** Go (`github.com/cbarraford/office-fleet`), `text/template` prompts, YAML config, table tests.

## Global Constraints

- Module path: `github.com/cbarraford/office-fleet`. Test command: `go test ./...` (render tests `t.Setenv` the DSN; verify with it unset).
- `pr_comment` is emitted ONLY for `action == "created"` comments; the bot's own comments (author == configured `bot_username`) are dropped; `issue_comment` events whose issue is NOT a pull request are dropped.
- `pr_comment` PayloadNorm uses GitLab `mr_note` key names: `project`, `mr_iid`, `mr_title`, `mr_source_branch`, `note_id`, `discussion_id`, `note_body`, `author`, `url`. For review comments, `discussion_id` = the comment id (reply target) and `mr_source_branch` = the PR head ref; for conversation (issue) comments both are empty strings.
- Reuse existing helpers `firstParam`, `apiJSON`, `paramToString`, `postPRComment`. Dedup key `note:<repo>:<comment_id>`.
- Back-compat: existing GitLab assignments unchanged; absent `forge` defaults to `gitlab`. New GitHub assignments `enabled: false`.
- Secrets reachable ONLY via `{{secret .Forge.tokenSecret}}` in prompts.
- Work continues on branch `feat/dual-forge-code-review` (PR #51). Do NOT create a new branch.

---

### Task 1: GitHub `pr_comment` event + `bot_username` config

**Files:**
- Modify: `internal/plugins/github/github.go` (struct field `botUsername`; `ConfigSchema`; `Init`)
- Modify: `internal/plugins/github/events.go` (route `issue_comment` + `pull_request_review_comment`; payload structs; `handleIssueComment`, `handleReviewComment`, `prCommentEvent`)
- Modify: `internal/plugins/github/events_test.go` (tests)

**Interfaces:**
- Produces: `pr_comment` events. `(g *GitHubPlugin) handleIssueComment(body []byte) ([]domain.Event, error)`, `handleReviewComment(body []byte)`, and `prCommentEvent(...)`. `g.botUsername` populated from `cfg["bot_username"]`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/plugins/github/events_test.go`:

```go
func TestHandleIssueComment_PR(t *testing.T) {
	g := &GitHubPlugin{}
	body := []byte(`{"action":"created","issue":{"number":12,"title":"Add X","pull_request":{"url":"u"}},"comment":{"id":555,"body":"please fix","html_url":"hu","user":{"login":"reviewer"}},"repository":{"full_name":"org/repo"}}`)
	evs, err := g.handleIssueComment(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].EventType != "pr_comment" {
		t.Fatalf("want 1 pr_comment, got %v", evs)
	}
	n := evs[0].PayloadNorm
	if n["project"] != "org/repo" || n["mr_iid"] != 12 || n["note_body"] != "please fix" || n["author"] != "reviewer" {
		t.Errorf("norm = %v", n)
	}
	if n["discussion_id"] != "" || n["mr_source_branch"] != "" {
		t.Errorf("conversation comment should have empty discussion_id/branch: %v", n)
	}
	if evs[0].DedupKey != "note:org/repo:555" {
		t.Errorf("dedup = %q", evs[0].DedupKey)
	}
}

func TestHandleIssueComment_DropsNonPRAndBotAndNonCreated(t *testing.T) {
	g := &GitHubPlugin{botUsername: "huginn"}
	// plain issue (no pull_request)
	plain := []byte(`{"action":"created","issue":{"number":1,"title":"t"},"comment":{"id":1,"body":"b","user":{"login":"x"}},"repository":{"full_name":"o/r"}}`)
	// bot's own comment
	bot := []byte(`{"action":"created","issue":{"number":1,"title":"t","pull_request":{"url":"u"}},"comment":{"id":2,"body":"b","user":{"login":"huginn"}},"repository":{"full_name":"o/r"}}`)
	// edited, not created
	edited := []byte(`{"action":"edited","issue":{"number":1,"title":"t","pull_request":{"url":"u"}},"comment":{"id":3,"body":"b","user":{"login":"x"}},"repository":{"full_name":"o/r"}}`)
	for _, b := range [][]byte{plain, bot, edited} {
		evs, err := g.handleIssueComment(b)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 0 {
			t.Errorf("want 0 events, got %d for %s", len(evs), b)
		}
	}
}

func TestHandleReviewComment_PR(t *testing.T) {
	g := &GitHubPlugin{}
	body := []byte(`{"action":"created","comment":{"id":777,"body":"nit","html_url":"hu","user":{"login":"reviewer"}},"pull_request":{"number":12,"title":"Add X","head":{"ref":"feat/x"}},"repository":{"full_name":"org/repo"}}`)
	evs, err := g.handleReviewComment(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].EventType != "pr_comment" {
		t.Fatalf("want 1 pr_comment, got %v", evs)
	}
	n := evs[0].PayloadNorm
	if n["mr_source_branch"] != "feat/x" {
		t.Errorf("source_branch = %v", n["mr_source_branch"])
	}
	if n["discussion_id"] != "777" {
		t.Errorf("discussion_id should be the comment id, got %v", n["discussion_id"])
	}
}

func TestHandleReviewComment_DropsBot(t *testing.T) {
	g := &GitHubPlugin{botUsername: "huginn"}
	body := []byte(`{"action":"created","comment":{"id":1,"body":"b","user":{"login":"huginn"}},"pull_request":{"number":1,"title":"t","head":{"ref":"b"}},"repository":{"full_name":"o/r"}}`)
	evs, err := g.handleReviewComment(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Errorf("bot's own review comment should be dropped, got %d", len(evs))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugins/github/ -run 'TestHandleIssueComment|TestHandleReviewComment'`
Expected: FAIL — `g.handleIssueComment undefined`.

- [ ] **Step 3: Add `bot_username` to the plugin**

In `internal/plugins/github/github.go`: add the struct field

```go
	botUsername   string
```

In `ConfigSchema`, add to `properties` (keep existing):

```go
				"bot_username": map[string]any{"type": "string", "description": "The fleet's own GitHub username; its comments are dropped at ingestion to prevent reply loops"},
```

In `Init`, after the poll_repos block, add:

```go
	if v, ok := cfg["bot_username"].(string); ok {
		g.botUsername = v
	}
```

- [ ] **Step 4: Add the payload structs, handlers, and routing in `events.go`**

Add to `internal/plugins/github/events.go`:

```go
type webhookIssueCommentPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		ID      int64  `json:"id"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type webhookReviewCommentPayload struct {
	Action  string `json:"action"`
	Comment struct {
		ID      int64  `json:"id"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	PullRequest struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// prCommentEvent builds the shared pr_comment envelope (GitLab mr_note field
// names). For conversation comments sourceBranch and discussionID are empty.
func prCommentEvent(repo string, number int, title, sourceBranch string, commentID int64, discussionID, noteBody, author, url string, raw []byte) domain.Event {
	return domain.Event{
		SourcePlugin: "github",
		EventType:    "pr_comment",
		PayloadRaw:   json.RawMessage(raw),
		PayloadNorm: map[string]any{
			"project":          repo,
			"mr_iid":           number,
			"mr_title":         title,
			"mr_source_branch": sourceBranch,
			"note_id":          commentID,
			"discussion_id":    discussionID,
			"note_body":        noteBody,
			"author":           author,
			"url":              url,
		},
		Identity: author,
		DedupKey: fmt.Sprintf("note:%s:%d", repo, commentID),
	}
}

// handleIssueComment ingests PR-conversation comments. Non-PR issues, the bot's
// own comments, and non-created actions are dropped.
func (g *GitHubPlugin) handleIssueComment(body []byte) ([]domain.Event, error) {
	var p webhookIssueCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("github: parse issue_comment webhook: %w", err)
	}
	if p.Action != "created" || p.Issue.PullRequest == nil {
		return nil, nil
	}
	if g.botUsername != "" && p.Comment.User.Login == g.botUsername {
		return nil, nil
	}
	return []domain.Event{prCommentEvent(p.Repository.FullName, p.Issue.Number, p.Issue.Title,
		"", p.Comment.ID, "", p.Comment.Body, p.Comment.User.Login, p.Comment.HTMLURL, body)}, nil
}

// handleReviewComment ingests inline review-thread comments. discussion_id is
// the comment id (the reply target); mr_source_branch is the PR head ref.
func (g *GitHubPlugin) handleReviewComment(body []byte) ([]domain.Event, error) {
	var p webhookReviewCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("github: parse pull_request_review_comment webhook: %w", err)
	}
	if p.Action != "created" {
		return nil, nil
	}
	if g.botUsername != "" && p.Comment.User.Login == g.botUsername {
		return nil, nil
	}
	discussionID := strconv.FormatInt(p.Comment.ID, 10)
	return []domain.Event{prCommentEvent(p.Repository.FullName, p.PullRequest.Number, p.PullRequest.Title,
		p.PullRequest.Head.Ref, p.Comment.ID, discussionID, p.Comment.Body, p.Comment.User.Login, p.Comment.HTMLURL, body)}, nil
}
```

(`strconv` is already imported in `github.go`; `events.go` needs it — add `"strconv"` to the `events.go` import block.)

In `HandleWebhook`'s switch (added in Group D), add two cases before `default`:

```go
	case "issue_comment":
		return g.handleIssueComment(body)
	case "pull_request_review_comment":
		return g.handleReviewComment(body)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/plugins/github/`
Expected: PASS (the four new tests + all existing).

- [ ] **Step 6: Commit**

```bash
git add internal/plugins/github/github.go internal/plugins/github/events.go internal/plugins/github/events_test.go
git commit -m "feat(github): pr_comment event (issue + review comments) with bot_username drop"
```

---

### Task 2: GitHub `reply_to_discussion` action

**Files:**
- Modify: `internal/plugins/github/github.go` (`Do`, `Actions`, add `replyToDiscussion`)
- Modify: `internal/plugins/github/github_test.go` (tests)

**Interfaces:**
- Consumes: `firstParam`, `paramToString`, `apiJSON`, `postPRComment`.
- Produces: action `reply_to_discussion` accepting `project`/`repo`, `mr_iid`/`pr_number`, `discussion_id`, `body`. With a non-empty `discussion_id` it replies in the review thread; with an empty one it posts a plain PR comment.

- [ ] **Step 1: Write the failing tests**

Add to `internal/plugins/github/github_test.go`:

```go
func TestReplyToDiscussion_Github_ReviewThread(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	_, err := g.Do(context.Background(), "reply_to_discussion", map[string]any{
		"project": "org/repo", "mr_iid": "12", "discussion_id": "777", "body": "thanks",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/repos/org/repo/pulls/12/comments/777/replies" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["body"] != "thanks" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestReplyToDiscussion_Github_NoThreadFallsBackToIssueComment(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	g := &GitHubPlugin{baseURL: srv.URL, token: "t"}
	_, err := g.Do(context.Background(), "reply_to_discussion", map[string]any{
		"project": "org/repo", "mr_iid": "12", "discussion_id": "", "body": "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/repos/org/repo/issues/12/comments" {
		t.Errorf("path = %q (expected issue-comment fallback)", gotPath)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugins/github/ -run TestReplyToDiscussion`
Expected: FAIL — `github: unknown action "reply_to_discussion"`.

- [ ] **Step 3: Implement and register**

In `internal/plugins/github/github.go`, add to `Do()`:

```go
	case "reply_to_discussion":
		return g.replyToDiscussion(ctx, params)
```

Add to `Actions()`:

```go
		{Name: "reply_to_discussion", Description: "Reply to a PR review thread (falls back to a plain PR comment when there is no thread)"},
```

Add the function:

```go
func (g *GitHubPlugin) replyToDiscussion(ctx context.Context, params map[string]any) (map[string]any, error) {
	repo := firstParam(params, "project", "repo")
	prNumber := firstParam(params, "mr_iid", "pr_number")
	discussionID := paramToString(params["discussion_id"])
	body := paramToString(params["body"])
	if repo == "" || prNumber == "" || body == "" {
		return nil, fmt.Errorf("github reply_to_discussion: project, mr_iid, and body are required")
	}
	if discussionID == "" {
		// Conversation comment (no review thread): post a plain PR comment.
		return g.postPRComment(ctx, params)
	}
	url := fmt.Sprintf("%s/repos/%s/pulls/%s/comments/%s/replies", g.baseURL, repo, prNumber, discussionID)
	result, _, err := g.apiJSON(ctx, http.MethodPost, url, map[string]string{"body": body})
	return result, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/github/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/github/github.go internal/plugins/github/github_test.go
git commit -m "feat(github): add reply_to_discussion action (review-thread reply + comment fallback)"
```

---

### Task 3: Forge-ize the code-feedback skill + assignments

**Files:**
- Modify: `configs/huginn.yaml` (code-feedback skill `prompt`, `config_schema`, `output_actions`; existing assignment; add github assignment)
- Modify: `internal/config/huginn_render_test.go` (add a render test)

**Interfaces:**
- Consumes: `skillPrompt(t, name)`; Forge fields `cli`, `host`, `cloneUser`, `tokenSecret`, `tokenEnv`, `changeCmd`, `changeNoun`, `changeAbbr`, `changeSigil`.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/huginn_render_test.go`:

```go
func TestCodeFeedbackPrompt_RendersBothForges(t *testing.T) {
	tmpl := skillPrompt(t, "code-feedback")
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantHost, wantNoun string }{
		{"gitlab", "gitlab.com", "merge request"},
		{"github", "github.com", "pull request"},
	}
	for _, tc := range cases {
		fp, _ := forge.Profile(tc.forge)
		ctx := prompt.Context{
			Event: map[string]any{
				"mr_iid": 12, "mr_title": "T", "author": "rev",
				"note_body": "fix", "mr_source_branch": "feat/x",
			},
			Assignment: map[string]any{"project": "o/r", "base_branch": "main"},
			Forge:      fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		if !strings.Contains(out, tc.wantHost) || !strings.Contains(out, tc.wantNoun) {
			t.Errorf("forge %s: missing %q/%q", tc.forge, tc.wantHost, tc.wantNoun)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestCodeFeedbackPrompt`
Expected: FAIL — github case lacks `github.com`/`pull request`.

- [ ] **Step 3: Replace the code-feedback skill `prompt:` block**

In `configs/huginn.yaml`, replace the code-feedback `prompt:` with (logic preserved; forge-surface + branch-fallback added):

```yaml
    prompt: |
      A reviewer left feedback on {{.Forge.changeNoun}} {{.Forge.changeSigil}}{{.Event.mr_iid}} ({{.Event.mr_title}}) in
      {{.Assignment.project}}:

        {{.Event.author}} wrote: <untrusted_user_input>{{.Event.note_body}}</untrusted_user_input>

      Only act if this feedback is addressed to you and warrants a response. Skip
      (status "skipped", empty replies) thanks/acknowledgements, bot chatter, or
      anything not directed at the {{.Forge.changeAbbr}} author. Never reply to your own comments.

      Workspace (only if you need code context or will make changes). The change's
      source branch is "{{.Event.mr_source_branch}}"; if that is empty (a
      conversation comment), first resolve it with
      `{{.Forge.cli}} {{.Forge.changeCmd}} view {{.Event.mr_iid}} --repo {{.Assignment.project}}`:
        git clone --depth 50 https://{{.Forge.cloneUser}}:{{secret .Forge.tokenSecret}}@{{.Forge.host}}/{{.Assignment.project}}.git repo
        cd repo && export {{.Forge.tokenEnv}}={{secret .Forge.tokenSecret}}
        git fetch origin <source_branch> && git checkout <source_branch>

      Think it through, then take the MINIMAL appropriate action:
      1. What is the reviewer asking — a code change, a question, or information?
         Is it blocking or a suggestion?
      2. If code changes are needed: review the current diff
         (git diff origin/{{.Assignment.base_branch}}...HEAD), make the targeted
         change (no scope creep, no refactoring unrelated code), commit, and push:
         git push origin <source_branch>. Then reply explaining it.
      3. If already addressed in a prior commit: explain with the commit reference.
      4. If it's a question: answer it concretely. If purely informational:
         acknowledge briefly.
      5. If you genuinely cannot address the feedback, reply with a body that begins
         "I was unable to address this feedback:" and explain why.

      Formatting for the reply: wrap ALL code identifiers in backticks (functions,
      variables, types, fields, file paths, commit hashes); use **bold** for key
      points and bullets/headers for anything over ~3 sentences.

      Do NOT resolve this thread — the reviewer opened it, and only the thread
      starter resolves their own thread. (huginn resolves only threads the bot
      itself started; on reviewer feedback that is never the case here.)

      Report your result as a single JSON object:
        {"summary": "<one line: replied (whether code changed / resolved) or skipped, and why>",
         "replies": [{"body": "<the reply>"}]}    <- empty [] when no reply is warranted
      If you have a submit_result tool, call it with this object as `output`.
      Otherwise end with exactly one fenced ```json code block.
```

- [ ] **Step 4: Add `forge` to `config_schema.properties` and dual-forge `output_actions`**

Add to `config_schema.properties` (keep existing):

```yaml
        forge: { type: string, description: "Which forge this assignment targets", enum: [gitlab, github], default: "gitlab" }
```

Replace `output_actions:` with:

```yaml
    output_actions:
      - plugin: gitlab
        action: reply_to_discussion
      - plugin: github
        action: reply_to_discussion
```

- [ ] **Step 5: Run the render test to verify it passes**

Run: `go test ./internal/config/ -run TestCodeFeedbackPrompt`
Expected: PASS.

- [ ] **Step 6: Add `forge: gitlab` to the existing assignment + add a GitHub assignment**

Add `forge: gitlab` as the first key of the existing code-feedback assignment's `config:`. Then add:

```yaml
  - agent: huginn
    skill: code-feedback
    name: gh
    enabled: false               # opt-in: enable + point at a GitHub repo
    trigger:
      kind: event-subscription
      filter:
        source: github
        event_type: pr_comment
        project: "myorg/myrepo"
    config:
      forge: github
      project: "myorg/myrepo"
      base_branch: "main"
    outputs:
      - plugin: github
        action: reply_to_discussion
        for_each: replies
        params:
          project: "{{.Assignment.project}}"
          mr_iid: "{{.Event.mr_iid}}"
          discussion_id: "{{.Event.discussion_id}}"
          body: "{{.Item.body}}"
```

- [ ] **Step 7: Validate config + full sweep**

Run:
```bash
go run ./cmd/fleet config validate --config configs/huginn.yaml
go build ./... && env -u FLEET_DATABASE_DSN go test ./...
```
Expected: validate exit 0; all tests pass.

- [ ] **Step 8: Commit**

```bash
git add configs/huginn.yaml internal/config/huginn_render_test.go
git commit -m "feat(huginn): dual-forge code-feedback prompt + GitHub pr_comment assignment"
```

---

### Task 4: Forge-ize review-finding-reply (+ `resolveThreadHowto` field)

**Files:**
- Modify: `internal/forge/profile.go` (both maps); `internal/forge/profile_test.go`
- Modify: `configs/huginn.yaml` (review-finding-reply skill `prompt`, `config_schema`, `output_actions`; existing assignment; add github assignment)
- Modify: `internal/config/huginn_render_test.go` (add a render test)

**Interfaces:**
- Produces: `resolveThreadHowto` on every profile.
- Consumes: `skillPrompt(t, name)`; Forge fields incl. `resolveThreadHowto`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/forge/profile_test.go`:

```go
func TestProfile_ResolveThreadHowto(t *testing.T) {
	gl, _ := Profile("gitlab")
	glHowto, _ := gl["resolveThreadHowto"].(string)
	if !strings.Contains(glHowto, "resolved=true") {
		t.Errorf("gitlab resolveThreadHowto = %q", glHowto)
	}
	gh, _ := Profile("github")
	ghHowto, _ := gh["resolveThreadHowto"].(string)
	if !strings.Contains(ghHowto, "gh api graphql") {
		t.Errorf("github resolveThreadHowto = %q", ghHowto)
	}
}
```

Add to `internal/config/huginn_render_test.go`:

```go
func TestReviewFindingReplyPrompt_RendersBothForges(t *testing.T) {
	tmpl := skillPrompt(t, "review-finding-reply")
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantHost, wantResolve string }{
		{"gitlab", "gitlab.com", "resolved=true"},
		{"github", "github.com", "gh api graphql"},
	}
	for _, tc := range cases {
		fp, _ := forge.Profile(tc.forge)
		ctx := prompt.Context{
			Event: map[string]any{
				"mr_iid": 12, "mr_title": "T", "author": "auth",
				"note_body": "pushback", "mr_source_branch": "feat/x", "discussion_id": "777",
			},
			Assignment: map[string]any{"project": "o/r"},
			Forge:      fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		if !strings.Contains(out, tc.wantHost) || !strings.Contains(out, tc.wantResolve) {
			t.Errorf("forge %s: missing %q/%q", tc.forge, tc.wantHost, tc.wantResolve)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/forge/ -run TestProfile_ResolveThreadHowto` and `go test ./internal/config/ -run TestReviewFindingReplyPrompt`
Expected: FAIL — field missing / prompt not forge-ized.

- [ ] **Step 3: Add `resolveThreadHowto` to both profiles**

In `internal/forge/profile.go`, add to `"gitlab"`:

```go
		"resolveThreadHowto": "glab api --method PUT \"projects/<project, url-encoded with / as %2F>/merge_requests/<mr_iid>/discussions/<discussion_id>?resolved=true\"",
```

Add to `"github"`:

```go
		"resolveThreadHowto": "resolve the review thread with gh api graphql: query repository(owner,name).pullRequest(number: <mr_iid>).reviewThreads to find the thread whose comment id is <discussion_id>, take its node id, then run the resolveReviewThread(input:{threadId:\"<node id>\"}) mutation",
```

- [ ] **Step 4: Replace the review-finding-reply skill `prompt:` block**

In `configs/huginn.yaml`, replace the review-finding-reply `prompt:` with:

```yaml
    prompt: |
      The author of {{.Forge.changeNoun}} {{.Forge.changeSigil}}{{.Event.mr_iid}} ({{.Event.mr_title}}) in
      {{.Assignment.project}} replied to one of your review findings:

        {{.Event.author}} wrote: <untrusted_user_input>{{.Event.note_body}}</untrusted_user_input>

      Only act if this is an UNRESOLVED discussion YOU started with a review
      finding, and the latest note is the author's reply (not your own). If the
      thread is already resolved or you did not start it, STOP and report status
      "skipped".

      If you need to re-check the code, clone and inspect (source branch
      "{{.Event.mr_source_branch}}"):
        git clone --depth 50 https://{{.Forge.cloneUser}}:{{secret .Forge.tokenSecret}}@{{.Forge.host}}/{{.Assignment.project}}.git repo
        cd repo && export {{.Forge.tokenEnv}}={{secret .Forge.tokenSecret}}
        git fetch origin {{.Event.mr_source_branch}} && git checkout {{.Event.mr_source_branch}}

      Evaluate the author's reply and respond (discussion only — no code changes):
      - Valid pushback: your finding was wrong or doesn't apply — acknowledge it
        gracefully ("A fair point, mortal").
      - Question/clarification: provide the detail concisely.
      - Acknowledgement that they will fix it: thank them briefly.
      - Partial agreement: address the disagreement constructively, firm but fair.

      Resolve the thread ONLY if you agree the finding was incorrect or no longer
      applies (valid pushback) — do it yourself (substitute the project, <mr_iid>,
      and <discussion_id>):
        {{.Forge.resolveThreadHowto}}
      Do NOT resolve if the finding still stands or you are merely answering a question.

      Report your result as a single JSON object:
        {"summary": "<one line>",
         "status": "replied|resolved|skipped",
         "replies": [{"body": "<markdown reply>"}]}    <- empty [] when status is skipped
      If you have a submit_result tool, call it with this object as `output`.
      Otherwise end with exactly one fenced ```json code block.
```

- [ ] **Step 5: Add `forge` to `config_schema.properties` and dual-forge `output_actions`**

Add to `config_schema.properties` (keep existing):

```yaml
        forge: { type: string, description: "Which forge this assignment targets", enum: [gitlab, github], default: "gitlab" }
```

Replace `output_actions:` with:

```yaml
    output_actions:
      - plugin: gitlab
        action: reply_to_discussion
      - plugin: github
        action: reply_to_discussion
```

- [ ] **Step 6: Run the render tests to verify they pass**

Run: `go test ./internal/forge/ -run TestProfile_ResolveThreadHowto` and `go test ./internal/config/ -run TestReviewFindingReplyPrompt`
Expected: PASS.

- [ ] **Step 7: Add `forge: gitlab` to the existing assignment + add a GitHub assignment**

Add `forge: gitlab` as the first key of the existing review-finding-reply assignment's `config:`. Then add:

```yaml
  - agent: huginn
    skill: review-finding-reply
    name: gh
    enabled: false               # opt-in: shares pr_comment with code-feedback; scope to avoid double-firing
    trigger:
      kind: event-subscription
      filter:
        source: github
        event_type: pr_comment
        project: "myorg/myrepo"
    config:
      forge: github
      project: "myorg/myrepo"
    outputs:
      - plugin: github
        action: reply_to_discussion
        for_each: replies
        params:
          project: "{{.Assignment.project}}"
          mr_iid: "{{.Event.mr_iid}}"
          discussion_id: "{{.Event.discussion_id}}"
          body: "{{.Item.body}}"
```

- [ ] **Step 8: Validate config + full sweep**

Run:
```bash
go run ./cmd/fleet config validate --config configs/huginn.yaml
go build ./... && env -u FLEET_DATABASE_DSN go test ./...
```
Expected: validate exit 0; all tests pass.

- [ ] **Step 9: Commit**

```bash
git add internal/forge/ configs/huginn.yaml internal/config/huginn_render_test.go
git commit -m "feat(huginn): dual-forge review-finding-reply + resolveThreadHowto forge field"
```

---

### Task 5: Group verification sweep + final huginn cleanup

**Files:** possibly `configs/huginn.yaml` (cleanup only).

- [ ] **Step 1: Build, vet, test (env-independent)**

Run:
```bash
go build ./... && go vet ./... && env -u FLEET_DATABASE_DSN go test ./...
```
Expected: all PASS.

- [ ] **Step 2: Deferred cosmetic cleanup across all dual-forge skills**

Across `configs/huginn.yaml`, the dual-forge developer skills still carry GitLab-specific advisory metadata flagged during earlier groups:
- `config_schema.properties.project` descriptions say `"GitLab project path (namespace/repo)"` — change to `"Project path (namespace/repo)"` for the dual-forge skills (issue-implement, issue-batch-implement, ci-fix, code-rebase, code-feedback, review-finding-reply, code-audit).
- `required_tools` lists `[git, glab]` — change to `[git, glab, gh]` for those skills (they may shell out to either CLI).

Make these edits, then re-run:
```bash
go run ./cmd/fleet config validate --config configs/huginn.yaml
env -u FLEET_DATABASE_DSN go test ./internal/config/...
```
Expected: validate exit 0; render tests still pass (these are description/metadata-only changes).

- [ ] **Step 3: Commit the cleanup**

```bash
git add configs/huginn.yaml
git commit -m "chore(huginn): neutralize GitLab-specific descriptions + add gh to required_tools"
```

---

## Self-Review

**Spec coverage:** Group E (code-feedback, review-finding-reply) → Tasks 3, 4; the GitHub `pr_comment` event (issue + review comments, bot drop) the design's §4 lists → Task 1; the GitHub `reply_to_discussion` action → Task 2; in-prompt GraphQL resolve → Task 4's `resolveThreadHowto`. The cross-group cosmetic Minors tracked in the ledger → Task 5. ✅

**Placeholder scan:** complete code/YAML in every step; `<...>` only inside prompt prose (agent-fill) and the howto strings' substitution markers. ✅

**Type consistency:** `pr_comment` PayloadNorm keys (`mr_iid`, `discussion_id`, `mr_source_branch`, `note_body`, …) match what the code-feedback/review-finding-reply prompts and the github `reply_to_discussion` params read; `discussion_id` is the comment id for review comments and empty for conversation comments, and `reply_to_discussion` branches on exactly that. `resolveThreadHowto` defined in Task 4, used in its prompt + asserted in both new tests. `skillPrompt` reused. ✅
