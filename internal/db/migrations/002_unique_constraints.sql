-- +migrate Up
ALTER TABLE agents ADD CONSTRAINT agents_name_unique UNIQUE (name);
ALTER TABLE duties ADD CONSTRAINT duties_name_unique UNIQUE (name);
ALTER TABLE assignments ADD CONSTRAINT assignments_agent_duty_unique UNIQUE (agent_id, duty_id);

-- +migrate Down
ALTER TABLE assignments DROP CONSTRAINT IF EXISTS assignments_agent_duty_unique;
ALTER TABLE duties DROP CONSTRAINT IF EXISTS duties_name_unique;
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_name_unique;
