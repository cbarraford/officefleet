# Dual-forge (GitLab + GitHub) developer skills — design

Date: 2026-06-20
Status: approved design, pending implementation plan

## Problem

The developer-role fleet skills in `configs/huginn.yaml` (issue-implement,
issue-batch-implement, ci-fix, code-rebase, code-feedback, code-review,
review-finding-reply, code-audit) only work against GitLab. They hardcode
`glab`, `gitlab.com`, the `gitlab_token` secret, MR/issue terminology, GitLab
API calls, and GitLab-only output actions. We want the same skills to run
against GitHub too.

A `github` plugin already exists but is far thinner than `gitlab`:

| | GitLab plugin | GitHub plugin (today) |
|---|---|---|
| Events | mr_opened/updated/merged/closed, **mr_note**, **pipeline_failed** | pr_opened/updated/merged/closed only |
| Actions | post_mr_comment, post_inline_comment, create_issue, reply_to_discussion, resolve_discussion (stub) | **post_pr_comment only** |
| Field names | project, mr_iid, title, source_branch, target_branch, last_commit_sha, note_body, discussion_id, merge_status, author, url | repo, pr_number, title, source_branch, target_branch, head_sha, author, url |

## Goals

- One shared skill set runs on both forges. The 8 prompts stay single-source.
- GitHub reaches feature parity with GitLab for all 8 skills (eventually).
- Land it as a **vertical slice first** (code-review end-to-end on GitHub),
  then repeat the proven pattern for the rest.

## Non-goals

- No new SCM providers beyond GitHub.
- No reimplementing `glab`/`gh` as plugin actions. The prompt keeps using the
  CLI for reads/discovery and git; only writes that already exist as plugin
  actions move off the CLI.
- No cross-run state (caps/feedback-learning stay in-prompt, as today).

## Locked decisions

1. **Approach:** unify in Go, one skill set (not duplicate-per-provider, not
   `{{if}}`-branches-per-prompt).
2. **Scope:** full parity for all 8 skills, built incrementally.
3. **Field vocabulary — lingua franca:** adopt GitLab's existing `PayloadNorm`
   key names as canonical. The GitLab plugin and existing prompts are left
   untouched; the **GitHub plugin normalizes into GitLab's key names**.
   - Tradeoff accepted: a GitHub PR number carried under the key `mr_iid` is a
     naming smell. Payoff: near-zero churn on the GitLab side and no field
     renames in the existing prompts. A `// ponytail:` comment in the GitHub
     normalizer names the smell and the neutral-rename upgrade path.
4. **Action vocabulary:** both plugins register the same action names; old names
   kept as aliases in each `Do()` switch so nothing existing breaks.
5. **Build order:** vertical slice first; **code-review is slice 1** (retires the
   most architectural risk — Forge profile + shared event vocab + the new
   inline-comment action — without the hardest new event plumbing).

## Architecture

### Forge profile (the linchpin)

A static Go table keyed by forge name, injected into the prompt template
context as `.Forge`. Every GitLab/GitHub difference the prompt touches is
captured here as **data**, so the prompt has no `{{if}}` provider branching.

Add a field to `internal/prompt.Context`:

```go
type Context struct {
    Event      map[string]any
    Agent      map[string]any
    Skill      map[string]any
    Assignment map[string]any
    State      map[string]any
    Forge      map[string]any // <-- new: resolved from assignment `forge` config
    Now        time.Time
    Item       map[string]any
}
```

Profile table (built-in, not user-editable; `host` may be overridden from the
plugin `base_url` for self-hosted/Enterprise):

