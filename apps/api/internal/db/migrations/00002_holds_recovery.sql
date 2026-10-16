-- +goose Up
-- MK wrapped with a key derived from the user's one-time recovery code.
ALTER TABLE users ADD COLUMN recovery_wrapped_mk bytea;

-- Credit reserved for in-flight LLM requests. Available credit is the ledger
-- balance minus unexpired holds. A hold is replaced by a usage ledger row when
-- the request finishes; holds left by a crash simply expire.
CREATE TABLE credit_holds (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount_micros   bigint NOT NULL CHECK (amount_micros > 0),
    expires_at      timestamptz NOT NULL
);
CREATE INDEX credit_holds_user_idx ON credit_holds (user_id, expires_at);

-- +goose Down
DROP TABLE credit_holds;
ALTER TABLE users DROP COLUMN recovery_wrapped_mk;
