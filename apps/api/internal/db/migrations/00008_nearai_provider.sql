-- +goose Up
-- NEAR AI Cloud joins the upstream providers. Its row is seeded on the next
-- start, with no key, like any provider the environment doesn't name.
ALTER TABLE llm_providers DROP CONSTRAINT llm_providers_id_check;
ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_id_check
    CHECK (id IN ('openrouter', 'ollama', 'nearai'));

-- +goose Down
DELETE FROM llm_models WHERE provider = 'nearai';
DELETE FROM llm_providers WHERE id = 'nearai';
ALTER TABLE llm_providers DROP CONSTRAINT llm_providers_id_check;
ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_id_check
    CHECK (id IN ('openrouter', 'ollama'));
