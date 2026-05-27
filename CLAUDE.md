# Coremetry

OpenTelemetry-native APM. Single Go binary + ClickHouse + Redis +
optional external Elasticsearch/Tempo. Runs at 1000s of services,
10000s of operations, 1B+ spans/day. Designed to land in an
operator's stack as one container, not a kubernetes opera.

When in doubt, the rubric is **"how would Datadog / Dynatrace /
Honeycomb engineer this?"** — that's the bar. Operator-facing UX
should feel familiar to an engineer who's used one of those for
five years.

---

## Hard constraints — non-negotiable

| Constraint | Why | Enforced where |
|---|---|---|
| **Single-binary release** | One image, one tag, one release pipeline. `COREMETRY_MODE=all\|ingest\|api\|worker` (v0.6.0) lets that single binary act in different roles for scale-out, but it's still one binary. | `main.go`, `Dockerfile` |
| **Picker = server-side search** | 10k+ ops can't ride a `<Combobox options={…}>`. | `ServicePicker` / `OperationPicker` / `MetricNamePicker` |
| **Table > 100 rows** | Virtualize, paginate server-side, OR `content-visibility:auto`. | `globals.css`, every list page |
| **Cache key hashes ALL inputs** | Length-only digests cross-poison (v0.5.187 incident). Use sorted + FNV. | `internal/api/cache.go` |
| **`timeRangeToNs(range)` inside `useEffect`/`useMemo`** | Bare call in JSX = `now()` ticks every render = infinite refetch (v0.5.184). | every page using `range` |
| **ClickHouse query on `spans` / `metric_points`** | Must have `LIMIT`, `SETTINGS max_execution_time`, time-bounded WHERE on indexed column. | `internal/chstore/*.go` |
| **Admin write = audit entry** | `s.audit(r, "kind.action", "resource", id, details)` per state mutation. | `internal/api/*.go` |
| **No PII redaction features** | Operator preference — full fidelity > theoretical safety. | Memory `feedback-no-redaction.md` |

---

## Performance budgets

- `/api/*` p99 < 200ms warm, < 1s cold
- Hot endpoints (`/api/services`, `/api/problems`, `/api/health`) p99 < 50ms warm
- `/api/spans/heatmap` < 3s for ≤6h window; auto-sample beyond
- `/api/logs/patterns` (ES significant_text) < 2s at billion-doc index
- TTFI on first paint < 1.5s on a fresh tab
- Polling cadence ≥ 10s for everything except `/api/health` (5s)
- Every polling component pauses on `document.hidden`

---

## Tech stack

**Backend:** Go 1.22+, ClickHouse 24+, Redis 7+ (cache/lock).
Single binary `main.go` wires `chstore.Store` (CH), `logstore.Store`
(CH or ES — selected via `COREMETRY_LOGS_BACKEND`), `tempo.Service`
(optional trace fallback), `cache.Cache` (Noop or Redis),
`auth.Service`, `notify.Notifier`, `copilot.Service`.

**Frontend:** React + Vite, no Tailwind (CSS variables in
`globals.css`). React Query for fetch+cache, react-router-dom v6
for routes. No state management library — components own state +
URL is source of truth for shareable views.

**Versioning:** git tags `v0.5.X`. Resolution order for runtime:
`COREMETRY_VERSION` env > `-X main.Version=` ldflag > `/app/VERSION`
file > `"dev"`.

---

## Architectural invariants

1. **OTel is the source of truth.** Spans, logs, metrics enter via
   OTLP/gRPC + OTLP/HTTP. No proprietary ingest path. Resource +
   span attributes are kept verbatim — operator queries them later.

2. **ClickHouse is the warm store.** Spans / metric_points / logs
   land here always; ES is an optional *read* backend for logs only
   (write side is CH). Aggregation tables (`*_summary_5m`,
   `topology_*_5m`, `db_*_5m`) pre-compute the dashboards' hot path.

