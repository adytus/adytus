-- +goose Up
-- Opens the backoffice, which sets the models and provider keys for the
-- whole server. There is no UI to grant it; set it by hand:
--   UPDATE users SET is_admin = true WHERE email = '...';
-- It gives no access to other users' chats or files.
ALTER TABLE users ADD COLUMN is_admin boolean NOT NULL DEFAULT false;

-- Upstream LLM endpoints. API keys are sealed with SERVER_KEY.
CREATE TABLE llm_providers (
    id          text PRIMARY KEY CHECK (id IN ('openrouter', 'ollama')),
    base_url    text NOT NULL,
    sealed_key  bytea,
    updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- The model whitelist. Seeded from MODELS_FILE on first start, then edited
-- in the backoffice. spec is an llm.Model.
CREATE TABLE llm_models (
    id          text PRIMARY KEY,
    provider    text NOT NULL REFERENCES llm_providers(id),
    spec        jsonb NOT NULL,
    enabled     boolean NOT NULL DEFAULT true,
    is_default  boolean NOT NULL DEFAULT false,
    position    integer NOT NULL,
    updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX llm_models_default_idx ON llm_models (is_default) WHERE is_default;

-- +goose Down
DROP TABLE llm_models;
DROP TABLE llm_providers;
ALTER TABLE users DROP COLUMN is_admin;
