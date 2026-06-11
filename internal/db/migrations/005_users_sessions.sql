-- +migrate Up

CREATE TABLE IF NOT EXISTS users (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    username        TEXT NOT NULL,
    CONSTRAINT users_username_unique UNIQUE (username),
    password_hash   TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('admin','viewer')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash      TEXT PRIMARY KEY,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at);

ALTER TABLE agents ADD COLUMN IF NOT EXISTS avatar_url TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS hired_at DATE;

-- +migrate Down

ALTER TABLE agents DROP COLUMN IF EXISTS hired_at;
ALTER TABLE agents DROP COLUMN IF EXISTS avatar_url;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
