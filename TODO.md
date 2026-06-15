# TODO — deferred / later

Active engineering work is tracked as GitHub issues and the session task list.
This file holds work intentionally deferred until later.

## Dogfood Phase 2 — needs real credentials / live services

Phase 1 (local stack) is complete — see `docs/superpowers/2026-06-11-dogfood-findings.md`.
Phase 2 requires real accounts/keys and a human in the loop, so it is held for later:

- Real GitLab: live webhooks/polls, mr-review against an actual MR (inline-comment
  positions on a real diff), code-audit `glab issue list` dedup, mr-feedback replies,
  clone-with-token inside run workspaces.
- Avatar generation against a real image backend (OpenAI key).
- GitHub / Slack / Discord / email plugins against live services.
- Browser walk of the SPA (all surfaces, click-through).

## Deferred by design (spec §15 / plugin breadth) — not yet ticketed

- `fetch` template helper — registered as a stub; needs a side-effects/governance design.
- Codex and Gemini CLI agentic backends (#27): the spec names `claude`/`codex`/`gemini`,
  but only `claude` is implemented. Adding them means a config-validation kind, an
  executor in the factory, and a `loginBinaries` entry each.
- Inbound plugin transports: Slack Events API (challenge-response), Discord Gateway, IMAP.
- Continuous trigger supervision/restart semantics (post-SP3).
- Subscription quota / fail-open / fallback-to-another-backend strategy.
- Per-agent / per-assignment cost budgets.
- Agent-scoped shared memory (cross-duty), beyond per-assignment state.
- Cross-assignment concurrency & rate limiting.
- Login rate limiting; `secure_cookies` defaults to false.