| field | gitlab | github |
|---|---|---|
| `name` | `gitlab` | `github` |
| `cli` | `glab` | `gh` |
| `host` | `gitlab.com` | `github.com` |
| `cloneUser` | `oauth2` | `x-access-token` |
| `tokenSecret` | `gitlab_token` | `github_token` |
| `tokenEnv` | `GITLAB_TOKEN` | `GITHUB_TOKEN` |
| `changeCmd` | `mr` | `pr` |
| `changeNoun` | `merge request` | `pull request` |
| `changeAbbr` | `MR` | `PR` |
| `changeSigil` | `!` | `#` |
| `headFlag` | `--source-branch` | `--head` |
| `baseFlag` | `--target-branch` | `--base` |
| `bodyFlag` | `--description` | `--body` |
| `removeBranchFlag` | `--remove-source-branch` | `--delete-branch` |
| `ciViewCmd` | `glab ci view` | `gh run view` |
| `ciLogCmd` | `glab ci trace` | `gh run view --log-failed` |
| `ciRetryCmd` | `glab ci retry` | `gh run rerun --failed` |

Resolution: the assignment gains a `forge: gitlab|github` config key (default
`"gitlab"` → back-compat). The render layer (`internal/run`, where `Context` is
assembled) looks up the profile and sets `ctx.Forge`. Unknown forge → load
error. A light load-time validation in `internal/config` checks that `forge` is
consistent with `trigger.filter.source` and `outputs[].plugin` when those are
present (prevents footguns; skip if absent).

### Shared event vocabulary (lingua franca)

The GitHub plugin's `normalizePR` (and the new comment/check normalizers) emit
GitLab's canonical keys **in addition to** the keys it emits today (keep both;
removal is a later cleanup):

| canonical key (GitLab) | GitHub source |
|---|---|
| `project` | `repo` (owner/name) |
| `mr_iid` | PR number |
| `title` | PR title |
| `source_branch` / `target_branch` | head ref / base ref |
| `last_commit_sha` | head SHA |
| `merge_status` | `mergeable_state` (so code-rebase can filter on it) |
| `author` / `url` | login / html_url |
| `note_body` / `discussion_id` / `mr_title` / `mr_source_branch` | comment body / review-thread id / PR title / head ref (comment events) |

Event **types stay provider-specific** (`mr_opened`/`pr_opened`,
`mr_note`/`pr_comment`, `pipeline_failed`/`checks_failed`). Prompts never read
`event_type`; the per-provider assignment filter names the right one.

### Shared action vocabulary

Both plugins register the same action names so a shared skill's `outputs:`
differ only by the literal `plugin:` field (already per-assignment). Old names
are kept as aliases (e.g. gitlab `case "post_mr_comment", "post_change_comment":`)
so existing config keeps working.

Shared set: `post_change_comment`, `post_inline_comment`, `create_issue`,
`reply_to_discussion`, `resolve_discussion`.

`output_actions` on a skill is advisory metadata (config validation does **not**
enforce it against the registry — `internal/config/config.go:416` only checks
`ValidateForEach`). A shared skill lists both providers' actions there for
honesty, but nothing blocks delivery if it doesn't.

### GitHub plugin parity buildout (the fixed cost)

Required across the whole effort (not all in slice 1):

- **Actions:** `post_inline_comment` (PR review-comment API: POST
  `/repos/{o}/{r}/pulls/{n}/comments` with `commit_id`+`path`+`line`+`side`; the
  plugin fetches the PR head SHA itself so the param surface matches GitLab —
  `project,mr_iid,path,line,body`; fall back to an issue comment on a stale
  position, mirroring the GitLab fallback), `create_issue`,
  `reply_to_discussion` (reply to a review comment), `resolve_discussion`
  (GraphQL `resolveReviewThread`).
- **Events:** `checks_failed` (webhook `workflow_run`/`check_suite` failure +
  poll over check runs for bot PRs), `pr_comment` (webhook `issue_comment` +
  `pull_request_review_comment`, with bot-author drop mirroring gitlab
  `bot_username`; + poll). Add `merge_status` to `pr_updated`.

### Prompt rewrite pattern

