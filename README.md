# Adytus

The private AI workspace where your data stays sealed. Chats and files are encrypted with your own key, so we can't read what's stored. Private models run on NEAR AI Cloud inside TEEs (Intel TDX + NVIDIA confidential computing), so the model host can't read your prompts. Top-ups go through our own NEAR deposit contract.

> Status: working prototype. Accounts with encrypted chats and files, streamed chat with per-call credit metering, private inference on NEAR AI Cloud, image input on vision models, and payments in NEAR or USDC on NEAR, or USDC or ETH on Base.

## Live demo

URL: [adytus.ai](https://adytus.ai) · login: `testuser@test.com` / `testuserpassword` · NEAR contract: [`adytus-pay.testnet`](https://testnet.nearblocks.io/address/adytus-pay.testnet) (testnet)

## Built at NEAR Hackathon

- **Existing project, disclosed.** Adytus started on 2026-10-16 (first commit `1bbf6a1`). The core app (accounts, encryption, chat, metering, tools, Base payments) was built before the NEAR work below.
- **NEAR work built during the hackathon:**
  - `12f9249`: NEAR AI Cloud as a model provider, with TEE models flagged as private inference
  - `cdba227`: private-inference notice in the chat
  - `5df0b85`: NEAR AI note on sealed attachments
  - `82ce498`: NEAR and USDC payments, plus our own deposit contract, [`contracts/near-deposit`](contracts/near-deposit)
  - `a8e0430`, `83acef4`: signed inference receipts. NEAR AI's per-answer signature is checked against the exact request and response bytes
  - `1aecfec`, `940c461`: public proof page (`/verify/<reply id>`) and a CLI that checks a reply's receipts without trusting our server
- **How NEAR fits:** open-weight models run in Intel TDX and NVIDIA confidential-computing TEEs on NEAR AI Cloud, so the host can't read prompts. Top-ups go through our own NEAR contract: users pay NEAR or USDC with a personal memo, and the API reads the contract's payment log to credit them.

## Quickstart with NEAR AI

Minimal settings in `apps/api/.env`, with a key from [cloud.near.ai](https://cloud.near.ai):

```sh
LLM_PROVIDER=nearai
LLM_API_KEY=sk-...
MODELS_FILE=models.nearai.json
```

Then follow [Running locally](#running-locally). To take payments on NEAR, deploy [`contracts/near-deposit`](contracts/near-deposit) (its README has the commands) and set it up in the backoffice under **Payments → NEAR**.

## Verify it yourself

Every reply from a private (TEE) model shows **✓ Verified in TEE** with a **Proof** link. The proof page at `/verify/<reply id>` is public and holds no chat content, only hashes and signatures. It runs three checks:

1. **Signature valid:** the signer recovered from NEAR AI's signature is the stated enclave key.
2. **Signer matches attested TEE key:** that key is in NEAR AI's attestation report, and its Intel TDX quote commits to the key and to a fresh nonce we chose.
3. **Hashes match:** the signed text is the SHA-256 of the exact request and response bytes.

To check without trusting our server, run the CLI. It recovers the signer locally and fetches the attestation report from NEAR AI directly, with its own nonce:

```sh
cd apps/api
go run ./cmd/verify https://adytus.ai <reply id>
```

It prints each check and ends with `PASS` or `FAIL`. For the full Intel and NVIDIA certificate chains, run the report through [NEAR AI's verifier](https://github.com/nearai/nearai-cloud-verifier).

## Docs

- [`docs/brief.md`](docs/brief.md) - product brief and original requirements
- [`docs/architecture.md`](docs/architecture.md) - stack, encryption model, and open questions
- [`docs/design.md`](docs/design.md) - design vibes and reference images
- [`docs/deploy.md`](docs/deploy.md) - push targets and where each part is deployed
- [`contracts/near-deposit`](contracts/near-deposit) - the NEAR deposit contract: build, deploy, move funds

## Layout

```
adytus/
  apps/
    api/    Go backend (Fiber v3, pgx, goose, errorx)
    web/         SvelteKit frontend (Bun)
    backoffice/  admin UI, served by the API to admins only
  contracts/
    near-deposit/  NEAR deposit contract (Rust, near-sdk)
  docs/
  compose.yaml   Postgres, SeaweedFS (S3) and Ollama for local dev
```

## Running locally

```sh
docker compose up -d                       # Postgres :5432, SeaweedFS S3 :9000, Ollama :11434
docker compose exec ollama ollama pull gemma3:270m
docker compose exec ollama ollama pull qwen2.5vl:3b   # vision, ~3 GB, fits in 8 GB VRAM
cp apps/api/.env.example apps/api/.env     # set SERVER_KEY and LLM_API_KEY (NEAR AI)
cd apps/api && go run ./cmd/api            # see the env note below; :8080, runs migrations on start
cd apps/web && bun install && bun run dev  # :5173, proxies /api to :8080
cd apps/backoffice && bun install && bun run dev  # rebuilds dist/ on change; the API serves it
```

The API reads its config from the environment. In bash or zsh, load `.env` with `set -a && . ./.env && set +a` first; that line does not work in fish (use `bash -c 'set -a && . ./.env && set +a && go run ./cmd/api'`).

`.env.example` starts on NEAR AI Cloud (`LLM_PROVIDER=nearai`, `MODELS_FILE=models.nearai.json`, key from cloud.near.ai): its open-weight models run inside TEEs, so the host can't read prompts, and the backoffice's model browser marks which ones are private. To work offline, switch to the local Ollama (`LLM_PROVIDER=ollama`), priced like OpenRouter models in `models.ollama.json` so budgeting behaves the same. Ollama's context window is set server-wide by `OLLAMA_CONTEXT_LENGTH` in `compose.yaml` (its OpenAI-compatible API can't set it per request); keep it equal to `context_tokens` in the catalog. OpenRouter also works; every OpenRouter request is sent with `provider.data_collection=deny` and `zdr=true`. When present, DeepSeek V4.1 Flash is offered first and is the default for new chats unless the backoffice picks another default. Ollama models are hidden from the picker while its endpoint does not answer.

## Backoffice

Admins set the model whitelist (provider, upstream, name, description, prices, limits, default) and the OpenRouter, Ollama and NEAR AI endpoints (picked from a list of known ones, or a custom URL) and keys for the whole server; changes apply to the next message, without a restart. A model can be rebranded with a description and an identity instruction, which goes into its system prompt so it introduces itself as that name rather than its upstream one. Single model mode offers one model to everyone — the picker disappears and older chats move to it on their next message. `LLM_*` and `MODELS_FILE` only seed the database on first start; after that the backoffice owns them. Keys are sealed with `SERVER_KEY` and never sent back, only their last four characters.

Under **Users**, admins can give or take a user's tokens (each change is recorded in the ledger with a note and who made it) and set a user's own daily cap, or none, in place of `DAILY_CAP_MICROS`. That's how a demo or support account gets credit without a payment.

NEAR payments are set entirely in the backoffice (**Payments → NEAR**): network, RPC, deposit contract, USDC token, price feed and limits. Deploy `contracts/near-deposit` first (see its README), run **Test these settings**, then switch them on. Base payments still come from the `BASE_*` environment variables.

There's no UI to grant access. Set the flag by hand:

```sh
docker compose exec postgres psql -U adytus -c "UPDATE users SET is_admin = true WHERE email = 'you@example.com'"
```

`GET /api/me` then lists `"privileges": ["backoffice"]` and the rail shows a Backoffice link. The backoffice is its own bundle (`apps/backoffice`, built to `dist/`), not part of the web app: the API serves it from `BACKOFFICE_DIR` at `/api/backoffice/ui/` behind the same admin check as its endpoints, with `Cache-Control: no-store`, and answers 404 to everyone else. The web app only holds a loader that imports it by URL. Admin rights are read from the database on every request, so revoking them is immediate. They give no access to anyone's chats or files.

## Python sandbox and document reader

The model's Python tool runs each call in [Pyodide](https://pyodide.org) (CPython 3.14 built for WebAssembly) in a fresh [Deno](https://deno.com) process, so it needs no Docker or VM and runs on a single PaaS box. Deno is started with an empty environment and no permissions but reading the Pyodide directory, so code has no network, environment, subprocesses or host files, even through Pyodide's bridge to JavaScript. Memory (`PYTHON_MEMORY_MB`, resident memory of the whole process) and wall time (`PYTHON_TIMEOUT`, imports included) are capped per run; one process is booted ahead of time (`PYTHON_WARM`). The chat's files are written to `/work/in` in Pyodide's in-memory filesystem, and files the code writes to `/work/out` are saved to the user's files. Nothing carries over between runs.

Code can import numpy, scipy, pandas, matplotlib, Pillow, sympy, lxml, beautifulsoup4, pyyaml and xlrd, and for documents python-docx, python-pptx, openpyxl, xlsxwriter, pypdf, pdfminer.six, fpdf2 and tabulate. Noto Sans fonts for Japanese, Chinese (Simplified and Traditional) and Korean are mounted at `/opt/fonts`, so fpdf2 and matplotlib can write those scripts. Packages and fonts are pinned with their hashes in `deploy/pyodide/fetch.ts`, which downloads Pyodide and extracts them; code can't install anything at run time. The image runs it at build time. For development, install Deno and run once from the repo root:

```sh
deno run --allow-net --allow-read --allow-write deploy/pyodide/fetch.ts apps/api/.pyodide
```

The API won't start without Deno on `PATH` and that directory (`PYODIDE_DIR`, `.pyodide` relative to `apps/api` by default).

The `read_document` tool gives the model a chat's PDF, Word, Excel and PowerPoint files as text, in Go: tables as Markdown, sheets, slides and pages under headings, long files in parts. PDFs go through [PDFium](https://pdfium.googlesource.com/pdfium/) compiled to WebAssembly, with no filesystem and bounded memory. Scanned PDFs have no text to read, as there's no OCR. Either tool being on lets users attach any file.

## Tests

```sh
cd apps/api
TEST_DATABASE_URL='postgres://adytus:adytus@localhost:5432/adytus_test?sslmode=disable' go test -p 1 ./...
cd apps/web && bun run check
cd apps/backoffice && bun run check
```

Integration tests drop and recreate the schema of the database they're given, so point them at a throwaway one.

The sandbox tests need Deno and `apps/api/.pyodide` (or `PYODIDE_DIR`), and skip without them.

## Third-party libraries and AI assistance

**API (Go, `apps/api/go.mod`):** [Fiber](https://github.com/gofiber/fiber) (HTTP), [pgx](https://github.com/jackc/pgx) (Postgres), [goose](https://github.com/pressly/goose) (migrations), [Eino](https://github.com/cloudwego/eino) and its OpenAI adapter (model calls and tool loop), [errorx](https://github.com/joomcode/errorx), [validator](https://github.com/go-playground/validator), [env](https://github.com/caarlos0/env), [google/uuid](https://github.com/google/uuid), [null](https://github.com/guregu/null), [minio-go](https://github.com/minio/minio-go) (S3), [btcd](https://github.com/btcsuite/btcd) (HD key derivation, secp256k1 signature recovery), [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) (Argon2id, Keccak), [golang.org/x/image](https://pkg.go.dev/golang.org/x/image), [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync), [expr](https://github.com/expr-lang/expr) (calculator tool), [excelize](https://github.com/xuri/excelize) (spreadsheets), [go-pdfium](https://github.com/klippa-app/go-pdfium) with [wazero](https://github.com/tetratelabs/wazero) (PDFium as WebAssembly).

**Web and backoffice (`apps/web/package.json`, `apps/backoffice/package.json`):** [Svelte](https://svelte.dev) and [SvelteKit](https://svelte.dev/docs/kit) (with the Vercel adapter), [Vite](https://vite.dev), [Tailwind CSS](https://tailwindcss.com), TypeScript, svelte-check, [svelte-streamdown](https://www.npmjs.com/package/svelte-streamdown) (streamed Markdown), [date-fns](https://date-fns.org), [qrcode-generator](https://www.npmjs.com/package/qrcode-generator). Built with [Bun](https://bun.sh).

**Contract (`contracts/near-deposit/Cargo.toml`):** [near-sdk](https://github.com/near/near-sdk-rs), built with [cargo-near](https://github.com/near/cargo-near).

**Runtime and services:** [Pyodide](https://pyodide.org) under [Deno](https://deno.com) (Python sandbox, with the packages listed above), [PDFium](https://pdfium.googlesource.com/pdfium/), Postgres, [SeaweedFS](https://github.com/seaweedfs/seaweedfs) (S3 storage), [SearXNG](https://github.com/searxng/searxng) (web search), [Ollama](https://ollama.com) (local models), [NEAR AI Cloud](https://cloud.near.ai) and [OpenRouter](https://openrouter.ai) (model providers), Coinbase, Kraken and CoinGecko public tickers (NEAR/USD price, with [Pyth Hermes](https://docs.pyth.network/price-feeds) as an option), NEAR RPC, and Base RPC with a Chainlink ETH/USD feed.

**AI assistance:** code and docs were written with help from [Claude Code](https://claude.com/claude-code) (Anthropic). Every change was reviewed and committed by the author.
