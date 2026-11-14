-- +goose Up
-- Payments on NEAR: native NEAR and USDC, sent to one deposit contract with
-- the user's memo. Unlike Base, everything here is set in the backoffice; the
-- environment has no say. One row, created off.
CREATE TABLE near_settings (
    id                  boolean PRIMARY KEY DEFAULT true CHECK (id),
    enabled             boolean NOT NULL DEFAULT false,
    network             text NOT NULL DEFAULT 'mainnet' CHECK (network IN ('mainnet', 'testnet')),
    rpc_url             text NOT NULL DEFAULT 'https://free.rpc.fastnear.com',
    -- The deposit contract (contracts/near-deposit). Empty until deployed.
    contract            text NOT NULL DEFAULT '',
    -- NEP-141 USDC; empty takes NEAR only. Native USDC on mainnet.
    usdc                text NOT NULL DEFAULT '17208628f84f5d6ad33f0da3bbbeb27ffcb398eac501a31bd6ad2011e36133a1',
    -- NEAR/USD from Pyth's Hermes service.
    price_url           text NOT NULL DEFAULT 'https://hermes.pyth.network',
    price_feed          text NOT NULL DEFAULT 'c415de8d2eba7db216527dff4b60e8f3a5311c740dadb233e13e12547e226750',
    max_price_age_secs  integer NOT NULL DEFAULT 300 CHECK (max_price_age_secs > 0),
    quote_ttl_secs      integer NOT NULL DEFAULT 900 CHECK (quote_ttl_secs > 0),
    min_micros          bigint NOT NULL DEFAULT 5000000 CHECK (min_micros > 0),
    dust_micros         bigint NOT NULL DEFAULT 100000 CHECK (dust_micros >= 0),
    tolerance_bps       integer NOT NULL DEFAULT 100 CHECK (tolerance_bps >= 0 AND tolerance_bps < 10000),
    poll_secs           integer NOT NULL DEFAULT 5 CHECK (poll_secs > 0),
    updated_by          uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at          timestamptz NOT NULL DEFAULT now()
);
INSERT INTO near_settings DEFAULT VALUES;

-- The memo a user attaches to a NEAR payment, which is how it's attributed.
CREATE TABLE near_memos (
    user_id     uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    memo        text NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- How far into each contract's deposit log the watcher has read.
CREATE TABLE near_cursors (
    contract    text PRIMARY KEY,
    next_index  bigint NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Quotes cover ETH on Base and NEAR alike. amount_atomic is wei or yoctoNEAR.
ALTER TABLE eth_quotes RENAME TO crypto_quotes;
ALTER TABLE crypto_quotes RENAME COLUMN amount_wei TO amount_atomic;
ALTER TABLE crypto_quotes ADD COLUMN asset text NOT NULL DEFAULT 'eth' CHECK (asset IN ('eth', 'near'));
ALTER INDEX eth_quotes_user_idx RENAME TO crypto_quotes_user_idx;
CREATE INDEX crypto_quotes_open_idx ON crypto_quotes (user_id, asset, created_at DESC) WHERE status = 'open';

-- A NEAR deposit is keyed by its place in the contract's log ("contract:index"),
-- since the contract can't see its transaction hash.
ALTER TABLE crypto_deposits
    ADD COLUMN chain text NOT NULL DEFAULT 'base' CHECK (chain IN ('base', 'near')),
    ADD COLUMN source_key text UNIQUE;
ALTER TABLE crypto_deposits DROP CONSTRAINT crypto_deposits_asset_check;
ALTER TABLE crypto_deposits ADD CONSTRAINT crypto_deposits_asset_check CHECK (asset IN ('usdc', 'eth', 'near'));

-- +goose Down
DELETE FROM crypto_deposits WHERE chain = 'near';
ALTER TABLE crypto_deposits DROP CONSTRAINT crypto_deposits_asset_check;
ALTER TABLE crypto_deposits ADD CONSTRAINT crypto_deposits_asset_check CHECK (asset IN ('usdc', 'eth'));
ALTER TABLE crypto_deposits DROP COLUMN source_key, DROP COLUMN chain;
DELETE FROM crypto_quotes WHERE asset = 'near';
DROP INDEX crypto_quotes_open_idx;
ALTER INDEX crypto_quotes_user_idx RENAME TO eth_quotes_user_idx;
ALTER TABLE crypto_quotes DROP COLUMN asset;
ALTER TABLE crypto_quotes RENAME COLUMN amount_atomic TO amount_wei;
ALTER TABLE crypto_quotes RENAME TO eth_quotes;
DROP TABLE near_cursors;
DROP TABLE near_memos;
DROP TABLE near_settings;