3. **Every read endpoint that touches CH must use the MV when one
   exists.** `service_summary_5m`, `operation_summary_5m`,
   `topology_edges_5m`, `db_summary_5m`, `db_caller_summary_5m`,
   `topology_root_flows_5m`. Reading raw `spans` for an aggregate
   query at billion-row scale is a bug.

4. **`ReplacingMergeTree(version)` for state.** alert_rules,
   problems, system_settings, saved_views, anomaly_events,
   log_templates. Reads always use `FINAL`. Version column ends
   the dedup story without operator-visible delete propagation
   delay.

5. **One CH table for saved state.** `saved_views(page='<kind>',
   id, owner_id, query_string, …)` is the catch-all for alert
   presets, topology views, dashboards, etc. Don't add a new
   schema per surface.

6. **Settings live in `system_settings`.** AI Copilot creds, LDAP
   config, branding, Tempo backend, Kibana URL, sampling — all
   one key/value table, JSON blob per key. The service that owns
   the config struct does `LoadPersisted(ctx, store)` at boot +
   `SavePersisted(...)` on admin PUT.

7. **Permission roles: admin / editor / viewer.**
   - admin = Settings tab + destructive actions
   - editor = rule/preset/sampling edits + per-row state changes
   - viewer = read-only everywhere
   `auth.RequireRole(auth.RoleAdmin, h)` or
   `auth.RequireAnyRole(editorRoles, h)`. Frontend buttons
   hide/disable based on `user.role`; viewer still SEES state
   (read-only chip), not blank.

---

## When you ship a new feature

Every new operator-facing surface must include:

1. **Backend handler** that hits the right MV (not raw spans)
2. **Cache wrapper** via `s.serveCached(w, r, key, ttl, fn)` with
   a key that hashes ALL inputs (sorted + FNV for sets)
3. **Auth gate** on the route if it writes state
4. **Audit entry** via `s.audit(r, ...)` if it writes state
5. **Settings persistence** if the operator can configure it —
   reuse `system_settings` + the `LoadPersisted`/`SavePersisted`
   pattern from `tempo` or `copilot`
6. **Frontend type** in `lib/types.ts`
7. **Frontend client** method in `lib/api.ts`
8. **Loading + error + empty states** — never render a blank
   panel; use the shared `<Spinner/>` and `<Empty/>` components
9. **TypeScript** is law — `cd frontend && npx tsc --noEmit` is
   the gate
10. **Backend** is law — `go build ./...` is the gate
11. **Regression test for bug-fixes** (v0.5.447) — every
    `v0.5.X — bug-fix` release ships with a Go test that would
    catch the bug if it re-regresses. Pattern: extract the
    minimal pure function the fix touches; table-driven test
    in `<package>/<feature>_test.go`; comment header cites the
    v0.5.X release and explains the original symptom. See
    [internal/api/cache_key_test.go](internal/api/cache_key_test.go)
    (v0.5.187 collision) for the canonical example. Pre-tag
    gate: `go test ./...` must pass.

If it has an AI explain affordance: route through
`s.copilotExplain(r, ...)` wrapper, NOT `s.copilot.Explain` direct.
The wrapper writes the ai_calls row for /ai page attribution.

---

## Code patterns

### Pickers
```tsx
// YES — server-debounced
<ServicePicker value={svc} onChange={setSvc} placeholder="…" />

// NO — eager catalogue load
<Combobox options={allServices} value={svc} onChange={setSvc} />
```

### Tables > 100 rows
```tsx
// YES — content-visibility lets the browser skip off-screen rows
<tr style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
```

### Cache key for a set
```go
// YES — stable digest
key := fmt.Sprintf("topology-exclude:%x", fnvDigest(sortedSlice(set)))

// NO — length-only collapse
key := fmt.Sprintf("topology-exclude:n=%d", len(set))
```

