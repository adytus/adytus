-- +goose Up
-- Admins can give or take credit (reason 'adjustment'); each such row says
-- why and who did it.
ALTER TABLE credit_ledger
    ADD COLUMN note text,
    ADD COLUMN created_by uuid REFERENCES users(id) ON DELETE SET NULL;

-- A user's own daily usage cap. NULL uses the server's DAILY_CAP_MICROS;
-- 0 means no cap.
ALTER TABLE users ADD COLUMN daily_cap_micros bigint CHECK (daily_cap_micros >= 0);

-- +goose Down
ALTER TABLE users DROP COLUMN daily_cap_micros;
ALTER TABLE credit_ledger DROP COLUMN created_by, DROP COLUMN note;
