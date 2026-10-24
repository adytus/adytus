# Architecture Notes

Started as notes on the brief; decisions that have been implemented are marked.

## Stack

| Area | Choice |
| --- | --- |
| Backend | Go, Fiber, validator lib |
| Go libs | guregu/null, pressly/goose, joomcode/errorx |
| Database | Postgres (local dev: pgsqlite, see caveats) |
| Object storage | S3-compatible (SeaweedFS locally) |
| Frontend | Svelte, Bun |
| LLM | OpenRouter (single backend key), Ollama for local mock, both through Eino |

## Encryption model

Implemented primitives live in `apps/api/internal/crypto`: Argon2id (one run, split into an auth key and the KEK; only `sha256(auth key)` is stored), XChaCha20-Poly1305 for all sealing and key wrapping, with the owning row's ID as associated data.


Key hierarchy:

1. **Password** -> KDF (e.g. Argon2id) -> **KEK** (key-encryption key).
2. On signup the backend generates a random **master key (MK)** and stores it wrapped with the KEK. Password change only re-wraps the MK.
3. Each object (chat, message blob, file) gets its own random **object key (DEK)**, stored wrapped with the MK.
4. Object data is encrypted with its DEK (e.g. AES-256-GCM / XChaCha20-Poly1305).

Sessions as decrypt hosts:

- While a user is connected, the session holds the unwrapped MK in memory and can unwrap DEKs.
- The browser decrypts chats client-side.
- **Decided: offline decryption is opt-in per object.** By default a DEK is wrapped only with the MK, so nothing is readable while the user is offline. When the user opts an object in (e.g. for background title generation or file processing), its DEK is also wrapped with the server key (`SERVER_KEY`) and stored in `objects.server_wrapped_dek`. Clearing that column revokes access. The UI must make clear which objects are opted in, since those are readable by whoever holds the server key.
- Prompts must reach OpenRouter in plaintext, so the backend sees them in memory during a request but must never persist them unencrypted.

## Open questions

- Password recovery: with a single secret password and no admin access, a lost password means lost data. Do we offer a recovery key?
- Streaming responses: encrypt-on-write after the stream completes vs per-chunk.
- Token counting: tokenizer per model vs trusting OpenRouter usage fields; budget accounting in the ledger.
- OpenRouter provider routing: enforce per-request provider restrictions (no data collection / zero data retention) in addition to the model whitelist.

## Local dev notes

- **pgsqlite** is a SQLite-backed Postgres wire-protocol proxy. Its README lists no `BYTEA` and no explicit `ON CONFLICT` / `TIMESTAMPTZ` support, which is a poor fit for encrypted blob storage. Prefer a real Postgres (docker compose or a local install) and treat pgsqlite as a fallback; verify before committing to it.
- **Mockable chat API (decided)**: the backend talks to models through [Eino](https://github.com/cloudwego/eino)'s `ToolCallingChatModel`, using the eino-ext OpenAI adapter for both OpenRouter and Ollama. Eino also brings tool calling and agents (ReAct, graphs), so we don't maintain our own tool-calling stack. The streaming tool loop is the one exception: Eino's ReAct agent stops at the first text chunk, so models that write a sentence before calling a tool (DeepSeek, Claude) silently lose the call. `server.reply` runs the loop instead, streaming that preamble and then the call.
- **Payments** are crypto only (see below). There is no free or simulated credit: an account has no tokens until a deposit is confirmed.

## Model calls and billing (implemented)

- **Metering per call.** `internal/meter` wraps the Eino model. Every `Generate`/`Stream` call reserves a hold (estimated prompt plus the catalog's max output), forces the catalog's upstream model and output limit, and settles what the call used. An agent loop therefore pays step by step and stops at the first step the user can't afford. The chat handler reserves the first call's hold before responding, so a lack of credit is a plain 402.
- **What gets billed.** OpenRouter's reported `usage.cost` when present (read off the HTTP stream, since Eino's adapter drops it), else provider-reported token counts at catalog prices, else (a stream cut off before usage arrived) our pessimistic estimate. A call that produced nothing is released, not billed.
- **Token credits.** Users see balances and charges in whole token credits, where 1 token = 1,000 micro-USD (so $10 buys 10,000 tokens). The ledger still stores micro-USD; holds and settlements round up to the next whole token.
- **Images.** Vision models accept image attachments. Images are downscaled to 1024px and re-encoded as JPEG before sending, which also strips metadata; the catalog's `image_tokens` is the per-image allowance used for holds.
- **Recovery code (product decision).** Signup and password change issue a recovery code that also wraps the MK; it's the only way back in after a lost password.
- **Sessions are in memory**, so restarting the API signs everyone out. Fine for one instance; several instances would need a shared session store.
- **Backoffice.** `users.is_admin` opens server-wide settings: the model catalog (`llm_models`) and provider URLs and keys (`llm_providers`, keys sealed with `SERVER_KEY`). The live catalog and provider clients sit in an `llm.Registry` snapshot; each change is validated in its transaction (at least one enabled model, every used provider configured) and swapped in after commit. A reply keeps the snapshot it started with. Like sessions, the snapshot is per instance: several instances would need to reload on change.

## Crypto payments on Base (implemented)

Off unless `BASE_XPUB` is set. Users pay USDC or ETH on Base; both credit the same ledger as a `subscription` row.

- **Non-custodial.** Each user gets one deposit address, derived from the operator's extended public key at a per-user child index (`deposit_addresses.idx`). The server holds no private key, and an `xprv` in `BASE_XPUB` is refused. Funds are swept with keys kept offline. Attribution is by address, so there is no memo or exact amount to get wrong, and top-ups can be any amount over `CRYPTO_MIN_MICROS`.
- **USDC.** 1 USDC base unit is exactly 1 micro-USD, so the transferred amount is the credit. The watcher scans USDC `Transfer` logs to deposit addresses behind a cursor (`chain_cursors`) that trails the head by `CRYPTO_CONFIRMATIONS`. `(tx_hash, log_index)` is unique, so a transfer credits once.
- **ETH.** Detected by reading the address balance at the confirmed block and crediting the rise over `eth_accounted_wei`, not by scanning transactions, which would miss smart-contract wallets (Coinbase Smart Wallet is common on Base). A fall is a sweep and moves the mark down. Addresses with a quote from the last 24h are polled every tick; the rest every `CRYPTO_FULL_SCAN`. Sweeping an address in the same window as a deposit can hide that deposit, so sweep addresses with no open quote.
- **ETH pricing.** The Chainlink ETH/USD feed is read on-chain (`BASE_ETH_USD_FEED`); a round older than an hour is refused. A quote locks the rate for `CRYPTO_QUOTE_TTL`. A payment seen before the quote expires (plus a minute) is credited at the locked rate, and up to `CRYPTO_ETH_TOLERANCE_BPS` under the quoted amount still gets the full quoted USD. Later payments get the live rate. Payments under `CRYPTO_DUST_MICROS` aren't credited (USDC is recorded as `ignored`; ETH keeps accumulating).
- **Limits.** The USDC scan filters on all deposit addresses in batches of 100, and the ETH full scan makes one RPC call per address; both are fine for a prototype and would want batching or an indexer at scale. The cursor is per database, but the watcher is idempotent, so two instances only duplicate work.
- **Testing.** `internal/billing/deposits_test.go` runs the watcher against a scripted JSON-RPC node and a real Postgres (`TEST_DATABASE_URL`). For a live check use Base Sepolia (`BASE_CHAIN_ID=84532`, see `.env.example`).
