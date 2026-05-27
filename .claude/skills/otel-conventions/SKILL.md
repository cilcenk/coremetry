---
name: otel-conventions
description: Coremetry's OpenTelemetry conventions guardrails — W3C trace-context propagator policy, semantic-conventions mapping (service.name / http.* / db.* / messaging.* / gen_ai.*), resource attribute "critical 5" hierarchy, OTLP gRPC vs HTTP endpoint rules, span duration + percentile naming, head + tail sampling decision points, EDOT vs raw OTel SDK acceptance. Use BEFORE any change that reads or writes OTel-shaped data — new ingest path, new receiver, new picker/aggregator, debugging "service doesn't show up" / "spans don't chain" / "metrics are misnamed". Triggers on words like "OTel", "OpenTelemetry", "OTLP", "trace context", "propagator", "semantic conventions", "service.name", "resource attribute", "span attribute", "EDOT", "OTEL_*", "B3", "Jaeger", "W3C tracecontext", "head sampling", "tail sampling".
---

# /otel-conventions — Coremetry OTel conventions

Coremetry is OTel-native: the source of truth for traces, logs,
metrics, and profiles is the OTLP wire format. Every internal
table column maps back to an OTel attribute or resource
attribute. When ingest, query, or UX gets a convention wrong
(propagator mismatch, missing service.name, percentile
nomenclature drift), the entire downstream story breaks.

This skill captures the OTel rules Coremetry agrees with and the
small set it deviates from (always documented with a reason).

**Read this before any change that touches OTel-shaped data.**

## When to use

- Adding or modifying `internal/otlp/*.go` (ingest path)
- Adding a new receiver / converter (`internal/otlp/convert.go`)
- Touching span / log / metric field names in `internal/chstore/`
- Writing a frontend picker / chart query that reads OTel attrs
- Debugging "my Python service doesn't show up"
- Debugging "spans don't chain across services"
- Adding a new alert rule shape that reads span attributes
- Reviewing operator-reported "wrong metric name" / "wrong unit"
- Adding new MCP tool / prompt that exposes OTel data

## Steps

### 1. W3C Trace Context — the ONLY propagator

Coremetry's `OTEL_PROPAGATORS` policy across the bundled demos
and recommended SDK configs:

```
OTEL_PROPAGATORS=tracecontext,baggage
```

**Why explicit-only:**

- W3C Trace Context (`traceparent` / `tracestate` headers) is
  the default in OTel SDKs ≥ 1.0 — but mixing in B3 / Jaeger
  across services breaks span chaining silently (one service
  emits B3, the next reads only tracecontext → trace splits
  into orphans).
- Baggage carries user-defined context across services (request
  id, deploy env). NEVER strip the propagator if any service
  uses baggage.

**Anti-patterns:**

- B3 (`b3` or `b3multi`) — common in legacy Spring Cloud
  Sleuth / Brave installs. If a bank's stack uses these, the
  fix is on the SDK side (switch to tracecontext); don't add
  B3 receivers to Coremetry.
- Jaeger (`jaeger`) — Uber's pre-OTel format. Same story:
  migrate the SDK, don't accept the format in ingest.

If an operator reports orphan spans / broken trace chains,
**first check `OTEL_PROPAGATORS` on every service in the chain**.
One mismatch breaks everything downstream.

### 2. The "critical 5" resource attributes

Every ingested signal MUST carry these on the Resource (set
once per process, NOT per span / log / point):

| Attribute | Source | Coremetry usage |
|---|---|---|
| `service.name` | SDK config (`OTEL_SERVICE_NAME`) | Primary partitioning key on every table |
| `deployment.environment` (or `resource.deployment.environment`) | SDK config | Picker filter; problem priority hint |
| `service.version` | SDK config | Deploy markers; before/after-release diff |
| `host.name` (or `resource.host.name`) | OTel host detector | Infra correlation; CPU / mem pivots |
| `service.instance.id` | OTel resource detector | Per-replica drill-down |

**When operator reports "service doesn't show up":**

1. Is `service.name` set? (Often the SDK didn't pick up
   `OTEL_SERVICE_NAME` — defaults to `unknown_service:<binary>`.)
2. Is the OTLP endpoint pointing at Coremetry's gRPC or HTTP?
   (Default gRPC 4317, HTTP 4318. Coremetry exposes both.)
3. Is there a recent span / log / point in the relevant table?
   `SELECT count() FROM spans WHERE service_name=? AND time>=now()-INTERVAL 5 MINUTE`.

