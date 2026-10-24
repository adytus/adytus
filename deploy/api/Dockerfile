# The API with the backoffice UI, the Python sandbox (Deno and Pyodide) and
# the document reader baked in. Build from the repo root:
#   docker build -f deploy/api/Dockerfile -t adytus-api .
# Search needs SearXNG, which a PaaS doesn't run, so SEARXNG_URL is empty.

FROM oven/bun:1 AS backoffice
WORKDIR /src/apps/backoffice
COPY apps/backoffice/package.json apps/backoffice/bun.lock ./
RUN bun install --frozen-lockfile
COPY apps/backoffice/ ./
# It imports shared components from the web app.
COPY apps/web/src/lib /src/apps/web/src/lib
RUN bun run build

FROM golang:1.27-alpine AS api
WORKDIR /src/apps/api
COPY apps/api/go.mod apps/api/go.sum ./
RUN go mod download
COPY apps/api/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# Pyodide and its packages, checked against pinned hashes. The download
# cache stays in this stage.
FROM denoland/deno:debian-2.9.7 AS pyodide
USER root
COPY deploy/pyodide/fetch.ts /src/fetch.ts
RUN deno run --allow-net --allow-read --allow-write /src/fetch.ts /pyodide && rm -rf /pyodide/cache

FROM denoland/deno:bin-2.9.7 AS deno

# Deno links glibc, so the base is cc rather than static.
FROM gcr.io/distroless/cc-debian12:nonroot
WORKDIR /app
COPY --from=deno /deno /usr/local/bin/deno
COPY --from=pyodide /pyodide /app/pyodide
COPY --from=api /out/api /app/api
COPY --from=api /src/apps/api/models.*.json /app/
COPY --from=backoffice /src/apps/backoffice/dist /app/backoffice
ENV ADDR=:8080 \
    BACKOFFICE_DIR=/app/backoffice \
    PYODIDE_DIR=/app/pyodide \
    SEARXNG_URL= \
    COOKIE_SECURE=true
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/api"]
