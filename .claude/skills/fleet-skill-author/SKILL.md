---
name: fleet-skill-author
description: >-
  Author, edit, or review an OfficeFleet fleet skill — a reusable unit of agent work
  defined as an entry under `skills:` in configs/fleet.yaml. Getting one right needs
  non-obvious project knowledge that is easy to get wrong by hand: the exact YAML
  schema, the Go-template prompt context (`{{.Event}}`/`{{.Assignment}}`/`{{.Agent}}`
  and the `secret` helper), the result-JSON output contract, the validation rules, and
  the seed/dry-run lifecycle — so consult this skill instead of editing the YAML
  directly. Use it whenever the user wants to add, write, change, or review a fleet
  skill, agent task, or scheduled/automated job for an OfficeFleet agent — for example
  "add a skill to fleet.yaml that triages issues", "make a skill so dev-1 summarizes
  merge requests", "edit the code-audit skill's prompt", "add an output_action to
  code-review", "add a config_schema to my skill", or "a skill that runs on a cron to
  file issues" — even when the user does not say the word "skill". This is about
  OfficeFleet's fleet.yaml skills, not about building a Claude Code/Anthropic SKILL.md
  (use skill-creator for that).
---

# Authoring an OfficeFleet skill

A **skill** is the reusable "what" an agent does (Agent = who, Skill = what,
Assignment = where/when/how). Skills are defined in `configs/fleet.yaml` under
`skills:`, validated, then **seeded into Postgres, which is the source of truth**
at runtime. A skill does nothing until an Assignment binds it to an Agent.

Source of truth for the schema: `internal/config/config.go` (`SkillConfig`,
`Validate`) and `internal/domain/types.go` (`Skill`). Working examples:
`configs/fleet.yaml` (`code-review`, `code-audit`, `code-feedback`). Read those before
writing — copy the closest one rather than starting blank.

## Schema (the `skills:` list entry)

```yaml
- name: triage-issue          # required, unique, immutable identity (the upsert key)
  role: developer             # required, category tag for UI grouping — NOT a persona
  description: One line on what this skill does.   # required
  trigger_kinds:              # required, subset of: manual | cron | event-subscription | continuous
    - manual
    - event-subscription
  required_tools: [git, glab] # CLI tools that must be on PATH at run time (informational + checked)
  prompt: |                   # required, Go text/template — see context + contract below
    ...
  output_actions:             # optional, where results may flow (plugin+action pairs)
    - plugin: gitlab
      action: create_issue
  config_schema:              # optional, JSON Schema for per-assignment config values
    type: object
    properties:
      project: { type: string }
    required: [project]
  backend:                    # optional, override; precedence: assignment > skill > agent default
    name: claude-default
```

Only `name`, `role`, `description`, `trigger_kinds`, `prompt` are needed for a
minimal skill. Omit the rest unless you have a reason.

## Prompt template

The prompt is a Go `text/template` rendered at run time (`internal/prompt`).
Available data:

- `{{.Event.<k>}}` — normalized event payload (event-subscription/webhook runs). Keys are plugin-specific (e.g. `.Event.mr_iid`, `.Event.note_body`).
- `{{.Assignment.<k>}}` — per-assignment `config:` values (e.g. `.Assignment.project`). These are what `config_schema` describes.
- `{{.Agent.name}}`, `{{.Agent.role}}` — the executing agent.
- `{{.Skill.<k>}}`, `{{.State.<k>}}`, `{{.Now}}`, `{{.Item.<k>}}` (`.Item` only during `for_each` output delivery).

Helpers: `{{secret "gitlab_token"}}` (only way to reach secrets — never via context),
`{{date}}`, `{{default "x" .Foo}}`, `{{truncate .S 80}}`, `{{json .Foo}}`.

Every `{{.X}}` you reference must exist in the render context or the run errors,
and `internal/config/sample_test.go` flags any `<no value>` — so don't reference
fields the trigger won't provide.

## The result contract (how output gets used)

End the prompt by telling the model to emit one JSON object, and mirror its keys
in the assignment's `outputs`. Standard phrasing (copy from `code-review`):

> Report your result as a single JSON object: `{...}`. If you have a
> `submit_result` tool, call it with this object as the `output` parameter (and a
> one-line verdict as `summary`). Otherwise end your final message with exactly
> one fenced ```json code block containing it.

An array key in that JSON can fan out: an assignment `output` with
`for_each: comments` delivers the action once per element, each as `{{.Item.*}}`.
`output_actions` on the skill just declares which plugin/action pairs are legal;
the assignment's `outputs` does the actual wiring.

## Validation rules (what `Validate` enforces)

- `name` non-empty and unique across skills.
- `backend.name`, if set, must be a defined backend; no `model`/`effort` override on a voter backend.
- A skill used by an `event-subscription` assignment **must** list `event-subscription` in `trigger_kinds` (checked at the assignment).
- `trigger_kinds` values are enforced to the four-item set by the REST API on create/patch (YAML load is lenient — so validate the values yourself).

## Lifecycle: author → validate → seed → verify

```bash
# 1. Edit configs/fleet.yaml (add/modify the skills: entry)

# 2. Validate + render-test in one step, no DB/env needed. This Loads, Validates,
#    and renders every skill prompt against a synthetic context — catching
#    structure errors, bad references, template typos, and missing fields. Use
#    this as your primary check:
go test ./internal/config

# 2b. (optional) The CLI validator. Needs the config path AND a DSN env, because
#     fleet.yaml interpolates ${env:FLEET_DATABASE_DSN} at load:
FLEET_DATABASE_DSN=postgres://x go run ./cmd/fleet --config configs/fleet.yaml config validate

# 3. Seed into the DB (needs a real Postgres). `migrate` seeds only when empty;
#    to push changes to existing rows, use --force (overwrites by name,
#    including any edits made in the web UI):
go run ./cmd/fleet seed --force

# 4. Confirm it landed
go run ./cmd/fleet skills list

# 5. Dry-run via an assignment without spending tokens (--fake), or for real:
go run ./cmd/fleet run --agent <agent> --skill <skill> --fake
go run ./cmd/fleet run --agent <agent> --skill <skill> --param key=value
```

A skill can't run alone — it needs an Assignment (`assignments:` in fleet.yaml)
binding it to an agent with a trigger and `outputs`. If the user wants the skill
to actually fire, add or point them to that assignment too.

## Gotchas

- **Name is identity.** Seeding upserts by `name`; renaming creates a second skill rather than renaming the first.
- **DB is source of truth, YAML is the seed.** A bare `seed`/`migrate` won't overwrite existing rows — `--force` does (and warns it clobbers UI edits).
- **`role` is a tag, not a persona.** The operative persona is the Agent's `system_prompt`, composed ahead of the skill prompt at run time.
- **Don't leak secrets.** Reach them only through `{{secret "name"}}`; rendered prompts are stored in run records.
