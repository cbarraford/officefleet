# SP3b — Plugin Breadth (GitHub, Slack, Discord, Email) — Design Spec

**Status:** Draft for review
**Date:** 2026-06-10
**Author:** brainstormed with Claude Code
**Parent:** `spec.md` §13 (SP3 entry: "more integration plugins"); SP3 design spec §11 deferral
(docs/superpowers/specs/2026-06-10-sp3-event-bus-design.md). The SP3 eventing framework
(WebhookSource/PollSource, dispatcher, `fleet serve`) is complete and is **not modified** by
this spec.

---

## 1. Summary

SP3b adds four integration plugins against the now-stable SP3 framework: **GitHub** (full:
webhook + poll event source AND a PR-comment action — the second complete proof that the
framework generalizes beyond GitLab) and **Slack / Discord / Email** (actions only — the
notification surface agents need for output delivery). Zero framework changes; zero new
module dependencies. Service capabilities that would require new machinery are explicitly
deferred with reasons recorded (§9).

### Decisions locked during brainstorming

1. **Scope: GitHub full + three actions.** GitHub gets events + action (mirrors GitLab).
   Slack, Discord, and Email are action-only in SP3b.
2. **Discord delivers via incoming webhook URLs** stored as secrets — zero bot setup. The
   bot-token REST alternative is deferred.
3. **No new dependencies.** All four plugins are stdlib `net/http` / `net/smtp` REST/SMTP.
4. **Deferred, recorded:** Slack Events API (its `url_verification` challenge needs a
   response *body*, which the platform-owned `WebhookSource` response contract cannot produce
   without a framework extension), Discord Gateway events (WebSocket machinery), IMAP inbound
   email (new dependency + protocol surface), richer GitHub actions.

---

## 2. Goals & non-goals

### Goals
- `internal/plugins/github` — `pr_events` source (webhook + poll, shared normalization) +
  `post_pr_comment` action; GitHub Enterprise supported via `base_url`.
- `internal/plugins/slack` — `send_message` action via `chat.postMessage`.
- `internal/plugins/discord` — `send_message` action via incoming webhook URLs.
- `internal/plugins/email` — `send_email` action via stdlib SMTP.
- All four self-register, expose `ConfigSchema`, and wire into the existing sample config and
  `main.go` blank imports. Compile-time interface assertions where capability interfaces are
  implemented.
- Test suites mirroring the GitLab plugin's depth.

### Non-goals (SP3b)
- Any change to `internal/plugin`, `internal/events`, `internal/server`, the dispatcher, or
  `fleet serve` — the framework is frozen for this spec.
- Slack event sources, Discord Gateway, IMAP inbound email (§9).
- New module dependencies.
- Slack/Discord/Email event sources of any kind (`EventSources()` returns nil for all three).
- Rich message formatting (Slack blocks, Discord embeds, HTML email) — plain text v1.

---

## 3. GitHub plugin (`internal/plugins/github/`)

The structural twin of the GitLab plugin: one file for the plugin core + action
(`github.go`), one for the event source (`events.go`), mirroring `plugins/gitlab`.

### 3.1 Plugin core

- `Name() = "github"`. Config schema: `base_url` (default `https://api.github.com` —
  override for GitHub Enterprise), `poll_interval` (Go duration string, default `60s`),
  `poll_repos` (array of `owner/repo` strings).
- `Init`: resolves secret `github_token` (API auth for action + poll) and secret
  `github_webhook_secret` (may be empty; the webhook handler then rejects all requests —
  same posture as GitLab); parses poll config (invalid `poll_interval` errors Init).
- Compile-time assertions: `var _ plugin.WebhookSource = (*GitHubPlugin)(nil)` and
  `var _ plugin.PollSource = (*GitHubPlugin)(nil)`.

### 3.2 Action: `post_pr_comment(repo, pr_number, body)`

