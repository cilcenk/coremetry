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

# ── Stage 3: minimal runtime image ────────────────────────────────────────────
FROM alpine:3.20
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
RUN chown -R nonroot:0 /app && chmod -R g+rX /app
USER 65532
EXPOSE 4317 8088
ENTRYPOINT ["./coremetry"]
