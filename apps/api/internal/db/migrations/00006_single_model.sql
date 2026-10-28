-- +goose Up
-- Single model mode: the model marked here is the only one users are offered
-- and every reply runs on it. No row marked means the mode is off.
ALTER TABLE llm_models ADD COLUMN is_single boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX llm_models_single_idx ON llm_models (is_single) WHERE is_single;

-- +goose Down
DROP INDEX llm_models_single_idx;
ALTER TABLE llm_models DROP COLUMN is_single;
