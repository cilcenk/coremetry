.PHONY: build build-ui build-go build-demo run dev-ui clean docker-up docker-up-demo docker-down audit

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

# audit — grep-based hard-constraint linter. Catches the
# regression patterns from CLAUDE.md ("Hard constraints" +
# "Performance pitfalls") that are cheap to match statically:
# cache-key length anti-pattern, eager Comboboxes, setInterval
# without document.hidden, direct s.copilot.Explain bypassing
# the wrapper, non-GLOBAL IN over Distributed tables, and
# FROM spans without nearby LIMIT/max_execution_time.
#
# Exits 1 on 🔴 critical findings, 0 on 🟡 warnings only.
# Intended as a pre-tag gate — run before `git tag v0.5.X`.
audit:
	@./scripts/audit.sh

# Docker build picks up VERSION from the env block above and
# tags two images: a precise per-version `coremetry:vX.Y.Z`
# AND `coremetry:latest` so a plain `docker pull coremetry`
# keeps working without a version pin.
#
# Writes VERSION into .env first so subsequent bare
# `docker compose up` invocations (without going through
# make) ALSO pick up the real git-describe version. Otherwise
# the user's login footer + service.version on the OTel
# browser SDK would silently read "dev" again any time
# someone runs compose directly.
docker-up: .env-version
	# v0.6.26 — dropped `--profile demo` from the default up
	# target. Operator-reported: java-demo / jboss-demo / go-demo
	# kept coming back on every rebuild even though docker-
	# compose.yml profile-gated them in v0.6.3 — make was
	# bypassing the gate. Default up now brings ONLY the core
	# stack (coremetry + clickhouse + redis + otel-collector +
	# elasticsearch). To run the demo apps explicitly use
	# `make docker-up-demo` below.
	docker compose up -d --build
	@# Belt-and-braces: docker-compose's build.tags handling
	@# isn't 100% reliable across versions. Re-tag the freshly
	@# built image as `coremetry:latest` so a plain `docker
	@# pull coremetry` (no version pin) always lands on the
	@# image the operator just built. Failure to look up the
	@# image (cold cache) is non-fatal.
	@IMG=$$(docker compose images coremetry --quiet 2>/dev/null | head -1); \
	  if [ -n "$$IMG" ]; then \
	    docker tag "$$IMG" coremetry:latest && \
	      echo "[make] tagged $$IMG as coremetry:latest"; \
	  fi

# Idempotent: rewrites .env every invocation so a fresh git
# tag flows through on the next `up`. Preserves any other
# vars an operator already added (we only touch the VERSION
# line). The dummy target name avoids collisions with the
# .env file itself — make would otherwise treat .env as a
# regular file dep and skip the regenerate.
.PHONY: .env-version
.env-version:
	@touch .env
	@grep -v '^VERSION=' .env > .env.tmp 2>/dev/null || true
	@echo "VERSION=$(VERSION)" >> .env.tmp
	@mv .env.tmp .env
	@echo "$(VERSION)" > VERSION.txt
	@echo "[make] wrote VERSION=$(VERSION) to .env + VERSION.txt"

# Bring up the demo apps (java-demo + jboss-demo + go-demo).
# Separate target so a routine `make docker-up` doesn't pull in
# the demo containers; operators who want them run this once.
.PHONY: docker-up-demo
docker-up-demo: .env-version
	docker compose --profile demo up -d --build

docker-down:
	# Bring down everything — including any opt-in demo profile
	# containers that an operator may have started via
	# `make docker-up-demo`.
	docker compose --profile demo --profile tempo --profile pyroscope --profile grafana down

clean:
	rm -rf coremetry demo frontend/out frontend/.next frontend/node_modules
