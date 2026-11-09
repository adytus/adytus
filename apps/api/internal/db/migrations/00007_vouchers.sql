-- +goose Up
-- A public code that discounts top-ups until expires_at. Paying (100-percent)%
-- of a list price credits the whole list price. Ending one early just moves
-- expires_at, so the rows stay for the report.
CREATE TABLE vouchers (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code        text NOT NULL UNIQUE,
    percent     integer NOT NULL CHECK (percent BETWEEN 1 AND 90),
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- The voucher a user has applied. Payments are attributed by address, with
-- nothing to carry a code, so it covers every deposit until it expires or is
-- removed.
CREATE TABLE voucher_claims (
    user_id     uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    voucher_id  uuid NOT NULL REFERENCES vouchers(id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- bonus_micros is the part of credit_micros that wasn't paid for.
ALTER TABLE crypto_deposits
    ADD COLUMN voucher_id uuid REFERENCES vouchers(id),
    ADD COLUMN bonus_micros bigint NOT NULL DEFAULT 0;
CREATE INDEX crypto_deposits_voucher_idx ON crypto_deposits (voucher_id) WHERE voucher_id IS NOT NULL;

-- +goose Down
DROP INDEX crypto_deposits_voucher_idx;
ALTER TABLE crypto_deposits DROP COLUMN bonus_micros, DROP COLUMN voucher_id;
DROP TABLE voucher_claims;
DROP TABLE vouchers;
