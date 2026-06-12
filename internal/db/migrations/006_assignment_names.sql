-- +migrate Up

ALTER TABLE assignments ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE assignments DROP CONSTRAINT IF EXISTS assignments_agent_duty_unique;

DO $$ BEGIN
    ALTER TABLE assignments ADD CONSTRAINT assignments_agent_duty_name_unique UNIQUE (agent_id, duty_id, name);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- +migrate Down

ALTER TABLE assignments DROP CONSTRAINT IF EXISTS assignments_agent_duty_name_unique;
ALTER TABLE assignments DROP COLUMN IF EXISTS name;

DO $$ BEGIN
    ALTER TABLE assignments ADD CONSTRAINT assignments_agent_duty_unique UNIQUE (agent_id, duty_id);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;
