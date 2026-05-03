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
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=go-builder /app/qmetry /app/demo ./
COPY config.yaml .
EXPOSE 4317 8088
ENTRYPOINT ["./qmetry"]
