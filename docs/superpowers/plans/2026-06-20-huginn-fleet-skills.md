# Huginn → OfficeFleet Skills — Reference & Plan

> **For agentic workers:** the runnable artifact is `configs/huginn.yaml`. This doc
> is the catalog: what each huginn-agent behavior is, how it maps to a fleet
> skill, which curated rules live in the prompt vs. config, and where the fleet
> model can't reach huginn's behavior (capability gaps). Refer back here before
> editing a skill.

**Goal:** Reproduce the huginn-agent bot's AI behaviors as OfficeFleet fleet
skills using only prompt + config — no new Go code in office-fleet.

**Source bot:** `/Users/cbarraford/workshop/huginn-agent` (Python; GitLab + Claude Code).

**Approach:** Each huginn AI prompt becomes one fleet skill in `configs/huginn.yaml`,
bound to a single `huginn` agent. The agent does all git/glab work *inside the
prompt* (clone, push, `glab mr create`, `glab ci retry`, merge) exactly like the
existing `code-review` skill clones the repo itself — so push/merge/retry need no
new output_actions. Output_actions are only for structured post-back
(comments/issues/discussion replies). huginn's curated decision rules (priority
sorting, capacity caps, CI infra-vs-code classification, review false-positive
list, gap analysis, self-verification, auto-resolve) are ported verbatim-in-intent
into the prompt; tunable limits become `config_schema` fields.

---

## Global Constraints

- **No new Go code.** Behavior lives in `configs/huginn.yaml` (prompt + config_schema) only.
- **GitLab plugin emits only MR + MR-note events** (`mr_opened/updated/merged/closed`, `mr_note`). No issue, pipeline, or push webhooks. (`internal/plugins/gitlab/events.go`)
- **Available output_actions:** `post_mr_comment`, `post_inline_comment`, `create_issue`, `reply_to_discussion`, `resolve_discussion`. (`internal/plugins/gitlab/gitlab.go:48-124`)
- **MR event keys:** `project, mr_iid, title, action, source_branch, target_branch, last_commit_sha, author, url`.
- **MR-note event keys:** `project, mr_iid, mr_title, mr_source_branch, note_id, discussion_id, note_body, author, url`.
- **No cross-run state** exposed to skills (no per-MR counters, no calibration/learning store).
- **Secrets only via** `{{secret "gitlab_token"}}`. Untrusted input wrapped in `<untrusted_user_input>` tags; the agent persona forbids obeying it.

---

## Skill Catalog

huginn has **11 distinct AI-prompt behaviors**. 8 become fleet skills; 3 are
folded or dropped. (`~15` pure-automation behaviors — merge checks,
notifications, webhook routing, health checks — are harness/plumbing, not skills.)

| # | Skill (`configs/huginn.yaml`) | huginn source | Trigger here | Trigger in huginn | Output |
|---|---|---|---|---|---|
| 1 | `issue-implement` | `claude_runner.py:1935` `implement` | cron, manual | webhook: issue assigned | clone→push→`glab mr create` |
| 2 | `issue-batch-implement` | `claude_runner.py:2018` `batch_implement` | cron, manual | poll: `batch` label | one MR for N issues |
| 3 | `ci-fix` | `claude_runner.py:2145` `fix_ci` | cron, manual | webhook: pipeline failed | retry / fix & push + comment |
| 4 | `code-rebase` | `claude_runner.py:2452` `rebase` | cron, manual | poll: conflicts/needs_rebase | force-with-lease push |
| 5 | `code-feedback` | `claude_runner.py:2308` `address_feedback` | event (mr_note), manual | webhook: MR note | fix & push + reply/resolve |
| 6 | `code-review` | `claude_runner.py:2485` `code_review` (+ `verify_findings:3440`, `challenge_findings:3511`) | event (mr_opened/updated), manual | poll: reviewer assigned | inline comments + verdict |
| 7 | `review-finding-reply` | `claude_runner.py:3365` `finding_reply` | event (mr_note), manual | poll: author replied | reply + auto-resolve |
| 8 | `code-audit` | `code_audit/agents/prompts/*.md` (14 categories) | cron, manual | CLI / cron | issue per finding |

