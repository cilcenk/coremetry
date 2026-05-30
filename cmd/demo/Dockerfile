# ── go-demo: retail-banking synthetic-telemetry generator ─────────────────────
# Standalone image for the cmd/demo traffic generator so it can be deployed
# on Kubernetes via the Coremetry Helm chart (charts/coremetry, goDemo.enabled).
# The docker-compose `demo` profile builds the SAME binary out of the root
# Dockerfile (which produces both `coremetry` and `demo`); this file builds
# ONLY ./cmd/demo so the chart has a small, single-purpose image to pull.
#
#   docker build -f cmd/demo/Dockerfile -t ghcr.io/cilcenk/coremetry-go-demo:<v> .
#
# Build context MUST be the repo root (it needs go.mod / go.sum + cmd/demo).

# ── Stage 1: build the Go binary ──────────────────────────────────────────────
FROM golang:1.25-alpine AS builder
# VERSION is informational only — cmd/demo doesn't stamp a version into the
# binary (no -X main.Version), so this is here purely to mirror the root
# Dockerfile's release-tag convention and land a /app/VERSION file.
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o demo ./cmd/demo

# ── Stage 2: minimal runtime image (OpenShift restricted-v2 friendly) ─────────
FROM alpine:3.20
ARG VERSION=dev
RUN apk add --no-cache ca-certificates tzdata && \
    # Group 0 ownership + g+rwX so OpenShift's random-UID rootless model can
    # read these files: every assigned UID is in the root group, so making the
    # install dir group-readable removes the "permission denied" pitfall
    # without granting world access. Mirrors the root Dockerfile's pattern.
    addgroup -S -g 65532 nonroot 2>/dev/null || true && \
    adduser  -S -u 65532 -G nonroot -h /app nonroot 2>/dev/null || true
WORKDIR /app
COPY --from=builder /app/demo ./
RUN echo "${VERSION:-dev}" > /app/VERSION && \
    chown -R nonroot:0 /app && chmod -R g+rX /app
USER 65532
# No EXPOSE — go-demo is an egress-only client (POSTs OTLP/HTTP to the
# collector + profiles to coremetry); it serves no inbound traffic.
ENTRYPOINT ["./demo"]
# Default args; the Helm chart / compose override these. -endpoint points at
# the in-cluster collector's OTLP/HTTP port.
CMD ["-endpoint", "http://coremetry-otelcol:4318", "-rps", "3"]
