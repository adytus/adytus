-- +goose Up
CREATE TABLE users (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email           text NOT NULL UNIQUE,
    -- sha256 of the Argon2id auth key; never the KEK.
    auth_verifier   bytea NOT NULL,
    kdf_salt        bytea NOT NULL,
    kdf_params      jsonb NOT NULL,
    -- Master key wrapped with the password-derived KEK.
    wrapped_mk      bytea NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Encrypted objects: chats, messages, files. Small payloads live in
-- `ciphertext`; files live in S3 under `s3_key` (also encrypted with the DEK).
CREATE TABLE objects (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id           uuid REFERENCES objects(id) ON DELETE CASCADE,
    kind                text NOT NULL CHECK (kind IN ('chat', 'message', 'file')),
    -- DEK wrapped with the user's MK.
    wrapped_dek         bytea NOT NULL,
    -- DEK wrapped with the server key. Set only when the user opts this
    -- object into offline processing; NULL means only the user can read it.
    server_wrapped_dek  bytea,
    ciphertext          bytea,
    s3_key              text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (ciphertext IS NOT NULL OR s3_key IS NOT NULL)
);
CREATE INDEX objects_user_kind_idx ON objects (user_id, kind, created_at DESC);
CREATE INDEX objects_parent_idx ON objects (parent_id);

-- Money is tracked in micro-USD (1 USD = 1_000_000).
CREATE TABLE subscriptions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan            text NOT NULL,
    paid_micros     bigint NOT NULL,
    credit_micros   bigint NOT NULL,
    payment_ref     text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Append-only ledger. Balance = sum(delta_micros) per user. Positive rows are
-- credits from subscriptions; negative rows are LLM usage.
CREATE TABLE credit_ledger (
    id                  bigserial PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    delta_micros        bigint NOT NULL,
    reason              text NOT NULL CHECK (reason IN ('subscription', 'usage', 'adjustment')),
    subscription_id     uuid REFERENCES subscriptions(id),
    model               text,
    prompt_tokens       integer,
    completion_tokens   integer,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX credit_ledger_user_idx ON credit_ledger (user_id, created_at DESC);

-- +goose Down
DROP TABLE credit_ledger;
DROP TABLE subscriptions;
DROP TABLE objects;
DROP TABLE users;