### Folded / dropped (with reason)

- **`verify_findings` + `challenge_findings`** → folded into `code-review`'s
  *Self-verification (MANDATORY)* block (re-read file → confirm in new code →
  not handled elsewhere → devil's-advocate counter-evidence → senior-engineer
  test). huginn already runs this inline in `code_review`; the separate Python
  passes were extra rigor we collapse into one prompt. Split back out only if a
  single pass measurably misses findings.
- **`batch_implement`** kept as its own skill (#2), not a flag on #1 — different
  prompt, branch naming, and MR title; a flag would branch the mega-prompt.
- **`feedback_analyze` (feedback learning)** → **dropped.** Needs durable
  cross-run pattern storage (huginn caps at 500 patterns in Postgres) with no
  fleet equivalent. The hints it produced fed `implement`/`fix_ci`/`code_review`;
  without the store there is nothing to inject. Revisit if office-fleet grows
  per-project skill memory.

---

## Curated behavior → where it lives

The point of the exercise: keep huginn's *exact* decisions, but in prompt/config,
not Go. Mapping of every curated rule:

### issue-implement
- Priority sort (`priority::0` highest → unlabelled last) — **prompt**.
- Capacity gate (skip run if open-MR count ≥ cap) — **prompt** check via `glab mr list --author @me`; cap is **config** `max_open_mrs`.
- One issue per run (highest priority, no existing MR) — **prompt**; throughput = **cron frequency** (huginn's `max_new_issues_per_run` becomes "how often the cron fires").
- Branch `huginn/<iid>-issue-<rand5>`, MR title = issue title, description `Resolve #<iid>` + `## Summary` — **prompt** (verbatim format).
- Labels/milestone — **config** `mr_labels`.

### issue-batch-implement
- Batch = issues with label (`batch`) — **config** `batch_label`; promote-single-to-normal and `max_batch_issues` cap — **prompt** + **config**.
- Branch `huginn/<minIID>-batch-<rand5>`, title `Batch: resolve issues #..` — **prompt**.

### ci-fix
- Infra-vs-code classification + the exact infra pattern list (timeout, conn refused, image not found, no runners, 429, OOM, flaky) — **prompt**.
- Infra → `glab ci retry`; code → fix & push — **prompt**.
- Regression/fixture failures never hand-edited; use regen command — **prompt** + **config** `regression_command`.
- Attempt cap (huginn: 5, persisted per-MR) — **approximated in prompt** by counting prior `"ci-fix attempt"` bot comments; cap is **config** `max_ci_fix_attempts`. *(see gap #2)*

### code-rebase
- Fetch → rebase → resolve keeping both intents → `--force-with-lease`; abort+comment on failure — **prompt** (verbatim).

### code-feedback
- Act only on feedback directed at the author; skip thanks/bot chatter; never reply to self — **prompt** (+ plugin drops the bot's own notes at ingest, `events.go:135`).
- Minimal targeted change, no scope creep; reply formatting (backtick all identifiers, bold/bullets) — **prompt** (verbatim).
- Resolve on full resolution — **output_action** `resolve_discussion`.

### code-review
- Skip self-authored MRs; optional review label gate — **prompt** + **config** `review_label`.
- False-positive DO-NOT-flag list, gap analysis (P0/P1 only), self-verification + devil's advocate — **prompt** (verbatim).
- Severity `critical|major|minor|suggestion`, category `bug|security|performance|style|documentation`, confidence 0–1 — **prompt**.
- Confidence floor for minor/suggestion — **config** `min_confidence` (huginn default 0.5).
- Verdict resolution (CHANGES_REQUESTED > APPROVED_WITH_COMMENTS > APPROVED) — **prompt**.
- Project path guidance — **config** `path_instructions`.
- *Dropped:* 3 rotating review-perspective variants, per-(category,severity) calibration store, diff chunking ≥100k (agent reads files directly instead). *(see gap #3)*

### review-finding-reply
- Reply taxonomy (valid pushback / question / ack / partial) — **prompt** (verbatim).
- Auto-resolve only on valid pushback (`resolve=true`) — **prompt** + **output_action** `resolve_discussion`.
- Read-only (no code changes) — **prompt**.

### code-audit
- Category set ported from huginn's 14 `agents/prompts/*.md` — **config** enum + **prompt** one-line focus per category.
- Dedup against open `code-audit` issues; verify + devil's-advocate before filing — **prompt**.
- *Partial fidelity:* the full per-category `.md` prompt bodies are not inlined; paste a category's `.md` body into its focus line for exact parity.

---

## Capability Gaps (recorded, not lost)

1. **No issue / pipeline / push events.** huginn fires `implement` on issue
   assignment, `fix_ci`/`rebase` on pipeline-failure webhooks. office-fleet has
   neither → skills 1–4 and 8 run on **cron** (scan via `glab`) or **manual**.
   Latency = cron interval, not real-time. *Closing it* would mean adding issue
   + pipeline ingestion to the GitLab plugin (`events.go`) — Go work, out of scope.
2. **No cross-run state.** CI fix attempt cap is approximated by counting prior
   bot comments (works, but fragile if comments are edited/deleted). True
   per-MR/per-issue counters and the feedback-learning store have no home.
3. **Review extras dropped.** Perspective-variant rotation, confidence
   calibration, and explicit diff chunking are huginn Python orchestration with
   no prompt/config equivalent; the single self-verifying prompt is the
   substitute.
4. **Routing overlap on `mr_note`.** Both `code-feedback` (feedback on *our* MR)
   and `review-finding-reply` (author replying to *our finding* on *their* MR)
   trigger on `mr_note`. Each prompt self-gates ("only act if…"), but if both are
   assigned to the same agent/project a note fires both runs; one will skip.
   Acceptable; tighten with separate agents or label scoping if noisy.

---

## Design Decisions

- **Norse-raven voice → agent `system_prompt`, once.** huginn repeats the voice
  block verbatim across `fix_ci`/`address_feedback`/`code_review`/`finding_reply`.
  The agent persona is composed ahead of every skill prompt (`fleet-skill-author`),
  so it lives in the `huginn` agent once. Delete that paragraph for behavior-only.
- **Untrusted-input guard → agent persona,** since every skill consumes untrusted
  diffs/logs/comments.
- **One `huginn` agent** binds all 8 skills (mirrors the single bot identity).

---

## Tasks

The artifact already exists (`configs/huginn.yaml`, validated). Remaining work is
wiring assignments and seeding — do these only when you actually want it running.

### Task 1: Confirm config validates
- [ ] `FLEET_DATABASE_DSN=postgres://x go run ./cmd/fleet --config configs/huginn.yaml config validate` → `OK` (done).

### Task 2: Add assignments (one per skill you want live)
- [ ] Append an `assignments:` block to `configs/huginn.yaml` binding `agent: huginn` to each skill with its trigger + `config` values + `outputs` wiring. For event skills set `event_type` (`mr_opened`/`mr_note`); for cron skills set the schedule. Copy the shape from `configs/fleet.yaml:211` (`on-open`). For `comments`/`issues`/`replies` array keys use `for_each` in the assignment `outputs`.
- [ ] Re-run Task 1's validate.

### Task 3: Seed + smoke test (needs Postgres)
- [ ] `go run ./cmd/fleet --config configs/huginn.yaml seed --force`
- [ ] `go run ./cmd/fleet skills list` → the 8 skills appear.
- [ ] `go run ./cmd/fleet run --agent huginn --skill code-review --fake` (dry run, no tokens).

---

## Self-Review

- **Coverage:** all 11 huginn AI behaviors accounted for — 8 skills + 2 folded
  (verify/challenge) + 1 dropped (feedback-learning, with reason).
- **Event keys:** every `{{.Event.*}}` referenced exists in the MR/MR-note
  normalized payloads (verified against `events.go`).
- **Output actions:** every `output_actions` entry is one of the 5 the GitLab
  plugin implements.
- **Gaps:** the 4 places the fleet model can't match huginn are named, not hidden.
