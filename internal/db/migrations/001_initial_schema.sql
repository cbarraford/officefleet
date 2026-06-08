-- +migrate Up

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS agents (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name            TEXT NOT NULL,
    CONSTRAINT agents_name_unique UNIQUE (name),
    role            TEXT NOT NULL,
    system_prompt   TEXT NOT NULL DEFAULT '',
    default_backend JSONB NOT NULL DEFAULT '{}',
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS duties (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name            TEXT NOT NULL,
    CONSTRAINT duties_name_unique UNIQUE (name),
    role            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    trigger_kinds   TEXT[] NOT NULL DEFAULT '{}',
    prompt          TEXT NOT NULL DEFAULT '',
    required_tools  TEXT[] NOT NULL DEFAULT '{}',
    output_actions  JSONB NOT NULL DEFAULT '[]',
    config_schema   JSONB NOT NULL DEFAULT '{}',
    backend         JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS assignments (
    id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    agent_id              UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    duty_id               UUID NOT NULL REFERENCES duties(id) ON DELETE CASCADE,
    CONSTRAINT assignments_agent_duty_unique UNIQUE (agent_id, duty_id),
    enabled               BOOLEAN NOT NULL DEFAULT TRUE,
    trigger               JSONB NOT NULL DEFAULT '{}',
    outputs               JSONB NOT NULL DEFAULT '[]',
    config                JSONB NOT NULL DEFAULT '{}',
    backend               JSONB,
    task_prompt_override  TEXT,
    extra_instructions    TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS runs (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    assignment_id           UUID NOT NULL REFERENCES assignments(id) ON DELETE CASCADE,
    agent_id                UUID NOT NULL,
    duty_id                 UUID NOT NULL,
    trigger_kind            TEXT NOT NULL,
    event_id                TEXT,
    rendered_system_prompt  TEXT NOT NULL DEFAULT '',
    rendered_prompt         TEXT NOT NULL DEFAULT '',
    llm_result              JSONB,
    outputs_delivered       JSONB NOT NULL DEFAULT '[]',
    status                  TEXT NOT NULL DEFAULT 'queued',
    tokens                  INTEGER NOT NULL DEFAULT 0,
    cost                    DOUBLE PRECISION NOT NULL DEFAULT 0,
    started_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at             TIMESTAMPTZ,
    error                   TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS runs_assignment_id_idx ON runs(assignment_id);
CREATE INDEX IF NOT EXISTS runs_status_idx ON runs(status);
CREATE INDEX IF NOT EXISTS runs_started_at_idx ON runs(started_at DESC);

CREATE TABLE IF NOT EXISTS assignment_state (
    assignment_id   UUID NOT NULL,
    key             TEXT NOT NULL,
    value           BYTEA NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (assignment_id, key)
);

CREATE TABLE IF NOT EXISTS assignment_notes (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    assignment_id   UUID NOT NULL,
    note            JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS assignment_notes_assignment_id_idx ON assignment_notes(assignment_id);

CREATE TABLE IF NOT EXISTS assignment_processed (
    assignment_id   UUID NOT NULL,
    dedup_key       TEXT NOT NULL,
    processed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (assignment_id, dedup_key)
);

CREATE TABLE IF NOT EXISTS secrets (
    name            TEXT PRIMARY KEY,
    encrypted_value BYTEA NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +migrate Down

DROP TABLE IF EXISTS secrets;
DROP TABLE IF EXISTS assignment_processed;
DROP TABLE IF EXISTS assignment_notes;
DROP TABLE IF EXISTS assignment_state;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS assignments;
DROP TABLE IF EXISTS duties;
DROP TABLE IF EXISTS agents;
