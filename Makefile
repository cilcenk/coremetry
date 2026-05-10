.PHONY: build build-ui build-go build-demo run dev-ui clean docker-up docker-down

# VERSION is auto-derived from `git describe` so local builds
# show something like "v0.4.48-3-gabcdef" instead of literal
# "dev". CI / release pipelines override via
#   `VERSION=v0.4.50 make docker-up`
# for clean tagged releases. Same value flows into Vite via
# VITE_APP_VERSION so the frontend's "Coremetry vX" footer +
# OTel browser-SDK resource attribute carry the actual
# version rather than "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
export VERSION
export VITE_APP_VERSION = $(VERSION)

build: build-ui build-go build-demo

build-ui:
	cd frontend && npm install && npm run build

build-go:
	go build -ldflags="-X main.Version=$(VERSION)" -o coremetry .

build-demo:
	go build -o demo ./cmd/demo

run: build
	./coremetry

dev-ui:
	cd frontend && npm run dev

# Docker build picks up VERSION from the env block above and
# tags two images: a precise per-version `coremetry:vX.Y.Z`
# AND `coremetry:latest` so a plain `docker pull coremetry`
# keeps working without a version pin.
docker-up:
	docker compose --profile demo up -d --build

docker-down:
	docker compose --profile demo down

clean:
	rm -rf coremetry demo frontend/out frontend/.next frontend/node_modules
