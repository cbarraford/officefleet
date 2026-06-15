-- +migrate Up
-- Assignments are unique per (agent, duty, name), not (agent, duty): an agent
-- can hold the same duty under more than one trigger/purpose (e.g. a manual and
-- a cron variant). Without the name discriminator the seed upsert silently
-- overwrote one with the other (issue #6).
ALTER TABLE assignments ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';

DO $$ BEGIN
    ALTER TABLE assignments DROP CONSTRAINT IF EXISTS assignments_agent_duty_unique;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE assignments ADD CONSTRAINT assignments_agent_duty_name_unique UNIQUE (agent_id, duty_id, name);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- +migrate Down
ALTER TABLE assignments DROP CONSTRAINT IF EXISTS assignments_agent_duty_name_unique;
ALTER TABLE assignments ADD CONSTRAINT assignments_agent_duty_unique UNIQUE (agent_id, duty_id);
ALTER TABLE assignments DROP COLUMN IF EXISTS name;