`POST {base_url}/repos/{repo}/issues/{pr_number}/comments` with JSON `{"body": ...}`,
headers `Authorization: Bearer {github_token}`, `Accept: application/vnd.github+json`.
(The issues comments endpoint is GitHub's canonical way to post a plain PR comment.)
All three params required (same `paramToString` coercion as GitLab); non-2xx → error with
bounded body snippet.

### 3.3 Webhook (push)

- Auth: `X-Hub-Signature-256` header must equal `sha256=` + hex HMAC-SHA256 of the **raw
  body** keyed by `github_webhook_secret`, compared constant-time (`hmac.Equal`). Missing
  header, bad signature, or unset secret → `*plugin.AuthError` → 401. Body capped at 1 MiB
  (read fully BEFORE verification — the same bytes are verified and parsed).
- Only `X-GitHub-Event: pull_request` is processed; other event names return zero events
  (202, ignored). Action mapping:
  `opened` → `pr_opened`; `synchronize` → `pr_updated`; `closed` with payload
  `pull_request.merged == true` → `pr_merged`; `closed` with `merged == false` → `pr_closed`;
  anything else (`reopened`, `edited`, `labeled`, …) → ignored (zero events).

### 3.4 Poll

- Per repo in `poll_repos`:
  `GET {base_url}/repos/{owner}/{repo}/pulls?state=open&sort=updated&direction=asc`
  with the bearer token; results filtered client-side to `updated_at > cursor` (GitHub's
  pulls list API has no `updated_after` server-side filter — this is the one mechanical
  difference from GitLab and is recorded here deliberately).
- Cursor discipline identical to GitLab: single RFC3339 cursor = max `updated_at` seen;
  advances **only when every repo polled successfully** and never from a zero time; empty
  cursor uses the `now - poll_interval` window; partial failure returns gathered events +
  unchanged cursor + nil error; total failure errors. No pagination — `sort=updated,asc`
  makes the 30-per-page default self-healing exactly as documented for GitLab.
- Poll-discovered PRs emit `pr_updated`.

### 3.5 Shared normalization (both surfaces)

```
payload_norm = {
  repo: "owner/repo", pr_number: 42, title: "...", action: "opened",
  source_branch: head.ref, target_branch: base.ref, head_sha: head.sha,
  author: user.login, url: html_url,
}
identity  = author login
dedup_key = "pr:{repo}:{pr_number}:{head_sha}"
```

The dedup key changes only when the head SHA changes — webhook/poll overlap collapses at the
event level, and a re-pushed PR re-fires subscribed duties, matching the GitLab convention.

Sample subscription filter: `{source: github, event_type: pr_opened, repo: owner/repo}`.

---

## 4. Slack plugin (`internal/plugins/slack/`)

- `Name() = "slack"`. Action `send_message(channel, text)` — both params required.
- `POST {base_url}/chat.postMessage` with JSON `{"channel": ..., "text": ...}`, header
  `Authorization: Bearer {slack_bot_token}` (secret), `Content-Type: application/json`.
- **Slack's failure contract:** HTTP 200 with `{"ok": false, "error": "channel_not_found"}`.
  The plugin decodes the response and returns an error whenever `ok != true`, including the
  `error` string. Non-2xx HTTP also errors. The `Do` result map returns the decoded response
  (`ts`, `channel`) for output auditing.
- Config: `base_url` (default `https://slack.com/api`) — exists for httptest, doubles as an
  escape hatch for Slack-compatible gateways. `EventSources()` returns nil (deferral §9).

---

## 5. Discord plugin (`internal/plugins/discord/`)

- `Name() = "discord"`. Action `send_message(content, webhook?)` — `content` required.
- Delivery: `POST {webhook_url}` with JSON `{"content": ...}`. The webhook URL is a secret:
  by default the secret named `discord_webhook_url`; if the optional `webhook` param is set,
  it names a **different secret** to resolve instead (multi-channel = one secret per
  channel's incoming webhook). Param values never contain the URL itself — only secret
  names — so URLs stay in the encrypted store and out of run records.
- The plugin retains the `plugin.SecretLookup` from `Init` (the interface passes it there);
  secrets resolve at `Do` time, so a fleet without Discord configured still initializes, and
  a missing/empty secret is a delivery error. Success = any 2xx (Discord returns 204).
- No config keys in v1. `EventSources()` returns nil (Gateway deferral §9).

---

## 6. Email plugin (`internal/plugins/email/`)

- `Name() = "email"`. Action `send_email(to, subject, body)` — all required; `to` is a
  comma-separated address list.
- Transport: stdlib `net/smtp.SendMail` (it negotiates STARTTLS automatically when the
  server advertises it). Auth: `smtp.PlainAuth` when the secret `smtp_password` is non-empty;
  unauthenticated otherwise (local/test relays). Message: `From`/`To`/`Subject` headers +
  CRLF + plain-text body; recipient list parsed from `to`, whitespace-trimmed, empties
  rejected.
- Config: `smtp_host` (required — Init errors without it), `smtp_port` (default `587`),
  `from` (required), `smtp_username` (defaults to `from`).
- Testability: the SMTP dial/send is behind an injected `send` function field (defaults to a
  thin `smtp.SendMail` wrapper); unit tests cover param validation, recipient parsing, and
  message assembly, plus one test against a minimal in-process SMTP fake on 127.0.0.1
  (plaintext, no auth) exercising the real `net/smtp` path.

---

## 7. Wiring, config, and testing

- **Registration:** each plugin self-registers in `init()`; `cmd/fleet/main.go` gains four blank imports (`plugins/github`, `plugins/slack`, `plugins/discord`, `plugins/email`).
- **Sample `configs/fleet.yaml`:** commented example blocks per plugin (github with
  poll_repos + a `pr_opened` event-subscription assignment example; slack/discord/email
  output-binding examples on the existing assignments), all inert (commented) so the default
  sample still validates and seeds unchanged. No `${env:` patterns in comments (the
  expansion-regex lesson from SP2).
- **No config-package changes:** plugin config blocks are free-form maps validated by each
  plugin's `Init` (established SP1–SP3 pattern).
- **Testing:**
  - GitHub: webhook fixture tests (valid signature, bad signature, missing header, unset
    secret, non-PR event ignored, action mapping incl. the `closed`+`merged` split,
    normalization golden), poll tests (parity of dedup key with the webhook for the same
    PR+SHA, client-side `updated_at > cursor` filtering, cursor advance/partial/total
    failure/empty-cursor window — mirroring the GitLab suite), action test via httptest
    (auth header, path, payload, non-2xx error).
  - Slack: httptest — success, `ok:false` decoded into an error, non-2xx, missing params.
  - Discord: httptest — success 204, 4xx error, default vs override secret resolution,
    missing secret error at Do (not Init), missing content.
  - Email: param validation + recipient parsing + message assembly via the seam; one
    127.0.0.1 fake-SMTP test through real `net/smtp`.
  - Integration: one end-to-end assertion that a GitHub webhook fixture dispatches through
    the EXISTING framework to a recorded Run (reusing the SP3 vertical test pattern in
    `package run`), proving framework generality without duplicating the full SP3 suite.

---

## 8. Acceptance criteria

1. A GitHub PR webhook POST with a valid `X-Hub-Signature-256` is normalized, persisted, and
   dispatched to a matching `event-subscription` assignment end-to-end (Run with EventID,
   prompts, outputs); invalid/missing signature or unset secret → 401, nothing persisted.
2. GitHub poll emits envelopes identical to the webhook's for the same PR+SHA (same
   dedup_key); cursor discipline matches §3.4 including client-side filtering; webhook+poll
   overlap stores one event row.
3. `post_pr_comment`, Slack `send_message`, Discord `send_message`, and `send_email` each
   deliver through `outputs.Deliver` with templated params and surface failures as
   delivery errors (Slack `ok:false` included).
4. Discord resolves webhook URLs only from secrets (default + per-call override); the URL
   never appears in params/run records.
5. Email validates config at Init (`smtp_host`/`from` required) and sends through real
   `net/smtp` against the in-process fake.
6. All four plugins register, expose ConfigSchema, and the updated sample fleet.yaml
   validates; `fleet serve` picks up GitHub's PollSource automatically with zero serve-code
   changes.
7. SP1–SP3 tests pass unchanged; no diffs outside `internal/plugins/`, `cmd/fleet/main.go`
   (imports), `configs/fleet.yaml`, and new tests; gofmt/vet clean.

---

## 9. Open questions / deferred

- **Slack Events API** — requires answering `url_verification` with a response body and raw-
  body signing verification; needs a small framework extension (e.g. a typed challenge
  response the server writes). Deferred until Slack-triggered duties are wanted.
- **Discord Gateway events** — WebSocket connection machinery; deferred.
- **IMAP inbound email** — new dependency + protocol surface; deferred.
- **Richer actions** — GitHub reviews/issues, Slack blocks/threads, Discord embeds, HTML
  email; deferred until a duty needs them.
- **Bot-token Discord delivery** — revisit if webhook-URL management becomes unwieldy.
