# Adytus

Private, encrypted chatbot for enterprise. An OpenWebUI / chatbot hybrid that wraps OpenRouter, where only the user can read their stored data (not even admins).

> Status: working prototype. Accounts with encrypted chats and files, streamed chat with per-call credit metering, image input on vision models, and USDC/ETH payments on Base.

## Docs

- [`docs/brief.md`](docs/brief.md) - product brief and original requirements
- [`docs/architecture.md`](docs/architecture.md) - stack, encryption model, and open questions
- [`docs/design.md`](docs/design.md) - design vibes and reference images

## Layout

```
adytus/
  apps/
    api/    Go backend (Fiber v3, pgx, goose, errorx)
    web/         SvelteKit frontend (Bun)
    backoffice/  admin UI, served by the API to admins only
  docs/
  compose.yaml   Postgres, SeaweedFS (S3) and Ollama for local dev
```

## Push targets

The monorepo is published to three remotes:

| Remote | Repository | Contents |
| --- | --- | --- |
| `origin` | `gecenterprises/adytus` | full monorepo |
| `backend` | `reaganiwadha/adytus-backend` | full monorepo mirror (Railway) |
| `ui` | `reaganiwadha/adytus-ui` | `apps/web` subtree only (Vercel) |

```sh
git remote add backend https://github.com/reaganiwadha/adytus-backend
git remote add ui https://github.com/reaganiwadha/adytus-ui

git push origin main
git push backend main
git subtree push --prefix apps/web ui main
```

## Running locally

```sh
docker compose up -d                       # Postgres :5432, SeaweedFS S3 :9000, Ollama :11434
docker compose exec ollama ollama pull gemma3:270m
docker compose exec ollama ollama pull qwen2.5vl:3b   # vision, ~3 GB, fits in 8 GB VRAM
cp apps/api/.env.example apps/api/.env     # set SERVER_KEY
cd apps/api && go run ./cmd/api            # see the env note below; :8080, runs migrations on start
cd apps/web && bun install && bun run dev  # :5173, proxies /api to :8080
cd apps/backoffice && bun install && bun run dev  # rebuilds dist/ on change; the API serves it
```

The API reads its config from the environment. In bash or zsh, load `.env` with `set -a && . ./.env && set +a` first; that line does not work in fish (use `bash -c 'set -a && . ./.env && set +a && go run ./cmd/api'`).

The LLM defaults to a local Ollama (`LLM_PROVIDER=ollama`), priced like OpenRouter models in `models.ollama.json` so budgeting behaves the same. Ollama's context window is set server-wide by `OLLAMA_CONTEXT_LENGTH` in `compose.yaml` (its OpenAI-compatible API can't set it per request); keep it equal to `context_tokens` in the catalog. Switch to OpenRouter in `.env`; every OpenRouter request is sent with `provider.data_collection=deny` and `zdr=true`. When present, DeepSeek V4.1 Flash is offered first and is the default for new chats unless the backoffice picks another default. Ollama models are hidden from the picker while its endpoint does not answer.

## Backoffice

Admins set the model whitelist (provider, upstream, prices, limits, default) and the OpenRouter and Ollama URLs and keys for the whole server; changes apply to the next message, without a restart. `LLM_*` and `MODELS_FILE` only seed the database on first start; after that the backoffice owns them. Keys are sealed with `SERVER_KEY` and never sent back, only their last four characters.

There's no UI to grant access. Set the flag by hand:

```sh
docker compose exec postgres psql -U adytus -c "UPDATE users SET is_admin = true WHERE email = 'you@example.com'"
```

`GET /api/me` then lists `"privileges": ["backoffice"]` and the rail shows a Backoffice link. The backoffice is its own bundle (`apps/backoffice`, built to `dist/`), not part of the web app: the API serves it from `BACKOFFICE_DIR` at `/api/backoffice/ui/` behind the same admin check as its endpoints, with `Cache-Control: no-store`, and answers 404 to everyone else. The web app only holds a loader that imports it by URL. Admin rights are read from the database on every request, so revoking them is immediate. They give no access to anyone's chats or files.

## Python sandbox

The model's Python tool runs each call in a throwaway container under [gVisor](https://gvisor.dev) (`runsc`), with numpy, scipy, Pillow and matplotlib. Containers have no network, a read-only root, no capabilities and an unprivileged user; a few are started ahead of time (`PYTHON_WARM`). The chat's files are copied into `/work/in`, and files the code writes to `/work/out` are saved to the user's files. Set it up with:

```sh
# install runsc (https://gvisor.dev/docs/user_guide/install/), then:
sudo runsc install && sudo systemctl restart docker
docker build -t adytus-sandbox deploy/sandbox
```

The API uses the `runsc` runtime and won't start if Docker doesn't have it. On a dev machine without gVisor, `PYTHON_RUNTIME=runc` runs the same containers under plain Docker, which shares the host kernel with model-written code; the API logs a warning when it does. `PYTHON_ENABLED=false` turns the tool off. Packages are pinned in `deploy/sandbox/requirements.txt` and installed at build time; code can't install anything at run time. The API drives containers through the `docker` CLI, so its user needs access to the Docker daemon, which is root-equivalent on that host.

## Tests

```sh
cd apps/api
TEST_DATABASE_URL='postgres://adytus:adytus@localhost:5432/adytus_test?sslmode=disable' go test -p 1 ./...
cd apps/web && bun run check
cd apps/backoffice && bun run check
```

Integration tests drop and recreate the schema of the database they're given, so point them at a throwaway one.

The sandbox tests need Docker, the `adytus-sandbox` image and `runsc`, and skip without them; `PYTHON_RUNTIME=runc` runs them under plain Docker.
