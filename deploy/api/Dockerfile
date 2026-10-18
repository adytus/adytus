# The API with the backoffice UI baked in. Build from the repo root:
#   docker build -f deploy/api/Dockerfile -t adytus-api .
# The Python sandbox needs Docker and gVisor, which a PaaS doesn't offer, so
# run it with PYTHON_ENABLED=false and an empty SEARXNG_URL.

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

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=api /out/api /app/api
COPY --from=api /src/apps/api/models.*.json /app/
COPY --from=backoffice /src/apps/backoffice/dist /app/backoffice
ENV ADDR=:8080 \
    BACKOFFICE_DIR=/app/backoffice \
    PYTHON_ENABLED=false \
    SEARXNG_URL= \
    COOKIE_SECURE=true
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/api"]
