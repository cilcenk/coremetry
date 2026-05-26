# Coremetry

[![CI](https://github.com/cilcenk/coremetry/actions/workflows/ci.yml/badge.svg)](https://github.com/cilcenk/coremetry/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/cilcenk/coremetry?display_name=tag&sort=semver)](https://github.com/cilcenk/coremetry/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cilcenk/coremetry)](https://goreportcard.com/report/github.com/cilcenk/coremetry)

**Open-source, OpenTelemetry-native, enterprise-grade APM.**
Traces, metrics, logs, profiles, RUM — all on ClickHouse, all
behind a single Go binary, all manageable via Helm. Designed
for billion-span/day production scale; runs on a laptop in
docker-compose for evaluation.

```
apps ──▶ OTel Collector ──▶ Coremetry  (gRPC :4317  /  HTTP :8088)
                              │
                              ├── ClickHouse  — primary store (spans / logs / metrics / profiles)
                              ├── Redis       — response cache + leader lock (optional)
                              └── Web UI      — Vite SPA embedded into the binary
```

---

## What it is

Coremetry collects every signal the OpenTelemetry ecosystem
produces and turns them into the dashboards an SRE actually
opens during an incident. Native OTLP ingest (gRPC + HTTP) on
the same wire format Datadog, Honeycomb, Dynatrace, Grafana
Cloud agents already speak — drop the Coremetry endpoint into
your existing OTel collector or SDK and you're shipping data.
No proprietary SDK, no vendor agent, no shim layer.

---

## Features

### Signal coverage

- **Distributed tracing** — full waterfall view, critical-path
  highlighting, side-by-side trace compare with per-operation
  delta diff, Tempo-compatible `/api/traces` for Grafana.
- **Metrics** — OTLP metrics ingestion, span-derived RED
  metrics (rate / errors / duration aggregated per service +
  operation), built-in dashboards.
- **Logs** — native OTLP logs with token-bloom-filter search
  on the body, optional external Elasticsearch read backend
  for organisations whose pipeline already terminates there.
- **Profiles** — pprof / pyroscope-compatible ingestion +
  flamegraph viewer.
- **Frontend RUM** — built-in browser SDK dogfooding so the UI
  ships its own page-load + click + fetch traces back through
  OTLP. Use the pattern as a template for instrumenting your
  own apps.

### SRE workflow

- **SRE-perspective dashboards** out of the box — Golden
  Signals (Google), RED (Tom Wilkie), USE (Brendan Gregg) +
  Incident War Room, Latency Investigation, Error Hunting,
  Database Performance, External Dependencies, Infrastructure
  Saturation. Inspired by Datadog / Honeycomb / Dynatrace
  patterns.
- **Custom dashboards** — drag-droppable panels, Grafana-style
  template variables (`$service`, `$env` …). Versioned preset
  bundle migration so upgrades update the seed dashboards
  without clobbering user-built ones.
- **Service map** — force-directed topology graph derived from
  sampled trace edges; node colour = error rate, edge weight =
  call frequency. Click-through to per-service detail.
- **Alert rules + incidents** — configurable rules over RED
  metrics, auto-grouped into incidents using the built-in
  topological correlator (a payment-service timeout + an
  upstream api-gateway saturation alert end up in one incident
  the oncall drives end-to-end).
- **Anomaly detection** — log-pattern anomalies (curated
  regex fingerprints for Oracle errors, OOM, NPE, deadlocks,
  panics, etc.) + trace-op anomalies (per-endpoint error-rate
  spikes) + metric anomalies (z-score baseline). Snooze /
  silence with TTL.
- **SLOs** — availability + latency targets with error-budget
  burn rate computation.
- **Synthetic monitors** — HTTP probes + heartbeat ping URLs;
  state changes flow through the same notifier path as alert
  rules.

### Performance + scale

- **ClickHouse-first** — every read path is a CH query;
  no in-app indexing or rollup engine. Designed for billion-
  span/day with proper schema (LowCardinality columns,
  primary-key prefix, `tokenbf_v1` skip indexes on log body,
  bloom-filter on trace_id, MergeTree partitioning by day).
- **Distributed CH support** — the schema migrator emits
  `ON CLUSTER` DDL when `cluster_name` is set, creating
  `Replicated*MergeTree` tables on each shard plus a
  `Distributed` wrapper that fans out queries.
- **Async insert + parallel flushers** — 8-worker pool, 500k
  buffer, 2 s flush window. Consumer pushback via bounded
  channels.
- **Trace sampling** — head sampling with always-keep-errors
  + always-keep-roots + per-service ratios. Optional buffered
  tail sampling that decides keep/drop based on aggregate
  trace properties (error / slow / probabilistic).
- **uPlot frontend charts** — replaces Chart.js for ~10× the
  render speed, ~100 KB smaller bundle.
- **Live updates via SSE** — problem.open / problem.resolve /
  anomaly.* events push to the browser; React Query
  invalidates the matching cache. Sub-second alert-to-UI
  latency vs the typical 30-s polling lag.

### Enterprise

- **OIDC SSO** — Google, Microsoft, Okta, generic OIDC.
- **LDAP / Active Directory** — AD group → role mapping,
  recursive memberOf lookup, LDAPS + StartTLS, internal-CA
  paste-in.
- **RBAC** — admin / editor / viewer roles. Editor manages
  dashboards / monitors / alerts / incidents; admin owns
  user + system settings.
- **Audit log** — append-only, partitioned monthly, 365-day
  TTL. Filterable by actor / action / target.
- **Notification channels** — Slack, Microsoft Teams, generic
  webhook, email (SMTP), WhatsApp (Twilio). Per-channel min
  severity filter; channel CRUD + send-test from the UI.
- **AI Copilot** — explain-this-trace / explain-this-anomaly
  via Anthropic Claude or GitHub Copilot; key configured at
  runtime, hidden from the UI after save.
- **SQL playground** — admin-only direct ClickHouse query
  console with three-layer defence (role gate, app-level
  allow-list, CH `readonly=2` + 60-s exec cap).
- **Resource sampling settings UI** — adjust head + tail
  sampling ratios live without process restart.

---

## Quick start (docker-compose, evaluation)

Boots ClickHouse, Redis, OTel Collector, Coremetry plus a
Java + Go demo emitting realistic traffic.

```bash
git clone https://github.com/cilcenk/coremetry
cd coremetry
docker compose up -d
```

Web UI: `http://localhost:8088` — login with
`admin@coremetry.local` / `admin`. The demo apps populate
data within ~30 seconds; the SRE dashboards are pre-seeded.
OTLP endpoints for your own apps:

- gRPC: `localhost:4317`
- HTTP: `localhost:8088/v1/{traces,logs,metrics}`

---

## Production install (Helm)

The chart in `charts/coremetry` is the supported install
path. Bundles ClickHouse + Redis + the Coremetry app plus an
OTel Collector for ingest. External ClickHouse / Redis is
fully supported — point the chart values at them and disable
the bundled subcharts.

### Add the chart repository

The chart ships as an OCI artifact. From a Helm 3.8+ install:

```bash
helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry \
  --version <release-tag> \
  --create-namespace --namespace coremetry
```

Replace `<release-tag>` with a published release (see
[Releases](https://github.com/cilcenk/coremetry/releases)) or
omit `--version` to take latest.

### Or install from a checkout

```bash
git clone https://github.com/cilcenk/coremetry
cd coremetry
helm install coremetry charts/coremetry \
  --create-namespace --namespace coremetry \
  --set ingress.enabled=true \
  --set 'ingress.hosts[0].host=coremetry.example.com' \
  --set 'ingress.hosts[0].paths[0].path=/' \
  --set 'ingress.hosts[0].paths[0].pathType=Prefix' \
  --set secrets.jwtSecret="$(openssl rand -hex 32)"
```

### Pointing at an external ClickHouse cluster

```bash
helm install coremetry charts/coremetry \
  --set clickhouse.enabled=false \
  --set clickhouse.external.addr="ch1:9000,ch2:9000,ch3:9000,ch4:9000" \
  --set clickhouse.database="coremetry_prod" \
  --set clickhouse.username="coremetry" \
  --set secrets.clickhousePassword="<password>"
```

The driver round-robins / fails over across the seed list —
no upstream LB needed.

### Connect external apps

Inside the cluster the OTLP receiver lives at
`coremetry-otelcol:4317` (gRPC) or
`coremetry:8088/v1/{traces,logs,metrics}` (HTTP). Point your
apps' `OTEL_EXPORTER_OTLP_ENDPOINT` at it.

### Reset schema on a fresh deploy

For first-install convenience the chart can drop + recreate
the entire CH database before the app pod starts:

```bash
helm install coremetry charts/coremetry \
  --set clickhouse.resetSchema=true
```

The pre-install hook Job runs `coremetry --reset-schema`,
which `DROP DATABASE IF EXISTS` + lets the app's normal
startup migrations rebuild the schema. **Destructive** — only
use on first install. Flip back to `false` for upgrades.

### OpenShift

A flat manifest set lives in `examples/openshift/` for
clusters that don't run Helm. Restricted-v2 SCC compatible
(no `runAsUser` pin), TLS-edge `Route` for the Web UI.

### Deployment topology — monolithic vs distributed (v0.6+)

The same Coremetry image runs in two shapes, picked via a
single values key. Switch at any time with a rolling upgrade:

```yaml
# values.yaml
deployment:
  mode: monolithic     # default — one Deployment, COREMETRY_MODE unset
  # OR
  mode: distributed    # three Deployments: ingest + api + worker
  roles:
    ingest: { replicas: 5 }   # scale up for OTLP fan-in load
    api:    { replicas: 2 }   # scale up for read HA / RPS
    worker: { replicas: 1 }   # MUST be 1 — leader-elected jobs
```

| Topology | When to use |
|---|---|
| `monolithic` (default) | POC, SME installs, dev clusters. One pod, one container, every subsystem in-process. Matches every Coremetry release up to v0.5.x — no migration cost for existing users. |
| `distributed` | Production at billion-spans/day scale. Three Deployments running the same image with `COREMETRY_MODE=ingest|api|worker`. Ingest scales horizontally for OTLP load; api scales for read HA; worker stays singleton (alert evaluator, anomaly detector, topology aggregator are leader-elected via Redis lock and a single replica avoids the lock-contention overhead). |

Distributed mode requires Redis (the lock + SSE pub/sub
bridge for cross-pod event fan-out). Without it, worker-fired
`problem.opened` events wouldn't reach the api pods' SSE
streams; v0.6.3 wires that bridge so browsers connected to any
api pod see every event regardless of where it fired.

Same binary contract from v0.5.x: one image, one release
pipeline, one tag. Container runtime picks its role from a
single env var; the chart wires it from `deployment.mode`.

### Model Context Protocol (MCP) — external LLM access (v0.6.4+)

Coremetry exposes its telemetry as an [MCP](https://modelcontextprotocol.io)
server so Claude Desktop, the Anthropic API tool-calling flow,
and internal copilots can investigate incidents via a uniform
JSON-RPC protocol instead of bespoke REST integrations.

Three capability surfaces are advertised:

- **Tools** (v0.6.5) — invokable functions: `list_services`,
  `get_service_health`, `list_problems`, `list_anomalies`,
  `search_logs`, `get_trace`, `query_metric`. Each has a JSON
  Schema for args and reads from the same MV path the UI uses.
- **Resources** (v0.6.6) — URI-addressed read-only references:
  `coremetry://services`, `coremetry://problems/open`, and
  templated URIs like `coremetry://trace/{trace_id}` or
  `coremetry://service/{name}`.
- **Prompts** (v0.6.7) — curated system+user message templates
  that surface the in-app ✨ Explain workflows over MCP:
  `explain_trace`, `explain_problem`, `suggest_runbook`,
  `explain_service_health`, `explain_exception`. The renderer
  fetches the data + combines with the canonical system prompt
  so the LLM gets a complete conversation seed.

Transport: HTTP+SSE per MCP spec 2024-11-05. GET
`/api/mcp/sse` opens the stream, POST `/api/mcp/messages?
sessionId=…` carries JSON-RPC requests. Auth via the existing
JWT/cookie middleware — viewer/editor/admin roles carry into
MCP just like REST.

Multi-pod note: MCP sessions are pod-local. In distributed
deployments, add session affinity at the ingress (sticky
cookie keyed on `Mcp-Session-Id` or the original sessionId
query param) so reconnects land on the same pod.

---

## OpenTelemetry agents

Coremetry consumes the same OTLP wire format every OTel SDK
emits. Sample setups:

- **Java**: `OTEL_EXPORTER_OTLP_ENDPOINT=http://coremetry:4317
  OTEL_EXPORTER_OTLP_PROTOCOL=grpc OTEL_SERVICE_NAME=my-service`
  with the OTel Java auto-instrumentation agent.
- **Go**: `otel-go` SDK with the standard
  `otlptracegrpc.WithEndpoint` exporter setup.
- **Python**: `opentelemetry-instrument` CLI plus
  `OTEL_EXPORTER_OTLP_ENDPOINT` env.
- **Node.js / .NET**: equivalent `OTEL_EXPORTER_OTLP_*`
  env-var setup.

Resource attributes Coremetry surfaces in the UI:
`service.name` (required), `service.version` (deploy
tracking), `telemetry.sdk.language`,
`process.runtime.{name,version,description}` (runtime badge),
`host.name` / `os.type` (saturation panel host attribution).

---

## Configuration

Highest-impact knobs in `config.yaml` (all overridable via
`COREMETRY_*` env vars):

```yaml
listen:
  http: ":8088"
  grpc: ":4317"

clickhouse:
  addr: "ch1:9000,ch2:9000"     # comma-separated for cluster
  database: "coremetry"
  username: "default"
  cluster_name: ""              # set to enable Distributed CH
  secure: false                 # native TLS (port 9440)

retention:
  spans_days: 30
  logs_days: 30
  metrics_days: 7

ingestion:
  batch_size: 10000
  buffer_size: 500000
  flush_interval: 2s
  workers: 8

sampling:
  default: 1.0                  # head sampling ratio
  always_keep_errors: true
  always_keep_roots: true
  tail:
    enabled: false
    window_sec: 30
    slow_ms: 1000

auth:
  initial_admin: "admin@coremetry.local"
  initial_password: "admin"     # rotate after first login
  oidc:
    enabled: false
  # LDAP / AD configured live from Settings UI.

logs:
  backend: "clickhouse"         # or "elasticsearch"
```

See `internal/config/config.go` for the full schema +
defaults.

---

## Architecture

- **Single Go binary** (~30 MB statically linked) embeds the
  Vite SPA via `go:embed`. One artifact to deploy.
- **OTLP/gRPC + OTLP/HTTP** on the same listener config; the
  binary terminates both and writes to ClickHouse via the
  async insert path.
- **Background workers**: alert evaluator (1 min), anomaly
  detector (2 min), anomaly recorder (1 min), correlator
  (5 min), monitor runner (5 s), errors-inbox refresher
  (1 min), tail sampler sweeper (1 s when enabled). All
  leader-gated via Redis lock so HA replicas don't double-run.
- **HTTP/REST API** at `/api/...` (typed; see
  [`internal/api/api.go`](internal/api/api.go) for routes).
- **SSE event bus** at `/api/events` for sub-second push of
  problem / anomaly state changes to the browser.
- **Sub-binary `--reset-schema` mode** for the Helm
  pre-install hook + `--migrate-from <addr>` for one-shot
  bulk copy from a legacy CH cluster.

---

## Development

Backend (Go 1.25+):

```bash
go build ./... && ./coremetry --config config.yaml
```

Frontend (Node 22+):

```bash
cd frontend
npm install
npm run dev          # Vite dev server with /api proxied to localhost:8088
npm run build        # production build (TypeScript check + Vite bundle)
npm run build:analyze # bundle treemap → dist/bundle-analysis.html
```

Tests:

```bash
go test ./...
```

---

## License

[MIT License](LICENSE). Free for any use — commercial,
non-commercial, modification, redistribution. SigNoz-style
permissive licensing so banks and SaaS operators can ship
Coremetry into production without per-seat negotiations.

---

## Status

Active development — see
[Releases](https://github.com/cilcenk/coremetry/releases) for
the per-version changelog. Open issues + feature requests
welcome via GitHub Issues.