### CH query bounds
```sql
-- YES
SELECT … FROM spans
WHERE service_name = ? AND time >= ? AND time <= ?
LIMIT 1000
SETTINGS max_execution_time = 30

-- NO — unbounded scan
SELECT count() FROM spans GROUP BY service_name
```

### Setting persistence
```go
// Service holds an http.Client + cfg behind a RWMutex.
// LoadPersisted hydrates from chstore at boot, SavePersisted
// writes on admin PUT and updates the live config atomically.
// See internal/tempo/client.go for the template.
```

### Settings save modal (admin-only)
```tsx
// Pattern: Settings.tsx tab → form → api.putX() → show ok/err.
// Token-style secrets: never echo back; "stored" indicator;
// empty input preserves the previously stored value (rotate by
// pasting a new one).
```

### Logstore plurality
Detectors / pattern queries (curated regex, Drain, log alerts) go
through `logstore.Store.CountPatterns(...)` batched form — ES
backend uses `_msearch` for a single round-trip; CH iterates
behind the tokenbf_v1 skip index. Never call ES directly from a
detector — that ties the detector to one backend.

### Topology / inbox triage hierarchy
- **P1** = handle now (critical + 2x threshold OR critical +
  fresh deploy OR critical open ≥ 4h)
- **P2** = handle today (critical default OR warning + same
  signal multipliers)
- **P3** = handle when convenient (steady warnings, info)
Reason string ships with every Problem so the operator sees the
rule.

---

## Workflow

### Daily

- `kuyruk` = "show the queue" — agent presents numbered prioritised
  pending items, ends with "Hangisi?".
- `devam` = continue current in-progress item.
- Operator-reported bugs are NEW priority — fix as `v0.5.X+1`
  immediately after the current release, don't batch.

### Release pattern — every functional change

```
 1. Edit code.
 2. cd frontend && npx tsc --noEmit  (frontend changes only)
 3. go build ./...                    (backend changes)
 4. go test ./...                     (bug-fix releases especially)
 5. make audit                        (hard-constraint lint; v0.5.446)
 6. git add <touched files>
 7. git commit -m "<heredoc — see format below>"
 8. git tag v0.5.X
 9. git push && git push --tags        ← triggers Release workflow
10. make docker-up                     (background — one at a time)
```

`make audit` exits 1 on 🔴 critical findings (cache-key length
anti-pattern, eager Combobox, direct copilot.Explain, non-GLOBAL
IN over Distributed). Don't tag until critical is clean. 🟡
warnings (setInterval without document.hidden, FROM spans
without nearby LIMIT) print but don't block — review the list,
ship if known false positive.

### Commit message — multi-line heredoc

```
v0.5.X — short title (≤ 70 chars)

Body: what changed, why, root cause if a bug fix. Wrap at 72 cols.
Operator-reported bugs start with "Operator-reported: …".

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
```

### Background rebuilds
`make docker-up` may run in the background. ONE at a time. Wait
for the previous to finish before starting the next (concurrent
builds steal docker layer cache + serialize via BuildKit anyway).

### Quarterly
- `/scale-audit` skill — production regression catcher across 7
  axes (pickers, tables, polling, cache keys, CH bounds, render
  traps, permission gating). Quarterly cadence OR after a
  multi-week feature sprint.

### Skills directory (`.claude/skills/`)

Project-local Claude Code skills pinned in the repo. Each
captures Coremetry-specific judgement that generic Claude
guidance would miss.

