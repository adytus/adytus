# Adytus

Private, encrypted chatbot for enterprise. An OpenWebUI / chatbot hybrid that wraps OpenRouter, where only the user can read their stored data (not even admins).

> Status: early scaffold. The API serves health, models and plans; auth, chat and storage are next.

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
  compose.yaml   Postgres + MinIO for local dev
```

## Running locally

```sh
docker compose up -d                       # Postgres :5432, MinIO :9000
cp apps/api/.env.example apps/api/.env     # set SERVER_KEY
cd apps/api && set -a && . ./.env && set +a && go run ./cmd/api   # :8080, runs migrations on start
cd apps/web && bun install && bun run dev  # :5173, proxies /api to :8080
```

The LLM defaults to a local Ollama (`LLM_PROVIDER=ollama`). Switch to OpenRouter in `.env`; every OpenRouter request is sent with `provider.data_collection=deny` and `zdr=true`.
