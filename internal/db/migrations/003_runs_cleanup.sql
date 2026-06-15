-- NOTE: despite the filename, this migration performs NO row cleanup/retention.
-- It only drops a redundant runs.created_at column (runs track started_at). Row
-- retention is operator-driven via `fleet runs prune --older-than`. The filename
-- is kept as-is because renaming an applied migration changes its version key and
-- would re-run it on existing databases.
-- +migrate Up
ALTER TABLE runs DROP COLUMN IF EXISTS created_at;
-- +migrate Down
ALTER TABLE runs ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
