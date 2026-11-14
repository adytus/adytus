-- +goose Up
-- Pyth's public Hermes now refuses price requests without a key (HTTP 401),
-- so NEAR is priced from public exchange tickers unless a Hermes server is
-- set. "exchanges" in price_url means Coinbase, Kraken and CoinGecko.
ALTER TABLE near_settings ALTER COLUMN price_url SET DEFAULT 'exchanges';
UPDATE near_settings SET price_url = 'exchanges' WHERE price_url = 'https://hermes.pyth.network';

-- +goose Down
UPDATE near_settings SET price_url = 'https://hermes.pyth.network' WHERE price_url = 'exchanges';
ALTER TABLE near_settings ALTER COLUMN price_url SET DEFAULT 'https://hermes.pyth.network';
