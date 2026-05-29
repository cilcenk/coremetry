.PHONY: build build-ui build-go build-demo run dev-ui clean docker-up docker-up-demo docker-down docker-distributed-up docker-distributed-down minikube-up minikube-down audit

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

# v0.6.65 — local distributed (ingest/api/worker) overlay, mirroring
# the Helm chart's deployment.mode=distributed. Reuses .env-version so
# the freshly-built image is stamped with the real git-describe VERSION
# (same as docker-up). `--scale coremetry=0` neutralizes the monolithic
# all-mode pod so coremetry-api cleanly owns 8088. Core stack only — add
# `--profile demo` by hand for traffic generators.
docker-distributed-up: .env-version
	docker compose -f docker-compose.yml -f docker-compose.distributed.yml up -d --build --scale coremetry=0
	@IMG=$$(docker compose -f docker-compose.yml -f docker-compose.distributed.yml images coremetry-api --quiet 2>/dev/null | head -1); \
	  if [ -n "$$IMG" ]; then \
	    docker tag "$$IMG" coremetry:latest && \
	      echo "[make] tagged $$IMG as coremetry:latest"; \
	  fi

docker-distributed-down:
	docker compose -f docker-compose.yml -f docker-compose.distributed.yml \
	  --profile demo --profile tempo --profile pyroscope --profile grafana down

# v0.6.67 — PROD-PARITY: deploy the REAL Helm chart to local minikube in
# distributed mode (ingest/api/worker + bundled CH/Redis/collector) via
# values-minikube.yaml. Builds the coremetry image, side-loads it into
# minikube (no registry/no GHCR), then helm upgrade --install. Use
# docker-compose for the fast edit→rebuild→verify dev loop; use THIS to
# validate that the production Helm chart + distributed topology actually
# deploy + run. The minikube values relax the bundled CH/Redis container
# securityContext (vanilla k8s has no SCC to inject a runAsUser).
minikube-up:
	@minikube status >/dev/null 2>&1 || minikube start --driver=docker --cpus=4 --memory=6144
	@# v0.6.71 — pass VERSION as build-args (else the Dockerfile's ARG
	@# VERSION=dev default bakes "dev"). v0.7.2 — tag the image with the real
	@# $(VERSION), NOT :local. minikube's image store keys on the tag, so
	@# reloading the same :local tag silently kept the STALE binary on the node
	@# (operator saw v0.6.65 long after newer tags shipped — a unique per-build
	@# tag can't collide). --set image.tag wires the version tag into the deploy.
	docker build --build-arg VERSION=$(VERSION) --build-arg VITE_APP_VERSION=$(VERSION) -t ghcr.io/cilcenk/coremetry:$(VERSION) .
	minikube image load ghcr.io/cilcenk/coremetry:$(VERSION)
	helm upgrade --install coremetry charts/coremetry -n coremetry --create-namespace \
	  -f values-minikube.yaml --set image.tag=$(VERSION) --wait --timeout 8m
	@echo "[make] Coremetry distributed on minikube — all roles up."
	@echo "[make]   UI:        kubectl port-forward -n coremetry svc/coremetry 8090:8088  → http://localhost:8090"
	@echo "[make]   Dashboard: minikube dashboard --url"

minikube-down:
	-helm uninstall coremetry -n coremetry
	-kubectl delete namespace coremetry --ignore-not-found

clean:
	rm -rf coremetry demo frontend/out frontend/.next frontend/node_modules
