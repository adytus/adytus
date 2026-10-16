# Architecture Notes

Initial thinking from the brief; nothing here is final or implemented.

## Stack

| Area | Choice |
| --- | --- |
| Backend | Go, Fiber, validator lib |
| Go libs | guregu/null, pressly/goose, joomcode/errorx |
| Database | Postgres (local dev: pgsqlite, see caveats) |
| Object storage | S3-compatible (MinIO locally) |
| Frontend | Svelte, Bun |
| LLM | OpenRouter (single backend key), Ollama for local mock |

## Encryption model (draft)

Key hierarchy:

1. **Password** -> KDF (e.g. Argon2id) -> **KEK** (key-encryption key).
2. On signup the backend generates a random **master key (MK)** and stores it wrapped with the KEK. Password change only re-wraps the MK.
3. Each object (chat, message blob, file) gets its own random **object key (DEK)**, stored wrapped with the MK.
4. Object data is encrypted with its DEK (e.g. AES-256-GCM / XChaCha20-Poly1305).

Sessions as decrypt hosts:

- While a user is connected, the session holds the unwrapped MK in memory and can unwrap DEKs.
- The browser decrypts chats client-side. The backend can decrypt an object with nobody connected only if its DEK was handed over (e.g. a per-object key re-wrapped to a server-held key), so this is the main design decision to settle.
- Prompts must reach OpenRouter in plaintext, so the backend sees them in memory during a request but must never persist them unencrypted.

## Open questions

- Which objects may the server decrypt offline (title generation, summaries, file processing)? Each one weakens the "admins can't access" guarantee.
- Password recovery: with a single secret password and no admin access, a lost password means lost data. Do we offer a recovery key?
- Streaming responses: encrypt-on-write after the stream completes vs per-chunk.
- Token counting: tokenizer per model vs trusting OpenRouter usage fields; budget accounting in the ledger.
- OpenRouter provider routing: enforce per-request provider restrictions (no data collection / zero data retention) in addition to the model whitelist.

## Local dev notes

- **pgsqlite** is a SQLite-backed Postgres wire-protocol proxy. Its README lists no `BYTEA` and no explicit `ON CONFLICT` / `TIMESTAMPTZ` support, which is a poor fit for encrypted blob storage. Prefer a real Postgres (docker compose or a local install) and treat pgsqlite as a fallback; verify before committing to it.
- **Mockable chat API**: define a provider interface in the backend with OpenRouter and OpenAI-compatible (Ollama) implementations, selected by config.
- **Payments** are mocked behind an interface as well.
