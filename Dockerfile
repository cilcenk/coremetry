# WITH_CLICKHOUSE_CLIENT — GLOBAL build arg (declared before any FROM so it can
# select the final stage). 0 (default) = lean image; the built-in
# `coremetry ch "<SQL>"` subcommand covers in-pod ClickHouse debugging without
# bloat. 1 = ALSO bundle the real ~460 MB clickhouse-client:
#   docker build --build-arg WITH_CLICKHOUSE_CLIENT=1 -t coremetry:debug .
ARG WITH_CLICKHOUSE_CLIENT=0

# ── Stage 1: build Vite static SPA ────────────────────────────────────────────
FROM node:22-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci || npm install
COPY frontend/ ./
# VITE_APP_VERSION is read by import.meta.env in lib/otel.ts +
# any "Coremetry vX" footer / login-page chrome. Same value
# the Go binary stamps so server + UI agree on the version.
# Falls back to "dev" when invoked outside a tagged context.
ARG VITE_APP_VERSION=dev
ENV VITE_APP_VERSION=${VITE_APP_VERSION}
# Vite outputs to dist/ (not Next.js's out/). Stage 2 embeds it
# via //go:embed all:frontend/dist into the Go binary.
RUN npm run build

# ── Stage 2: build Go binaries (with embedded frontend/dist) ──────────────────
FROM golang:1.25-alpine AS go-builder
# VERSION is the release tag stamped into the binary via -ldflags.
# `docker compose build --build-arg VERSION=$(git describe --tags)`
# during release; falls back to "dev" for local builds without a
# tag context. Surfaced on the login page so operators can match a
# running instance to a release without shelling in.
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-builder /app/frontend/dist /app/frontend/dist
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.Version=${VERSION}" -o coremetry . && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o demo ./cmd/demo

# ── Optional source of the real clickhouse-client (only built when selected) ──
# BuildKit builds a stage only if the chosen target depends on it, so this and
# runtime-1 below are skipped entirely for the default WITH_CLICKHOUSE_CLIENT=0.
FROM clickhouse/clickhouse-server:24.8-alpine AS chclient

# ── Stage 3: minimal runtime base ─────────────────────────────────────────────
FROM alpine:3.20 AS runtime-base
# Re-declare VERSION inside this stage — Docker ARGs are
# scoped per-stage, so the value passed into stage 2 isn't
# visible here without this line.
ARG VERSION=dev
RUN apk add --no-cache ca-certificates tzdata && \
    # Group 0 ownership + g+rwX so OpenShift's random-UID rootless model
    # can read these files: every assigned UID is in the root group,
    # so making the install dir group-readable removes the "permission
    # denied" pitfall without granting world access.
    addgroup -S -g 65532 nonroot 2>/dev/null || true && \
    adduser  -S -u 65532 -G nonroot -h /app nonroot 2>/dev/null || true
WORKDIR /app
COPY --from=go-builder /app/coremetry /app/demo ./
COPY config.yaml .
# Stamp VERSION into a runtime file too. main.go's init reads
# /app/VERSION as a fallback when the linker-injection didn't
# fire — covers the operator who forgot --build-arg in their
# remote pipeline. Defaults to "dev" so the file always exists.
RUN echo "${VERSION:-dev}" > /app/VERSION
RUN chown -R nonroot:0 /app && chmod -R g+rX /app

# Variant 0 (default): lean — built-in `coremetry ch` only.
FROM runtime-base AS runtime-0

# Variant 1: also bundle the real clickhouse-client (~460 MB). The alpine
# clickhouse binary is musl-linked, so it runs on alpine:3.20; 755 keeps it
# executable by OpenShift's random UID. `clickhouse client …` or the symlink.
FROM runtime-base AS runtime-1
COPY --from=chclient /usr/bin/clickhouse /usr/local/bin/clickhouse
RUN ln -sf /usr/local/bin/clickhouse /usr/local/bin/clickhouse-client

# ── Final: pick the variant by the global build arg ───────────────────────────
FROM runtime-${WITH_CLICKHOUSE_CLIENT} AS final
USER 65532
EXPOSE 4317 8088
ENTRYPOINT ["./coremetry"]
