-- +goose Up
-- What NEAR AI signed for each model call behind a reply: the hashes of the
-- exact request and response bytes, its signature over them, and whether
-- that signature checked out. Hashes and signatures reveal nothing of the
-- conversation, so this sits outside the encrypted message, where a public
-- proof can read it later. A reply that used tools has one row per call.
CREATE TABLE inference_receipts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id       uuid NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    seq              integer NOT NULL,
    provider         text NOT NULL,
    model            text NOT NULL,
    completion_id    text NOT NULL,
    request_sha256   text NOT NULL,
    response_sha256  text NOT NULL,
    -- As NEAR AI returned them; empty when it couldn't be asked.
    signed_text      text NOT NULL DEFAULT '',
    signature        text NOT NULL DEFAULT '',
    signing_address  text NOT NULL DEFAULT '',
    signing_algo     text NOT NULL DEFAULT '',
    -- verified: the signature is over our hashes, by signing_address.
    -- mismatch: it isn't (detail says how). unavailable: no signature to check.
    status           text NOT NULL CHECK (status IN ('verified', 'mismatch', 'unavailable')),
    detail           text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (message_id, seq)
);
CREATE INDEX inference_receipts_user_idx ON inference_receipts (user_id, created_at DESC);

-- +goose Down
DROP TABLE inference_receipts;