| Skill | Use when |
|---|---|
| `/release` | Cut a release — next v0.6.X tag, commit, push, rebuild |
| `/bugfix` | Operator-reported bug — investigate, fix as v0.6.X+1, ship immediately |
| `/spec` | Idea → implementation plan before edit phase. Use for any change spanning 3+ files |
| `/kuyruk` | "What's next" — shows prioritised pending items, ends with "Hangisi?" |
| `/scale-audit` | Quarterly perf regression sweep across 7 axes |
| `/clickhouse-schema` | BEFORE any change to a CH table, query, or MV. Captures engine-choice, MV bypass invariant, ORDER BY rules, async_insert tuning, migration checklist, anti-patterns. (v0.6.23) |
| `/helm-chart-coremetry` | BEFORE any change under `charts/coremetry/`. OpenShift restricted-v2 SCC, global.imageRegistry air-gap rewrite, deployment.mode monolithic/distributed (v0.6.2), MCP/SSE session affinity (v0.6.21), Route vs Ingress, version-bump rules. (v0.6.25) |
| `/otel-conventions` | BEFORE any change that reads or writes OTel-shaped data. W3C tracecontext-only propagator policy, semconv → column mapping, critical-5 resource attrs, head + tail sampling decision points, EDOT vs raw SDK acceptance, gen_ai.* readiness, OTel-spec deviations documented. (v0.6.25) |
| `/mcp-tools` | BEFORE adding a tool / resource / prompt to the MCP server in `internal/mcptools/`. range_s convention, clampLimit caps, description-as-contract style, args↔JSON-Schema mirror, auth + audit gating, tools-vs-resources-vs-prompts triage. (v0.6.40) |

---

## Performance pitfalls — historical incidents

Each entry references the incident-shaped fix. Avoid re-living
them.

- **`timeRangeToNs(range)` in JSX / IIFE on every render** —
  re-evaluates `now()`, breaks `useEffect` dep equality, infinite
  refetch (v0.5.184). Always `useMemo([range])` or call inside
  `useEffect` body where deps are explicit.
- **Cache key = `len(set)`** — cross-set poisoning where two
  different sets sharing the same cardinality return each
  other's data (v0.5.187). Stable digest required.
- **`table-layout: fixed` + `white-space: nowrap` + small fixed
  width** — silently clips text. Use `min-width` + `max-width`
  + `ellipsis` + `title` attribute for tooltip.
- **ES `query_string` with `case_insensitive: true`** — rejected
  by ES 8.x as an unknown field (v0.5.231). Don't add it back.
  Standard analyzer already case-folds.
- **Per-pattern `_search` over N curated patterns** — N
  round-trips. Use `_msearch` for a single coordinator fan-out
  (v0.5.241).
- **`significant_text` without `background_filter`** — ES
  defaults the background to the whole index = catastrophic on
  billion-doc indices. Always cap baseline window AND wrap in
  `sampler` to bound per-shard scoring (v0.5.243).
- **Drain-style templating against raw logs at billion-row
  scale** — sample-based puller (1000/5min) > full scan. Sample
  bias on rare templates is fine because the curated detector
  + significant_text panel pick those up (v0.5.244 architecture
  note).
- **Polling without `document.hidden`** — burns mobile/laptop
  battery + idle API trafficky. See PublicStatus.tsx pattern.
  Always:
  ```js
  setInterval(() => { if (!document.hidden) fetchOnce(); }, 30_000);
  ```
- **Unit-mixing in SQL/time templates (`toDate(time) + INTERVAL %s`
  with `%s` ∈ {"30 DAY", "1 HOUR"})** — `toDate(time) + INTERVAL 1
  HOUR` = midnight + 1h = 01:00 of the SAME day, not "1 hour from
  the row's time". v0.6.36: retention.spans = "1h" via admin UI
  silently TTL'd every span after 01:00 — operator saw inconsistent
  /traces counts because merges ran intermittently. Rule: ANY
  template that accepts a value+unit (Nh/Nd, ms/s, MB/GB) MUST
  have a table-driven test exercising **every** unit at ship time.
  For sub-day TTLs use `<col> + INTERVAL N HOUR` (row-level); for
  day TTLs use `toDate(<col>) + INTERVAL N DAY` (partition-aligned).
  Never let `toDate()` wrap a sub-day calculation. See
  [internal/chstore/retention_test.go](internal/chstore/retention_test.go)
  for the canonical example.