Per skill: `glab`→`{{.Forge.cli}}`; clone host/user/token→`.Forge.*` +
`secret .Forge.tokenSecret`; `MR`/`!`→`.Forge.changeAbbr`/`.Forge.changeSigil`;
create/CI flags→`.Forge.*Flag` / `.Forge.ci*Cmd`. Writes that already have
plugin actions stay as `outputs:` (provider-neutral). Field references stay
unchanged (lingua franca). GitLab-API-specific in-prompt calls (e.g.
`glab api .../closes_issues`, `discussions?resolved=true`) get a Forge-driven
equivalent or move to a plugin action.

Example (issue-implement create line), one template, both forges:

```
{{.Forge.cli}} {{.Forge.changeCmd}} create {{.Forge.headFlag}} <branch> \
  {{.Forge.baseFlag}} {{.Assignment.base_branch}} --title "<title>" \
  {{.Forge.bodyFlag}} "<body>" {{.Forge.removeBranchFlag}} --label "<labels>"
```

### Config & assignments

Skills are shared; **assignments stay per-provider** (they already differ by
`project`, filter `source`, and `outputs[].plugin`). Each assignment adds
`forge: gitlab|github`. `configs/huginn.yaml` ships a gitlab and a github
assignment block per skill as the worked example.

## Build order

### Slice 1 — code-review end-to-end on GitHub (the proof)

code-review is mostly git + Forge-clone + structured outputs, so CLI divergence
is minimal and it exercises the whole pattern.

1. Forge profile (gitlab+github) + `.Forge` field on `prompt.Context`, populated
   from assignment `forge` config in `internal/run` (default gitlab).
2. GitHub plugin: `normalizePR` also emits canonical keys (`project`, `mr_iid`,
   `last_commit_sha`, `merge_status`); add `post_inline_comment` action; register
   `post_change_comment` as the shared alias of `post_pr_comment`.
3. Rewrite the code-review prompt to use `.Forge.*` for clone + prose; field
   references unchanged.
4. `configs/huginn.yaml`: add a github code-review assignment (`forge: github`,
   filter `source: github`/`event_type: pr_opened`, `outputs[].plugin: github`).
5. Tests (see below).

When slice 1 is green on a real GitHub repo, the architecture is proven.

### Remaining skills (repeat the pattern)

- **No new plugin work** (Forge + prompt + assignment only): issue-implement,
  issue-batch-implement.
- **Needs `create_issue` action + continuous trigger:** code-audit.
- **Needs new events/actions:** ci-fix (`checks_failed` event + ci command
  profile fields), code-rebase (`merge_status` on `pr_updated`), code-feedback &
  review-finding-reply (`pr_comment` event + `reply_to_discussion` /
  `resolve_discussion` actions).

## Testing

- GitHub plugin: table tests for each new/extended event normalization (webhook
  + poll) asserting the **canonical key names**; each new action's request
  shaping via `httptest` (mirroring existing plugin tests), including the
  inline-comment stale-position fallback.
- Forge profile: a test asserting both profiles resolve and that a rewritten
  prompt renders forge-correct commands (`gh pr create --head ...` for github,
  `glab mr create --source-branch ...` for gitlab).
- One render test per rewritten skill against both forges.

## Risks / open questions

- **CLI parity assumptions.** `gh`'s flags/subcommands are assumed stable
  (`gh pr create --head/--base/--body`, `gh run view --log-failed`,
  `gh run rerun --failed`). Verify against the installed `gh` version during
  slice work; adjust profile fields if they differ.
- **GitHub inline-comment positioning.** The reviews/comments API needs a
  `commit_id` and line/side; stale positions are rejected. The fallback to an
  issue comment (carrying `path:line` in the body) keeps findings from being
  lost, matching GitLab behavior.
- **Self-hosted hosts.** `host` should derive from the plugin `base_url` for
  GitLab self-managed / GitHub Enterprise rather than the hardcoded default.
- **Lingua-franca smell** (decision 3) is deliberate, marked in code, and
  reversible to neutral names later.
