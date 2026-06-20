-- +migrate Up
DO $$ BEGIN
    ALTER TABLE agents ADD CONSTRAINT agents_name_unique UNIQUE (name);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE skills ADD CONSTRAINT skills_name_unique UNIQUE (name);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE assignments ADD CONSTRAINT assignments_agent_skill_unique UNIQUE (agent_id, skill_id);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- +migrate Down
ALTER TABLE assignments DROP CONSTRAINT IF EXISTS assignments_agent_skill_unique;
ALTER TABLE skills DROP CONSTRAINT IF EXISTS skills_name_unique;
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_name_unique;
