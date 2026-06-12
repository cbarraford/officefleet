-- +migrate Up
-- Historical cleanup only: this migration removes the legacy created_at
-- duplicate. Run-row retention is handled operationally with `fleet runs prune`.
ALTER TABLE runs DROP COLUMN IF EXISTS created_at;
-- +migrate Down
ALTER TABLE runs ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