Coremetry's coalesce chain for k8s/openshift cluster (resource
attrs vary by SDK):

```
coalesce(k8s.cluster.name, openshift.cluster.name, cluster, '')
```

When adding a new attribute that has multiple SDK-emitted names,
use the same coalesce pattern at read time — don't enforce one
name at ingest.

### 3. Semantic conventions Coremetry honors

Coremetry's internal column names map to OTel semconv. **Don't
rename them** — picker/chart queries depend on the stability.

| OTel attribute | Coremetry column | Notes |
|---|---|---|
| `http.method` | `http_method` (LowCardinality) | server + client kind |
| `http.route` | `http_route` (LowCardinality) | parameterised path |
| `http.status_code` | `http_status` (UInt16) | numeric, not string |
| `db.system` | `db_system` (LowCardinality) | postgresql / mysql / mongodb |
| `db.statement` | `db_statement` (String, ZSTD) | full text |
| `rpc.system` | `rpc_system` | grpc / connect / dubbo |
| `rpc.method` | `rpc_method` (LowCardinality) | |
| `messaging.system` | `msg_system` (LowCardinality) | kafka / rabbit / sqs |
| `peer.service` | `peer_service` (LowCardinality) | for topology edges |
| `gen_ai.operation.name` | (not stored yet) | LLM observability — see step 8 |

**When OTel adds a new semconv attribute** (e.g.,
`http.request.body.size`, `process.runtime.name`), and operators
ask for filtering on it:

1. Check whether it already appears in `attr_keys/attr_values`
   (Array(LowCardinality(String))) — it might be there as
   key/value pair already.
2. If filter cardinality is high (>1k distinct values per
   service), add a typed column. Otherwise, leave it in
   attr_keys + use the FilterBuilder's "any attribute" search.

