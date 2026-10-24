-- +goose Up
-- One deposit address per user on Base, derived from an extended public key
-- (BASE_XPUB) at child index idx. The server never holds a private key.
-- eth_accounted_wei is the ETH balance already credited; a balance above it
-- is a new deposit, a balance below it means the address was swept.
CREATE TABLE deposit_addresses (
    user_id            uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    idx                bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    address            text UNIQUE,
    eth_accounted_wei  numeric(78, 0) NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now()
);

-- Every confirmed on-chain payment that was credited (or ignored as dust).
-- USDC rows are keyed by (tx_hash, log_index) so a transfer is credited once;
-- ETH rows have no tx hash, since a balance increase can come from a contract.
CREATE TABLE crypto_deposits (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    asset            text NOT NULL CHECK (asset IN ('usdc', 'eth')),
    amount_atomic    numeric(78, 0) NOT NULL,
    credit_micros    bigint NOT NULL,
    -- micro-USD per ETH used to price an ETH deposit.
    rate_micros      bigint,
    quote_id         uuid,
    status           text NOT NULL CHECK (status IN ('credited', 'ignored')),
    tx_hash          text,
    log_index        integer,
    block_number     bigint NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX crypto_deposits_log_idx ON crypto_deposits (tx_hash, log_index) WHERE tx_hash IS NOT NULL;
CREATE INDEX crypto_deposits_user_idx ON crypto_deposits (user_id, created_at DESC);

-- A locked ETH/USD rate for a top-up. An ETH deposit seen before expires_at
-- (plus a short slack) is credited at rate_micros; later ones at the live rate.
CREATE TABLE eth_quotes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    usd_micros    bigint NOT NULL,
    amount_wei    numeric(78, 0) NOT NULL,
    rate_micros   bigint NOT NULL,
    status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'credited')),
    expires_at    timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX eth_quotes_user_idx ON eth_quotes (user_id, created_at DESC);

-- Where the USDC log scan stopped, per chain.
CREATE TABLE chain_cursors (
    chain_id    bigint PRIMARY KEY,
    last_block  bigint NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE chain_cursors;
DROP TABLE eth_quotes;
DROP TABLE crypto_deposits;
DROP TABLE deposit_addresses;
