-- +goose Up
-- Login with NEAR can be switched off in the backoffice. It's on by default,
-- on the network set for NEAR payments.
ALTER TABLE near_settings ADD COLUMN login_enabled boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE near_settings DROP COLUMN login_enabled;
