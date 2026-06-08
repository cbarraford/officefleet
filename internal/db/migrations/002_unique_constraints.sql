-- +migrate Up
DO $$ BEGIN
    ALTER TABLE agents ADD CONSTRAINT agents_name_unique UNIQUE (name);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE duties ADD CONSTRAINT duties_name_unique UNIQUE (name);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE assignments ADD CONSTRAINT assignments_agent_duty_unique UNIQUE (agent_id, duty_id);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- +migrate Down
ALTER TABLE assignments DROP CONSTRAINT IF EXISTS assignments_agent_duty_unique;
ALTER TABLE duties DROP CONSTRAINT IF EXISTS duties_name_unique;
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_name_unique;