---

## Self-observability

Coremetry observes itself. The `/admin/stats` page reads
`GetSystemStats(ctx)` — meta-observability snapshot of every
ingest queue's accepted counter, Redis hit rate, CH connection
pool, and per-endpoint hit rate. AI usage is recorded in
`ai_calls` and surfaces on `/ai` (admin only).

When adding a new ingest path or expensive endpoint, register a
counter so the operator can see whether it's healthy without
SSH'ing into the container.

---

## What goes WHERE

| Domain | Path |
|---|---|
| OTLP ingest | `internal/otlp/` |
| Span / metric / log CH writes | `internal/chstore/` |
| External log backend (ES) read | `internal/logstore/` |
| External trace backend (Tempo) | `internal/tempo/` |
| HTTP API (handlers + cache wrapper) | `internal/api/` |
| Alert evaluator | `internal/evaluator/` |
| Anomaly detectors (curated + recorder) | `internal/anomaly/` |
| Drain log templater | `internal/templater/` |
| Notification fan-out (channels) | `internal/notify/` |
| Auth (local + LDAP + OIDC) | `internal/auth/` + `internal/ldap/` |
| AI Copilot wrapper + system prompts | `internal/copilot/` |
| Sampling | `internal/sampling/` |
| Topology batch correlator | `internal/topology/` |
| ClickHouse migrations | `internal/chmigrate/` |
| Frontend pages | `frontend/src/pages/` |
| Frontend components | `frontend/src/components/` |
| Frontend types + API client | `frontend/src/lib/{types,api}.ts` |
| Frontend route registry | `frontend/src/App.tsx` |
| Frontend sidebar registry | `frontend/src/components/Sidebar.tsx` |

AI Copilot system prompts: `internal/copilot/copilot.go` BOTTOM
of file. Every Copilot endpoint routes through
`s.copilotExplain(r, ...)` — never `s.copilot.Explain` direct
(/ai attribution depends on the wrapper).

---

## Frontend type discipline

`frontend/src/lib/types.ts` is the single source of truth for
data shapes shared between API client and components. Don't
re-declare types in components — import from `lib/types.ts`. If
the backend adds a field, the type goes here first, then the
consumer reads it.

Naming: PascalCase for types, camelCase for properties (Go
struct tags do the JSON conversion). Optional fields use `?:`
when the backend can omit them (`omitempty` Go tag) and a
`unknown`-typed field for genuinely unknown shapes that the
component will narrow.

---

## Anti-patterns (don't)

- Don't bypass `s.serveCached` — every hot read goes through it.
- Don't create new schema for user-saved state (use `saved_views`).
- Don't fetch full catalogues for pickers.
- Don't add a polling interval shorter than 10s on anything except
  `/api/health`.
- Don't write code that ignores `document.hidden`.
- Don't add backwards-compat shims when removing a feature.
- Don't propose data redaction features (operator preference).
- Don't fix typing by `as any` — fix the root cause.
- Don't write `// TODO` without a release tag context; either ship
  the fix or queue it via `kuyruk`.
- Don't add a metrics counter without thinking about its
  cardinality — `LowCardinality(String)` for high-cardinality
  dimensions, not `String`.

---

## Decision log — recent architectural calls

- **v0.5.208** — Tempo external trace backend as a *fallback*,
  not a replacement. Coremetry samples at low rate, Tempo holds
  100%, fallback resolves the long-tail trace-by-id.
- **v0.5.210** — P1/P2/P3 priority score blended at READ time
  (no extra column). Persisted is wasteful; fresh recompute is
  cheap and lets fresh deploys/threshold ratios re-rank
  instantly.
- **v0.5.220** — Local monolithic Tempo + 30/100 collector split
  for POC. Operators replicate this layout in prod (sample to
  Coremetry, 100% to Tempo).
