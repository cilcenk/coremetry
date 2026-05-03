# ── Stage 1: build Next.js static export ──────────────────────────────────────
FROM node:20-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci || npm install
COPY frontend/ ./
RUN npm run build

# ── Stage 2: build Go binaries (with embedded frontend/out) ───────────────────
FROM golang:1.25-alpine AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-builder /app/frontend/out /app/frontend/out
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o qmetry . && \
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
COPY --from=go-builder /app/qmetry /app/demo ./
COPY config.yaml .
RUN chown -R nonroot:0 /app && chmod -R g+rX /app
USER 65532
EXPOSE 4317 8088
ENTRYPOINT ["./qmetry"]
