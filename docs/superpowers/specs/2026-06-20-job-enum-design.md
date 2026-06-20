# Job enum on agents & skills

**Date:** 2026-06-20
**Status:** Approved, ready for implementation plan

## Problem

Every agent should have a *job* (developer, data-scientist, marketer, lawyer,
…) and every skill should belong to a job. When binding a skill to an agent,
only skills matching the agent's job should be offered. Today both `Agent` and
`Skill` carry a free-form `role` string with no constraint, and the assignment
UI lists *all* skills regardless of fit.

## Decision summary

| Decision | Choice |
|----------|--------|
| Concept mapping | Reuse the existing `role` field; **do not rename**. Change its *type* to a new `Job` enum. JSON tag and DB column stay `role`. UI labels it "Job". |
| Enum values | `developer` only as a real job, for now. `JobUnknown = "unknown"` is the zero/default sentinel — not selectable, excluded from `Jobs`/`Valid()`. Adding a real job = one line in `domain.Jobs` + one line in the frontend `JOBS` const. |
| Match enforcement | UI filters the skill picker **and** the server rejects mismatches (config validation + API). |
| DB constraint | None. Column stays `TEXT NOT NULL`; the Go enum is the single source of truth. No migration. |

`User.Role` (admin/viewer) is a separate auth concept and is **out of scope** —
do not touch it.

## Design

### 1. The enum — `internal/domain/job.go` (new)

```go
package domain

// Job is the closed set of jobs an agent performs and a skill belongs to.
// JobUnknown is the zero/default sentinel — an unset agent/skill reads as
// "unknown". It is NOT a real, assignable job (excluded from Jobs and Valid).
type Job string

const (
	JobUnknown   Job = "unknown"
	JobDeveloper Job = "developer"
)

// Jobs is the canonical list of real, assignable jobs (JobUnknown excluded).
// Add a value here (and to the frontend JOBS const) to introduce a new job.
var Jobs = []Job{JobDeveloper}

// Valid reports whether j is a real, assignable job. The unknown sentinel and
// the empty zero value are both invalid.
func (j Job) Valid() bool {
	for _, v := range Jobs {
		if j == v {
			return true
		}
	}
	return false
}

// String maps the empty zero value to the unknown sentinel so an unset job
// surfaces as "unknown" wherever a Job is printed or rendered.
func (j Job) String() string {
	if j == "" {
		return string(JobUnknown)
	}
	return string(j)
}
```

The zero/default value of a `Job` is the empty string, which `String()` renders
as `"unknown"`. `JobUnknown` and `""` both fail `Valid()`, so they are rejected
on agent/skill create and never appear in the assignment-job dropdowns — the
"every agent must have a real job" rule is preserved.

`domain.Agent.Role` and `domain.Skill.Role` change type `string → Job`. The
`db:"role"` and `json:"role"` tags are unchanged, so neither the DB schema nor
the JSON API contract changes. pgx v5 scans/encodes named string types via its
reflection fallback, so the repo layer needs no change.

### 2. Config — `internal/config/config.go`

- `AgentConfig.Role` and `SkillConfig.Role` change type `string → domain.Job`
  (yaml key stays `role`).
- `Validate` additions:
  - For every agent: error if `!a.Role.Valid()`. An empty job fails this check,
    which satisfies "an agent is required to have a job."
  - For every skill: error if `!d.Role.Valid()`.
  - For every assignment where both agent and skill resolve: error if
    `agentByName[a.Agent].Role != skillByName[a.Skill].Role`. (`Validate`
    already builds `agentByName`/`skillByName`.)

Error messages list the valid jobs, e.g.
`agent "dev-1": invalid job "" (must be one of: developer)`.

### 3. Seeding — `internal/seed/seed.go`

`agent.Role = ac.Role` and `skill.Role = dc.Role` — both sides are now
`domain.Job`, so the assignments compile unchanged.

### 4. API — `internal/api/entity_handlers.go`

- Agent create/patch: when `role` is provided, reject (400) if
  `!domain.Job(*body.Role).Valid()`; on create, `role` is required.
- Skill create/patch: same validation; required on create.
- Assignment create/patch: after resolving the agent and skill (already fetched
  to validate existence), reject (400) if `agent.Role != skill.Role`.

The request/response JSON field stays `role` (a string); validation is the only
addition.

### 5. Avatars & prompt context (type-cast sites)

- `internal/avatar/service.go`: `s.gen.Generate(genCtx, name, string(agent.Role))`
  — `Generate` keeps its `role string` signature; the avatar template `{{.Role}}`
  is unchanged.
- `internal/run/pipeline.go`: the prompt context maps use
  `"role": string(req.Agent.Role)` and `"role": string(req.Skill.Role)` so the
  template context stays plain strings. `{{.Agent.role}}` / `{{.Skill.role}}`
  render unchanged.
- `cmd/fleet/main.go`: `%s` formatting works on a named string type; no change.

### 6. Frontend

- `web/src/api/types.ts`: add
  `export type Job = 'developer';` and
  `export const JOBS: Job[] = ['developer']; // keep in sync with domain.Jobs`.
  `Agent.role` / `Skill.role` typed as `Job`.
- `web/src/pages/Agents.tsx`: replace the free-text role input with a
  `<select>` over `JOBS`, relabeled "Job". Required.
- `web/src/pages/Skills.tsx`: same — `<select>` over `JOBS`, label "Job".
  Required.
- `web/src/pages/AgentDetail.tsx` (`AssignmentModal`): filter the skill dropdown
  to `skills.filter((d) => d.role === agent.role)`. When the filtered list is
  empty, show a hint ("No skills for this job yet") instead of an empty select.

### 7. Tests

- Update fixtures that use non-`developer` job strings:
  `internal/api/entity_handlers_test.go` currently posts `"role": "analyst"` and
  patches to `"engineer"` — change to `developer`.
- Add coverage:
  - config `Validate` rejects an agent/skill with an invalid/empty job.
  - config `Validate` rejects an assignment whose skill job ≠ agent job.
  - API rejects (400) an invalid job on agent/skill create.
  - API rejects (400) an assignment with a job mismatch.

## Deliberate simplifications (ponytail)

- **No DB CHECK constraint.** A `CHECK (role IN (...))` would force a migration
  every time a job is added, for no safety the Go enum doesn't already give.
  Add one only if rows start being written outside the app.
- **No `GET /api/v1/jobs` endpoint.** The frontend hardcodes `JOBS`. With a
  short, slow-changing list, two one-line consts beat an endpoint + fetch +
  loading state. Add the endpoint if the list grows or needs to be dynamic.
- **One job per skill, one job per agent.** No "any/shared" job for
  cross-job skills. Add a sentinel (e.g. `JobAny`) if that need appears.

## Out of scope

- Renaming `role` → `job` at the DB/JSON layer.
- `User.Role` (admin/viewer).
- Per-agent multiple jobs.
