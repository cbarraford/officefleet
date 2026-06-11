-- +migrate Up

CREATE TABLE IF NOT EXISTS events (
    id              UUID PRIMARY KEY,
    source_plugin   TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    payload_raw     JSONB NOT NULL DEFAULT '{}',
    payload_norm    JSONB NOT NULL DEFAULT '{}',
    identity        TEXT NOT NULL DEFAULT '',
    dedup_key       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    received_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispatched_at   TIMESTAMPTZ
);

-- Event-level dedup: the same occurrence arriving twice (webhook + poll
-- overlap, webhook retries) stores one row via ON CONFLICT DO NOTHING.
CREATE UNIQUE INDEX IF NOT EXISTS events_source_dedup_unique ON events(source_plugin, dedup_key);
CREATE INDEX IF NOT EXISTS events_status_idx ON events(status);
CREATE INDEX IF NOT EXISTS events_source_type_idx ON events(source_plugin, event_type);

CREATE TABLE IF NOT EXISTS poll_cursors (
    plugin      TEXT PRIMARY KEY,
    cursor      TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +migrate Down

DROP TABLE IF EXISTS poll_cursors;
DROP TABLE IF EXISTS events;
