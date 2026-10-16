# Adytus

Private, encrypted chatbot for enterprise. An OpenWebUI / chatbot hybrid that wraps OpenRouter, where only the user can read their stored data (not even admins).

> Status: working prototype. Accounts with encrypted chats and files, streamed chat with per-call credit metering, image input on vision models, and mocked payments.

## Docs

- [`docs/brief.md`](docs/brief.md) - product brief and original requirements
- [`docs/architecture.md`](docs/architecture.md) - stack, encryption model, and open questions
- [`docs/design.md`](docs/design.md) - design vibes and reference images

## Layout

```
adytus/
  apps/
    api/    Go backend (Fiber v3, pgx, goose, errorx)
    web/    SvelteKit frontend (Bun)
  docs/
  compose.yaml   Postgres, SeaweedFS (S3) and Ollama for local dev
```

## Running locally

```sh
docker compose up -d                       # Postgres :5432, SeaweedFS S3 :9000, Ollama :11434
docker compose exec ollama ollama pull gemma3:270m
docker compose exec ollama ollama pull qwen2.5vl:3b   # vision, ~3 GB, fits in 8 GB VRAM
cp apps/api/.env.example apps/api/.env     # set SERVER_KEY
cd apps/api && go run ./cmd/api            # see the env note below; :8080, runs migrations on start
cd apps/web && bun install && bun run dev  # :5173, proxies /api to :8080
```

The API reads its config from the environment. In bash or zsh, load `.env` with `set -a && . ./.env && set +a` first; that line does not work in fish (use `bash -c 'set -a && . ./.env && set +a && go run ./cmd/api'`).

The LLM defaults to a local Ollama (`LLM_PROVIDER=ollama`), priced like OpenRouter models in `models.ollama.json` so budgeting behaves the same. Ollama's context window is set server-wide by `OLLAMA_CONTEXT_LENGTH` in `compose.yaml` (its OpenAI-compatible API can't set it per request); keep it equal to `context_tokens` in the catalog. Switch to OpenRouter in `.env`; every OpenRouter request is sent with `provider.data_collection=deny` and `zdr=true`.

## Tests

```sh
cd apps/api
TEST_DATABASE_URL='postgres://adytus:adytus@localhost:5432/adytus_test?sslmode=disable' go test -p 1 ./...
cd apps/web && bun run check
```

Integration tests drop and recreate the schema of the database they're given, so point them at a throwaway one.
