# Dual-forge Group C: code-rebase — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make the `code-rebase` fleet skill run on both GitLab and GitHub: rebase a stale/conflicted bot change onto its base and force-with-lease push.

**Architecture:** GitLab delivers `merge_status` directly on MR events, and its assignment filters `merge_status: cannot_be_merged`. GitHub computes mergeability (`mergeable_state`) ASYNCHRONOUSLY, so it is frequently `unknown`/absent at webhook time and is NOT returned by the PR-list (poll) endpoint at all. Design choice: (1) emit a best-effort `merge_status` on GitHub PR events mapped from `mergeable_state` (`dirty`→`cannot_be_merged`), so the field exists for filtering when available; (2) the GitHub code-rebase assignment fires on `pr_updated` WITHOUT a `merge_status` filter and relies on the prompt's existing self-discovery (it lists open bot changes and rebases the conflicted/behind one). This matches how the prompt already works on GitLab.

**Tech Stack:** Go (`github.com/cbarraford/office-fleet`), `text/template` prompts, YAML config, table tests.

## Global Constraints

- Module path: `github.com/cbarraford/office-fleet`. Test command: `go test ./...` (render tests `t.Setenv` the DSN; verify with it unset).
- Lingua franca: add `merge_status` to GitHub PR events alongside existing keys; do not remove/rename any key. Map `mergeable_state == "dirty"` → `merge_status == "cannot_be_merged"`; otherwise pass the raw `mergeable_state` through (empty string when absent).
- Back-compat: GitLab `code-rebase` assignment unchanged in behavior; absent `forge` defaults to `gitlab`. The existing GitHub PR-event tests must keep passing — adding a parameter to `normalizePR` requires updating its existing direct caller in `events_test.go`.
- Secrets reachable ONLY via `{{secret .Forge.tokenSecret}}` in the prompt.
- The GitHub code-rebase assignment deliberately OMITS a `merge_status` filter (GitHub's value is async/unreliable at event time); a comment must document this. Default `enabled: false` (the GitLab `on-cannot-merge` assignment is enabled against the `myorg/myrepo` placeholder).
- Work continues on branch `feat/dual-forge-code-review` (PR #51). Do NOT create a new branch.

---

### Task 1: Emit `merge_status` on GitHub PR events

**Files:**
- Modify: `internal/plugins/github/events.go` (`webhookPRPayload`, `HandleWebhook` call, `normalizePR`, the `Poll` call site)
- Modify: `internal/plugins/github/events_test.go` (update the existing `normalizePR` direct call; add a merge_status test)

**Interfaces:**
- Produces: GitHub PR events carry `merge_status` in `PayloadNorm`. `normalizePR` gains a `mergeableState string` parameter inserted immediately BEFORE the final `raw []byte` parameter.

- [ ] **Step 1: Write the failing test (and fix the existing caller)**

In `internal/plugins/github/events_test.go`:

First, update the EXISTING `TestNormalizePR_LinguaFrancaKeys` call to pass the new `mergeableState` argument (empty string) before `raw` — change its `normalizePR(...)` call to:

```go
	ev := normalizePR("pr_opened", "org/repo", 9, "T", "opened",
		"feat", "main", "deadbeef", "carol", "http://x", "", []byte(`{}`))
```

Then add a new test:

```go
func TestNormalizePR_MergeStatus(t *testing.T) {
	dirty := normalizePR("pr_updated", "o/r", 1, "t", "synchronize",
		"f", "main", "sha", "u", "url", "dirty", []byte(`{}`))
	if dirty.PayloadNorm["merge_status"] != "cannot_be_merged" {
		t.Errorf("dirty -> %v, want cannot_be_merged", dirty.PayloadNorm["merge_status"])
	}
	clean := normalizePR("pr_updated", "o/r", 1, "t", "synchronize",
		"f", "main", "sha", "u", "url", "clean", []byte(`{}`))
	if clean.PayloadNorm["merge_status"] != "clean" {
		t.Errorf("clean -> %v, want clean (passthrough)", clean.PayloadNorm["merge_status"])
	}
	absent := normalizePR("pr_updated", "o/r", 1, "t", "synchronize",
		"f", "main", "sha", "u", "url", "", []byte(`{}`))
	if absent.PayloadNorm["merge_status"] != "" {
		t.Errorf("absent -> %v, want empty", absent.PayloadNorm["merge_status"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugins/github/ -run 'TestNormalizePR'`
Expected: FAIL — compile error (too few args to `normalizePR` in the new test) / `merge_status` missing.

- [ ] **Step 3: Add the parameter and mapping in `normalizePR`**

In `internal/plugins/github/events.go`, change the `normalizePR` signature to insert `mergeableState string` before `raw []byte`:

```go
func normalizePR(eventType, repo string, number int, title, action, sourceBranch, targetBranch, sha, author, htmlURL, mergeableState string, raw []byte) domain.Event {
```

At the top of the function body, compute the mapped status:

```go
	mergeStatus := mergeableState
	if mergeableState == "dirty" {
		mergeStatus = "cannot_be_merged"
	}
```

Add to the `PayloadNorm` map literal (keep all existing keys):

```go
		"merge_status": mergeStatus,
```

- [ ] **Step 4: Parse `mergeable_state` from the webhook and pass it through both call sites**

In `webhookPRPayload`, add the field to the `PullRequest` struct (alongside `Merged`):

```go
		MergeableState string `json:"mergeable_state"`
```

In `HandleWebhook`, update the `normalizePR(...)` call to pass `pr.MergeableState` before `body`:

```go
	ev := normalizePR(eventType, payload.Repository.FullName, pr.Number, pr.Title, payload.Action,
		pr.Head.Ref, pr.Base.Ref, pr.Head.SHA, pr.User.Login, pr.HTMLURL, pr.MergeableState, body)
```

In `Poll`, the list endpoint does NOT return mergeable_state — pass `""` before `raw`:

```go
			events = append(events, normalizePR("pr_updated", repo, pr.Number, pr.Title, "synchronize",
				pr.Head.Ref, pr.Base.Ref, pr.Head.SHA, pr.User.Login, pr.HTMLURL, "", raw))
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/plugins/github/`
Expected: PASS (the merge_status test, the updated lingua-franca test, and the existing webhook/poll integration tests).

- [ ] **Step 6: Commit**

```bash
git add internal/plugins/github/events.go internal/plugins/github/events_test.go
git commit -m "feat(github): emit merge_status on PR events (mergeable_state dirty->cannot_be_merged)"
```

---

### Task 2: Forge-ize the code-rebase skill + assignments

**Files:**
- Modify: `configs/huginn.yaml` (code-rebase skill `prompt`, `config_schema`, `output_actions`; existing assignment; add github assignment)
- Modify: `internal/config/huginn_render_test.go` (add a render test)

**Interfaces:**
- Consumes: `skillPrompt(t, name)` helper (already in the test file); Forge fields `cli`, `host`, `cloneUser`, `tokenSecret`, `tokenEnv`, `changeCmd`, `changeAbbr`, `listLimitFlag`.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/huginn_render_test.go`:

```go
func TestCodeRebasePrompt_RendersBothForges(t *testing.T) {
	tmpl := skillPrompt(t, "code-rebase")
	secrets := map[string]string{"gitlab_token": "glt", "github_token": "ght"}
	cases := []struct{ forge, wantHost, wantList string }{
		{"gitlab", "gitlab.com", "glab mr list"},
		{"github", "github.com", "gh pr list"},
	}
	for _, tc := range cases {
		fp, _ := forge.Profile(tc.forge)
		ctx := prompt.Context{
			Assignment: map[string]any{"project": "o/r", "base_branch": "main"},
			Forge:      fp,
		}
		out, err := prompt.Render(tmpl, ctx, secrets)
		if err != nil {
			t.Fatalf("forge %s render: %v", tc.forge, err)
		}
		if !strings.Contains(out, tc.wantHost) || !strings.Contains(out, tc.wantList) {
			t.Errorf("forge %s: missing %q/%q", tc.forge, tc.wantHost, tc.wantList)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestCodeRebasePrompt`
Expected: FAIL — github case lacks `github.com`/`gh pr list`.

- [ ] **Step 3: Replace the code-rebase skill `prompt:` block**

In `configs/huginn.yaml`, replace the code-rebase `prompt:` with:

```yaml
    prompt: |
      Rebase a stale/conflicted bot {{.Forge.changeAbbr}} in {{.Assignment.project}} onto
      origin/{{.Assignment.base_branch}}.

      Workspace:
        git clone https://{{.Forge.cloneUser}}:{{secret .Forge.tokenSecret}}@{{.Forge.host}}/{{.Assignment.project}}.git repo
        cd repo
        export {{.Forge.tokenEnv}}={{secret .Forge.tokenSecret}}
      Find a bot-authored open {{.Forge.changeAbbr}} that has merge conflicts, needs a rebase, OR is
      simply behind origin/{{.Assignment.base_branch}} (its branch has diverged /
      cannot fast-forward merge — huginn rebases this clean-but-behind case too):
        {{.Forge.cli}} {{.Forge.changeCmd}} list --author @me --repo {{.Assignment.project}} {{.Forge.listLimitFlag}} 100
      If none, STOP and report status "skipped". Otherwise use its source branch below.

      Rebase:
      1. git fetch origin {{.Assignment.base_branch}} <source_branch>
      2. git checkout <source_branch>
      3. git rebase origin/{{.Assignment.base_branch}}
      4. If conflicts occur: read each conflicted file to understand BOTH versions,
         resolve keeping the intent of both changes, git add <file>, then
         git rebase --continue. Repeat until the rebase completes.
      5. Push: git push origin <source_branch> --force-with-lease

      If you cannot resolve the conflicts: git rebase --abort, report status
      "failed", and post this comment verbatim on the {{.Forge.changeAbbr}} (use {{.Forge.cli}}):
        "The branches have grown tangled like the roots of Yggdrasil, and I could not unravel them. Manual intervention is required to resolve these conflicts. May your merge be swift once the path is cleared."

      Report your result as a single JSON object:
        {"summary": "<one line>", "status": "rebased|failed|skipped",
         "mr_iid": <int or null>}
      If you have a submit_result tool, call it with this object as `output`.
      Otherwise end with exactly one fenced ```json code block.
```

- [ ] **Step 4: Add `forge` to `config_schema.properties` and a github `output_actions` entry**

Add to `config_schema.properties` (keep existing):

```yaml
        forge: { type: string, description: "Which forge this assignment targets", enum: [gitlab, github], default: "gitlab" }
```

Replace the code-rebase `output_actions:` block with both forges, using the shared action name:

```yaml
    output_actions:
      - plugin: gitlab
        action: post_change_comment
      - plugin: github
        action: post_change_comment
```

- [ ] **Step 5: Run the render test to verify it passes**

Run: `go test ./internal/config/ -run TestCodeRebasePrompt`
Expected: PASS.

- [ ] **Step 6: Add `forge: gitlab` to the existing assignment + add a GitHub assignment**

Add `forge: gitlab` as the first key of the existing `on-cannot-merge` assignment's `config:`. Then add a parallel GitHub assignment after it:

```yaml
  - agent: huginn
    skill: code-rebase
    name: gh-on-update
    enabled: false               # opt-in: enable + point at a GitHub repo
    trigger:
      kind: event-subscription
      filter:
        source: github
        event_type: pr_updated
        project: "myorg/myrepo"
        # NOTE: no merge_status filter — GitHub computes mergeability asynchronously,
        # so mergeable_state is often "unknown"/absent at webhook time (and the PR-list
        # poll endpoint omits it). This fires on every PR update; the prompt self-discovers
        # whether a rebase is actually needed and skips otherwise.
    config:
      forge: github
      project: "myorg/myrepo"
      base_branch: "main"
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
git commit -m "feat(huginn): dual-forge code-rebase prompt + GitHub assignment"
```

---

### Task 3: Group verification sweep

**Files:** none.

- [ ] **Step 1: Build, vet, test (env-independent)**

Run:
```bash
go build ./... && go vet ./... && env -u FLEET_DATABASE_DSN go test ./...
```
Expected: all PASS.

- [ ] **Step 2: Confirm code-rebase renders for both forges**

Run: `env -u FLEET_DATABASE_DSN go test ./internal/config/ -run TestCodeRebasePrompt -v`
Expected: PASS for both subcases.

---

## Self-Review

**Spec coverage:** Group C (code-rebase) of the design's follow-on list → Task 2; the `merge_status` on GitHub `pr_updated` the design calls for → Task 1. ✅

**Placeholder scan:** complete code/YAML in every step; `<...>` only inside prompt prose (agent-fill). ✅

**Type consistency:** the new `normalizePR` `mergeableState string` parameter (inserted before `raw`) is applied consistently at the function definition, the webhook caller, the poll caller, and BOTH `events_test.go` call sites (the updated lingua-franca test + the new merge_status test). `skillPrompt` reused, not redefined. ✅