**Don't promote every new semconv attribute to a column.**
Schema churn at billion-row scale is expensive (per the
clickhouse-schema skill's "ORDER BY is immutable" rule).

### 4. OTLP gRPC vs HTTP — both, no preference

Coremetry accepts both:

- **OTLP/gRPC** on 4317 (`internal/otlp/StartGRPC`) — primary
  for SDK fleet ingestion. Lower overhead per message.
- **OTLP/HTTP** on 8088 (mux'd into the API server) at
  `/v1/traces`, `/v1/logs`, `/v1/metrics`, `/v1/profiles`.
  Easier for browser-side / Lambda / serverless that can't
  do long-lived gRPC streams.

**SDK config recommendations** (in docs / java-demo /
go-demo / jboss-demo):

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://coremetry:8088     # for HTTP
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
# OR
OTEL_EXPORTER_OTLP_ENDPOINT=http://coremetry:4317     # for gRPC
OTEL_EXPORTER_OTLP_PROTOCOL=grpc
```

The Collector forwards either to the right Coremetry port.
Reading `coremetry-otel-collector:14317` vs direct varies by
deploy topology — both work.

**Don't add a third protocol.** Jaeger UDP, Zipkin HTTP,
OpenTracing wire format — all out of scope. Migrate the SDK.

### 5. Span duration + percentile naming

Internal:
- `duration` = nanoseconds (Int64, T64 + ZSTD codec)
- `duration_ms` = float64 milliseconds (computed at read time:
  `duration / 1e6`)

UI / aggregate APIs expose milliseconds. Operators think in ms;
nanoseconds in the API would force every dashboard panel to
divide manually.

**Percentile naming** (locked across MVs + alert rules + UI):

| Code | UI label | Description |
|---|---|---|
| `p50_ms` | "P50" | median latency |
| `p95_ms` | "P95" | tail latency |
| `p99_ms` | "P99" | high-tail (default for SLOs) |
| `avg_ms` | "Average" | mean — affected by outliers; rarely useful at scale |

**Don't add `p999_ms` or `p9999_ms` as new MV columns.** Beyond
p99, the quantilesState() merge inflates state size sharply and
the precision drops. If a SLO needs p999, compute it on raw spans
with a bounded window.

### 6. Head + tail sampling decision points

Coremetry samples at TWO points:

1. **Head sampling** — `internal/sampling/Sampler.Decide(span)`
   — fires when an OTLP span arrives. Default `keepErrors=true,
   keepRoots=true`. Probabilistic sampling for the rest.
2. **Tail sampling** — `internal/sampling/Tail.Add(span)` —
   buffers complete traces, decides after the trace closes
   (root finishes). Tail sampler's `AttachFlush` returns kept
   spans through the SAME consumer the head path uses.

**When adding a new "keep these spans" rule:**

- If the rule depends on a single span's attribute (status_code,
  http.method): add to head sampler.
- If the rule depends on the WHOLE TRACE (any span erred, total
  duration > threshold, contains a specific operation): add to
  tail sampler.

**Don't reach for tail sampling for everything.** Tail buffers
the entire trace in memory until close + N seconds — at high QPS
the buffer dominates memory. Head sampling is cheaper per span
and covers 90% of "keep spans we care about" rules.

### 7. EDOT vs raw OTel SDK — both acceptable

Coremetry doesn't push a particular SDK distribution. Operators
can use:

- **Elastic Distribution of OTel (EDOT)** — zero-code agent
  attachment (Java/Python/.NET). Good for "give me traces with
  no app change" requests.
- **Raw OTel SDK** — language-native SDKs from
  `opentelemetry.io`. Standard. Full control over exporters /
  processors / samplers.

Both speak OTLP, both work. Coremetry's docs show EDOT for the
zero-code path AND raw OTel for control. Don't force operators
into one.

**When debugging "trace from EDOT looks different from raw":**

- EDOT may set additional resource attributes (`agent.name=
  elastic-otel-javaagent`, `elastic.distro.version=...`).
  Filter them out in the picker if they create noise; don't
  reject the data.
- Both should produce the same span shape (kind, name, duration,
  attributes). If they differ, the SDK has a bug, not Coremetry.

### 8. gen_ai.* semantic conventions — LLM observability ready

OTel ships a draft semantic convention for AI/LLM
instrumentation:

| Attribute | Meaning |
|---|---|
| `gen_ai.operation.name` | "chat", "completion", "embedding" |
| `gen_ai.system` | "openai", "anthropic", "vertex_ai" |
| `gen_ai.request.model` | model id (gpt-4, claude-opus) |
| `gen_ai.usage.input_tokens` | prompt token count |
| `gen_ai.usage.output_tokens` | completion token count |

**Coremetry's current stance:** ingested as generic span attrs
(land in `attr_keys/attr_values`). The Copilot wrapper at
`internal/api/ai_observability.go::copilotExplain` writes its
OWN `ai_calls` row that captures the same info — internally
typed, not from OTel.

**When an SDK starts emitting gen_ai.* on spans:**

1. The data is already queryable via FilterBuilder ("any
   attribute" → `gen_ai.request.model = ?`).
2. If operators ask for LLM-specific dashboards, the gen_ai
   attrs are first-class — don't ingest into a separate path.
3. Cost / token aggregation is NOT in the OTel draft yet
   (Coremetry's `ai_calls` extends it). Watch the spec; align
   when it stabilises.

### 9. Coremetry-specific deviations (documented)

These are intentional differences from the OTel standard.
Document them clearly when surfaced:

1. **Span events compressed inline.** OTel spec says events
   are first-class on a span (each with timestamp + attrs).
   Coremetry stores them as a JSON-encoded `events String`
   column to avoid a separate spans_events table. Reads use
   JSONExtract*; at scale the inline storage wins on partition
   pruning + scan cost.
2. **Resource + span attributes merged in queries.** The OTel
   spec maintains them as separate scopes. Coremetry's
   FilterBuilder treats them uniformly (filter on
   `resource.k8s.pod.name` AND `http.method` in the same
   filter list). Operators want this; the spec is too pedantic
   here.
3. **Profile pprof endpoint** at `/v1/profiles` — OTel
   profiling spec is still experimental. Coremetry accepts
   raw pprof + a thin metadata wrapper. When the spec
   stabilises, Coremetry will pivot.
4. **Logs `body` is a String, not the OTel-spec'd AnyValue.**
   The spec allows structured log bodies (maps, arrays).
   Coremetry stores the stringified form + uses attr_keys /
   attr_values for the structured portion. Saves a recursive
   AnyValue serdes hot path at billion-log scale.

### 10. Diagnostic flow for "OTel data looks wrong"

When operator reports any of:
- "service doesn't show up"
- "spans don't chain across services"
- "metric name has weird suffix"
- "log timestamps look off"

Run this checklist in order:

1. **Is the SDK configured?** `OTEL_SERVICE_NAME`,
   `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`.
2. **Is the propagator consistent?** Same `OTEL_PROPAGATORS`
   value across every service in the chain.
3. **Is the OTLP receiver healthy?** `curl http://coremetry:
   8088/v1/traces -X POST -d '{}'` should 415 (wrong
   content-type) not 404 / 500.
4. **Are spans landing in CH?** `SELECT count() FROM spans
   WHERE service_name=? AND time>=now()-INTERVAL 5 MINUTE`.
5. **Is the head sampler dropping them?** Check
   `/api/health` for `spans_dropped` rate. If non-zero +
   `keepErrors=true`, the operator's spans aren't erroring +
   aren't roots + got probabilistically sampled out.
6. **Is the pipeline (v0.5.263) filtering them?** Check
   /admin/pipeline rules — a "drop spans where service=X" rule
   could silently kill ingest.

## Anti-patterns

- **Adding B3 / Jaeger / Zipkin receivers.** Coremetry is
  OTLP-only by design. Migrating SDKs is the right answer.
- **Renaming a typed column to "match OTel name better".**
  Picker queries + frontend types + saved views depend on the
  current names. Don't churn.
- **Promoting every new OTel attribute to a typed column.**
  attr_keys / attr_values handles it without schema migration.
- **Adding `OTEL_TRACES_EXPORTER=otlp` env to docs.** Modern
  OTel SDKs default to OTLP — explicit setting is noise and
  becomes wrong when the SDK switches default exporters.
- **Ignoring `OTEL_PROPAGATORS=tracecontext,baggage` in new
  demos / docs.** The default depends on the SDK version; pin
  it explicitly so the demo trace always chains.
- **Storing `gen_ai.*` in a parallel side-channel table.** The
  attrs are already on the span; query through the existing
  spans table. Side-channel = drift between Copilot view and
  Trace view.
- **Forcing EDOT or raw SDK.** Banks have legacy + greenfield
  services. Coremetry must accept both.

## Hard-constraint reminders

- **OTLP is the source of truth.** Spans, logs, metrics enter
  via OTLP/gRPC + OTLP/HTTP. No proprietary ingest path.
  (CLAUDE.md invariant #1.)
- **Resource + span attributes kept verbatim.** Operators
  query them later via FilterBuilder. Don't strip "useless"
  ones at ingest.
- **Cluster filter coalesce chain** for k8s/openshift —
  `coalesce(k8s.cluster.name, openshift.cluster.name,
  cluster, '')`. Same shape for any future multi-name attr.

## Historical incidents — read before guessing

- **v0.5.471** — added `cluster` filter to logs path because
  k8s.cluster.name / openshift.cluster.name / cluster were
  resolving inconsistently across SDKs. Coalesce chain pattern
  was set here; copy it for any new multi-name attr.
- **v0.5.263** — ingest-time pipeline engine added (drop /
  enrich rules evaluated BEFORE the sampler). Reduced span
  volume 30%+ on installs with chatty `gae_app` etc. spans.
  When debugging "spans missing", check pipeline rules first
  before suspecting the sampler.
- **v0.5.208** — Tempo external trace backend as a fallback.
  OTel spans land in CH at low sampling, Tempo gets 100%
  retention. `/trace/{id}` resolves CH first, Tempo second.
  Lesson: Coremetry's sampling rate is independently tunable
  from the long-tail-trace story.
- **v0.5.244** — Drain templater is sample-based on purpose;
  full-scan templating at billion-log scale is unworkable.
  Don't re-derive without reading the v0.5.244 commit msg.
- **v0.5.346** — async_insert tuning to current values for
  ingest. Don't churn.
- **v0.5.394** — `COREMETRY_VERSION` env override removed —
  stale env value silently masked the actual build tag.
  Lesson: don't add overrides for things the binary should
  self-report.

## Don't

- **Don't add a non-OTLP ingest path.** OTLP gRPC + HTTP only.
  Use the OTel Collector for protocol bridging if needed.
- **Don't strip resource attributes at ingest.** They're the
  operator's filter affordance. attr_keys/attr_values handles
  the open-ended ones; typed columns hold the named ones.
- **Don't add an OTel attribute as a typed column without
  checking cardinality.** > 10k distinct values per service =
  attr_keys/attr_values lane.
- **Don't break propagator consistency in docs.** Every demo /
  example SDK setup MUST include `OTEL_PROPAGATORS=
  tracecontext,baggage`.
- **Don't follow the OTel spec literally when it costs
  scale.** The four documented deviations (inline span events,
  merged attribute scopes, stringified log body, pprof
  acceptance) exist for a reason. Re-deriving them in the
  spec's direction = re-introducing the cost.
