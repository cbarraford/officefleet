# OfficeFleet

OfficeFleet runs configured agent duties against manual, scheduled, and webhook-driven events. It stores configuration and run history in Postgres, invokes configured LLM backends, and delivers outputs through plugins such as GitLab, GitHub, Slack, Discord, and email.

## Requirements

- Go 1.26.1 or newer
- Node.js 22 or newer for the embedded web UI
- PostgreSQL with the `uuid-ossp` extension available
- The CLI tools required by your duties, such as `claude`, `git`, or `glab`

## Configuration

Start from [`configs/fleet.yaml`](configs/fleet.yaml). Runtime config is read from `fleet.yaml` by default; root-level `fleet.yaml`, `fleet.*.yaml`, and `.env*` files are ignored so local DSNs and secrets are not committed.

Common environment variables:

- `FLEET_DATABASE_DSN`: Postgres DSN used when `--db` and `database.dsn` are unset.
- `FLEET_MASTER_KEY`: base64-encoded 32-byte key used to encrypt and decrypt stored secrets.

## Setup

```bash
make build
./fleet --config configs/fleet.yaml config validate
./fleet --config configs/fleet.yaml migrate
```

Secrets are write-only through the CLI:

```bash
printf '%s' "$GITLAB_TOKEN" | ./fleet --config fleet.yaml secrets set gitlab_token
```

Run an assignment manually:

```bash
./fleet --config fleet.yaml run --id <assignment-uuid> --param mr_iid=42
```

Run the daemon:

```bash
./fleet --config fleet.yaml serve
```

## Development

```bash
make test
make lint
make vet
```

`make build` embeds the web app and writes the local binary to `./fleet`, which is ignored. Build metadata is injected with `-ldflags`; check it with:

```bash
./fleet version
```

## Migrations

Migrations are forward-only. The runner applies only the `-- +migrate Up` block from each file and records applied versions in `schema_migrations`.
