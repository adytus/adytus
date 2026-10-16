-- +goose Up
-- Per-user preferences, e.g. {"tools": {"web_search": true}}. Not sensitive,
-- so stored in the clear.
ALTER TABLE users ADD COLUMN settings jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE users DROP COLUMN settings;