- **v0.5.226 / v0.5.235** — Faceted sidebar shipped THEN dropped
  at billion-doc scale because top-10 terms aren't useful when
  the operator's value is in the long tail. Replaced with
  click-from-row filter (still in place) + significant_text +
  Drain templates.
- **v0.5.241** — Log-pattern detector consumes `logstore.Store`,
  not raw `chstore`. Decoupled detector from CH-only path so
  ES-backed installs get coverage too. ES backend batches via
  `_msearch`.
- **v0.5.244** — Drain templater is sample-based on purpose.
  Three-layer log anomaly cover: curated regex (high-priority
  known failures) + significant_text (rare tokens, unsupervised)
  + Drain templates (full shape clustering, sample-based).
- **v0.5.246-247** — Topology op view + service view share the
  same NODE / COL / ROW constants + orphan handling so the
  operator's eye doesn't recalibrate when switching tabs.
- **v0.6.0** — `COREMETRY_MODE` env var lets the single
  binary run in four roles: `all` (default, monolithic POC),
  `ingest` (OTLP receivers + CH writers), `api` (HTTP API +
  SSE + Copilot), `worker` (evaluator + anomaly + topology
  agg + notifier; replicas=1 — leader-elected). Preserves the
  single-binary pitch (one image, one tag) while letting
  banks run 5×ingest + 2×api + 1×worker at billion-spans
  scale.
- **v0.6.2** — Helm chart `deployment.mode: monolithic |
  distributed` toggle. Monolithic = unchanged behaviour from
  v0.5.x (one Deployment, replicaCount applies). Distributed
  = three Deployments + four Services (`<release>` alias →
  api, plus `-ingest`/`-api`/`-worker`). HPA targets api in
  distributed mode; worker locked at 1 replica.
- **v0.6.3** — SSE Redis pub/sub bridge. Worker-pod-fired
  events (problem.open, anomaly.fire) ride a `coremetry-
  events` Redis channel so every api pod's local SSE
  subscribers receive them. PodID-stamped envelopes prevent
  loops; 200ms publish deadline so a wedged Redis doesn't
  stall the evaluator. Single-pod / Noop-cache installs are
  unchanged (no Redis activity).
- **v0.6.4-v0.6.7** — Model Context Protocol server. JSON-RPC
  2.0 over HTTP+SSE per spec 2024-11-05. Exposes tools (7
  telemetry surfaces in `internal/mcptools/`), resources
  (URI-addressed snapshots + templated per-id reads), and
  prompts (curated system+user message pairs that surface the
  in-app ✨ Explain workflows). Auth via existing JWT
  middleware — viewer/editor/admin roles carry into MCP. Runs
  on api+all modes only (worker/ingest pods don't take
  operator traffic).
- **v0.6.8** — AI-driven CH query optimizer on
  `/admin/clickhouse`. Operator pastes SQL, Copilot rewrites
  it against the MV catalogue + six-rule checklist (MV bypass
  / LIMIT / max_execution_time / time-bounded WHERE / GLOBAL
  IN / quantileTDigest defaults). Suggestion only — no auto-
  run; operator copies the optimized SQL to their CH client.
  Routes through `s.copilotExplain` so every call writes an
  ai_calls row for /ai attribution.

  # Coremetry Development Rules

## Backend (Go)
- Veri yazma işlemlerinde ClickHouse Async Insert (`async_insert=1`) mekanizmasını bozma.
- Her yeni eklenen API endpoint'i için mutlaka `internal/api/api.go` içine tip güvenli rota ekle.
- Arka plan işçilerinde lider kilidini (Leader Lock) korumak için mutlaka Redis mutex yapısını kullan.

## Frontend (TypeScript/React)
- Grafik çizimleri için kesinlikle Chart.js veya ağır kütüphaneler ekleme, sadece `uPlot` kullan.
- State güncellemelerini gerçek zamanlı SSE (Server-Sent Events) veriyolu ile senkronize et.
