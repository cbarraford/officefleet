# OfficeFleet

A self-hosted platform for running agentic "employees": configurable AI agents
that perform recurring skills (code review, audits, replies) triggered by cron
or inbound events (GitLab/GitHub/Slack/Discord/email), with an operator web UI.

The data model is two-level: an **Agent** (persona) is bound to a reusable
**Skill** (the "what") by an **Assignment** (the per-agent "where/when/how").
LLM providers are **named backends** in `fleet.yaml` (the `claude` CLI, or an
OpenAI-compatible endpoint, or a multi-model voter). See `spec.md` for the full
design.

## Prerequisites

- Go 1.26+
- PostgreSQL
- The `claude` CLI on `PATH` (for the default subscription backend), plus any
  tools your skills invoke (e.g. `git`, `glab`)
- Node 20+ (only to build the operator SPA)

## Build

```bash
make build      # builds the SPA and the `fleet` binary (with version stamped in)
./fleet version
```

`make test` runs the Go tests and the SPA checks; `make vet` and `make lint`
(needs [golangci-lint](https://golangci-lint.run)) enforce the same checks CI does.

## Configuration & environment

- Config file: `fleet.yaml` (`--config` to override). A documented sample is in
  `configs/fleet.yaml`.
- `FLEET_DATABASE_DSN` — Postgres DSN (or set `database.dsn` / `--db`).
- `FLEET_MASTER_KEY` — AES key for secret encryption at rest. Required to read
  encrypted secrets; without it, `fleet` fails closed on encrypted values.
- `${env:VAR}` references in `fleet.yaml` expand from the environment;
  `${secret:name}` backend keys resolve from the encrypted secret store at run
  time.

## First run

```bash
export FLEET_DATABASE_DSN=postgres://user:pass@localhost:5432/officefleet
export FLEET_MASTER_KEY=$(head -c32 /dev/urandom | base64)

./fleet migrate                              # apply schema migrations + seed from config
./fleet secrets set gitlab_token             # store integration credentials (encrypted)
./fleet users create --password-stdin admin  # create an operator login for the UI
```

## Running

```bash
./fleet run <assignment-id>     # execute one assignment now (--fake to dry-run)
./fleet serve                   # daemon: webhooks, polling, event dispatch, cron, web UI
./fleet schedule                # (deprecated) cron-only daemon
```

Other commands: `fleet agents|skills|assignments|backends|events|runs ...`
(`fleet runs prune --older-than 90d` trims old run history).

## Migrations

Migrations are **forward-only**: `fleet migrate` applies pending `Up` blocks in
order. The `-- +migrate Down` blocks in each file are reference for manual
rollback only — there is no `migrate down` runner.

## License

Internal project; see repository owner.
