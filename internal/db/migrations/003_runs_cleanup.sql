-- +migrate Up
ALTER TABLE runs DROP COLUMN IF EXISTS created_at;
