# Product Brief

Adytus is an OpenWebUI / chatbot hybrid designed for enterprise. Essentially an OpenRouter wrapper with a lot of token counting and token budgeting logic.

## Product owner message

> adytus will be..
> 1. Private protected encrypted data storage (only user can access the storage, admins cant even access) -> similar like crypto wallet, but more simple login like just using 1 secret encrypted password
> +
> 2. Private (not store data) LLM use tool (maximize the data leak protection) -> basically just using Openrouter AI that has No AI Training, No data storage rule like Azure (ChatGPT 6.1)

## Requirements

### Accounts and billing
- Simple login for now: email and password.
- After login, the user is greeted with a subscription model: **$10 buys roughly $5 worth of OpenRouter usage** on the backend side.
- **Payment is crypto only** (USDC and ETH on Base); there is no free credit.

### LLM access
- Backend holds a **single OpenRouter key** for everyone; usage is **limited per user** (token budgeting).
- Backend **whitelists** the models that can be used with Adytus, selected so they go through **private providers** (no training, no data retention).
- Lots of token counting and budgeting logic.
- **Chat APIs must be mockable** so a local Ollama instance can be used temporarily.

### Privacy and encryption
The key selling point is that data is private and encrypted.

- The password both **generates an encryption key** in the backend and **decrypts that encryption key** (key wrapping).
- The browser smartly manages and decrypts chat data. Needs a smart encryption/decryption exchange for stored chat data.
- **File uploads**: S3-compatible storage, also encrypted, also smartly encrypted/decrypted.
- Any connected session acts as a **host for decrypting objects**.
- Each object has its own **pre-key (per-object key)** that is decrypted first; that key can then be decrypted so the object can be decrypted on the backend even when nobody is connected.
- Don't over-complicate it.

### Stack preferences
- Monorepo. Backend in **Go**, frontend in **Svelte**.
- Use **Rust instead** only if there is a clearly more solid library for the job and it would use fewer tokens; otherwise Go.
- Go libraries: [guregu/null](https://github.com/guregu/null), [pressly/goose](https://github.com/pressly/goose), [joomcode/errorx](https://github.com/joomcode/errorx), Fiber, plus a validator library.
- **Postgres** for the database. For local development, maybe use [erans/pgsqlite](https://github.com/erans/pgsqlite).
- **Bun** for JS tooling.
