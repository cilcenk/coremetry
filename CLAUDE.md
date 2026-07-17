# Coremetry

OpenTelemetry-native APM. Single Go binary + ClickHouse + Redis +
optional external Elasticsearch/Tempo. 1000s of services, 1B+
spans/day, one container. Rubric when in doubt: **"how would
Datadog / Dynatrace / Honeycomb engineer this?"**

## Hard constraints — non-negotiable

| Constraint | Enforced where |
|---|---|
| **Single-binary release** — one image/tag; `COREMETRY_MODE=all\|ingest\|api\|worker` for roles | `main.go`, `Dockerfile` |
| **Picker = server-side search** — never eager `<Combobox options={…}>` | `ServicePicker` / `OperationPicker` / `MetricNamePicker` |
| **Table > 100 rows** — virtualize, server-paginate, or `content-visibility:auto` | every list page |
| **Every data table sortable + resizable** — shared primitive, never hand-rolled | `useDataTable` + `DataTable.tsx`; template `SlowQueries.tsx` |
| **Cache key hashes ALL inputs** — sorted + FNV; length-only digests cross-poison (v0.5.187) | `internal/api/cache.go` |
| **`timeRangeToNs(range)` only inside `useEffect`/`useMemo`** — bare in JSX = infinite refetch (v0.5.184) | every page using `range` |
| **CH query on `spans`/`metric_points`** — `LIMIT` + `SETTINGS max_execution_time` + time-bounded WHERE | `internal/chstore/*.go` |
| **Admin write = audit entry** — `s.audit(r, "kind.action", "resource", id, details)` | `internal/api/*.go` |
| **No PII redaction features** — operator preference: full fidelity | memory `feedback-no-redaction.md` |

## Performance budgets

- `/api/*` p99 < 200ms warm, < 1s cold; hot endpoints (`/api/services`, `/api/problems`, `/api/health`) < 50ms warm
- `/api/spans/heatmap` < 3s ≤6h window (auto-sample beyond); `/api/logs/patterns` < 2s at billion docs
- TTFI < 1.5s fresh tab; polling ≥ 10s (except `/api/health` 5s); every poller pauses on `document.hidden`

## Tech stack

**Backend:** Go 1.22+, ClickHouse 24+, Redis 7+. `main.go` wires
`chstore.Store`, `logstore.Store` (CH or ES via
`COREMETRY_LOGS_BACKEND`), `tempo.Service`, `cache.Cache`,
`auth.Service`, `notify.Notifier`, `copilot.Service`.
**Frontend:** React + Vite, no Tailwind (CSS vars in `globals.css`),
React Query, react-router v6. No state library — URL is source of
truth for shareable views.
**Versioning:** git tags `v0.9.X` (v0.8 zinciri v0.8.588'de kapandı, operatör kararı 2026-07-17). Runtime resolution:
`COREMETRY_VERSION` env > ldflag > `/app/VERSION` > `"dev"`.

## Architectural invariants

1. **OTel is the source of truth** — OTLP/gRPC + HTTP only; attributes kept verbatim.
2. **ClickHouse is the warm store** — ES is optional *read* backend for logs only.
3. **MV-first reads** — `service_summary_5m`, `operation_summary_5m`, `topology_edges_5m`, `db_summary_5m`, `db_caller_summary_5m`, `topology_root_flows_5m`. Raw `spans` for an aggregate = bug.
4. **`ReplacingMergeTree(version)` for state**, reads use `FINAL`. Whole-row replace: carry ALL fields forward.
5. **One CH table for saved state** — `saved_views(page='<kind>', …)`; no new schema per surface.
6. **Settings live in `system_settings`** — JSON blob per key; `LoadPersisted` at boot + `SavePersisted` on admin PUT (template: `internal/tempo/client.go`).
7. **Roles admin / editor / viewer** — `auth.RequireRole` / `RequireAnyRole`; viewer SEES state read-only, never blank.

## Ship checklist — every new operator-facing surface

1. Handler hits the right MV; 2. `s.serveCached` with hash-all-inputs
key; 3. auth gate + 4. audit entry if it writes; 5. settings via
`system_settings` if configurable; 6. type in `lib/types.ts`;
7. client method in `lib/api.ts`; 8. loading/error/empty states
(`<Spinner/>`, `<Empty/>`); 9. `npx tsc --noEmit` gate;
10. `go build ./...` gate; 11. bug-fix release = regression test
(pure-function, table-driven, header cites the vX.Y.Z; canonical:
`internal/api/cache_key_test.go`) + `go test ./...`.

AI explain affordances route through `s.copilotExplain(r, ...)`,
never `s.copilot.Explain` direct (/ai attribution).

## Code patterns

- **Cache key for a set:** `fmt.Sprintf("…:%x", fnvDigest(sortedSlice(set)))` — never `n=%d len(set)`.
- **CH bounds:** every spans/metric_points query has time-bounded WHERE + `LIMIT` + `SETTINGS max_execution_time`.
- **Tables:** `useDataTable` (`storageKey`, COLS with `sortValue`/`numeric`) + `<DataTableColgroup/>` + `<DataTableHead/>`; server-paged tables adopt resize half only. Rows > 100: `contentVisibility: 'auto'`.
- **Buttons/fields — one design language:** shared `<Button variant size>` atom (`components/ui/Button.tsx`), `Field.tsx` for labelled inputs, `.badge .b-ok/.b-err`. Never hand-roll `<button style={{…}}>`.
- **Secrets in Settings:** never echo back; "stored" indicator; empty input preserves stored value.
- **Logstore plurality:** detectors go through `logstore.Store.CountPatterns(...)` batched (ES `_msearch` / CH tokenbf) — never call ES directly from a detector.
- **Triage:** P1 = now (critical + 2x threshold / fresh deploy / open ≥ 4h), P2 = today, P3 = convenient. Reason string ships with every Problem.

## Workflow

- `kuyruk` = show prioritised queue, end with "Hangisi?". `devam` = continue current item.
- Operator-reported bugs = NEW priority, ship as `v0.9.X+1` immediately, never batch.

**Release — every functional change:**
```
edit → npx tsc --noEmit (fe) → go build ./... → go test ./...
→ make audit → git add <files> → commit (heredoc) → tag v0.9.X
→ git push && git push --tags → deploy (background, ONE at a time)
```
`make audit`: 🔴 critical blocks the tag; 🟡 warnings reviewed, ship
if known false positive.

**Commit format:** `v0.9.X — title (≤70)` + body (what/why/root
cause, 72 cols, operator bugs start "Operator-reported: …") +
`Co-Authored-By: Claude <noreply@anthropic.com>`.

**Skills (`.claude/skills/`):** `/release`, `/bugfix`, `/spec` (3+
file changes), `/kuyruk`, `/scale-audit` (quarterly),
`/clickhouse-schema` (BEFORE any CH table/query/MV change),
`/helm-chart-coremetry` (BEFORE `charts/coremetry/` changes),
`/otel-conventions` (BEFORE OTel-shaped data changes), `/mcp-tools`
(BEFORE MCP server changes), `/frontend-dashboard-panel` (BEFORE new
dashboard panel types), `/frontend-conventions` (BEFORE any frontend
change adding a component/table/filter/drawer/theme/polling loop),
`/tdd` (test-first loop for new backend features + pure helpers).

## Frontend UI conventions

Full rules in `/frontend-conventions`. The one-liners:

- One design language: `<Button>`/`<Field>`/`<Badge>` atoms +
  `.tab-strip`; never hand-rolled button styles.
- Every table = `useDataTable` (sort+resize, persisted widths);
  server-paged tables resize-only; >100 rows `content-visibility`.
- Pickers server-debounced; never validate a pick against a sampled
  subset (v0.8.265).
- URL = source of truth for every selection (drawer/focus/filters/
  tab/range) — write with replace:true; sig-guard URL→state imports
  (v0.8.253); one-way-read is a recurring bug class (256/265/267).
- Themes are token-level CSS vars only (dark/light/redhat v0.8.268);
  chrome variants via scoped token remap; charts re-resolve on
  data-theme; uPlot only.
- ES-cost UI discipline: fetch on expand/open only, no list
  prefetch, staleTime ≥ server TTL, cache-key params snapped to
  bounded rungs (v0.8.270).

## Pitfall rules — full incident stories in [docs/INCIDENTS.md](docs/INCIDENTS.md)

- `timeRangeToNs` bare in JSX → infinite refetch; memo it.
- Cache key from `len(set)` → cross-poisoning; stable digest.
- `table-layout:fixed` + `nowrap` + small width silently clips; use min/max-width + ellipsis + title.
- ES: no `case_insensitive` on `query_string` (8.x rejects); `_msearch` over per-pattern `_search`; `significant_text` needs `background_filter` + `sampler`.
- Drain templating is sample-based (1000/5min), never full-scan.
- Value+unit templates (Nh/Nd, ms/s): test EVERY unit; never `toDate()` around sub-day math (`retention_test.go`).
- Combined-MV drop: inner table first with `max_table_size_to_drop=0` (`dropCombinedMV`).
- MV state columns use `quantilesTDigestState`, never reservoir `quantilesState`; MV type changes cause a rolling-deploy read-error window — roll fast or dual-column.
- SQL references telemetry tables UNQUALIFIED (no `coremetry.` prefix).
- `toDateTime64(?)` bind args must be tz-less (no trailing `Z`) — `chDateTime64Arg`.
- Coremetry Deployment keeps `maxUnavailable: 0` or the OTel collector wedges on rollout ("zero addresses"); pre-fix: restart collector.

## What goes WHERE

| Domain | Path |
|---|---|
| Demo generators (Go synthetic / Java / JBoss) | `cmd/demo/`, `java-demo/`, `jboss-demo/` |
| OTLP ingest | `internal/otlp/` |
| CH writes (spans/metrics/logs) | `internal/chstore/` |
| ES log read backend | `internal/logstore/` |
| Tempo trace fallback | `internal/tempo/` |
| HTTP API + cache wrapper | `internal/api/` |
| Alert evaluator / anomaly / templater | `internal/evaluator/`, `internal/anomaly/`, `internal/templater/` |
| Notifications | `internal/notify/` |
| Auth (local + LDAP + OIDC) | `internal/auth/`, `internal/ldap/` |
| Copilot (system prompts at BOTTOM of copilot.go) | `internal/copilot/` |
| Topology correlator / CH migrations | `internal/topology/`, `internal/chmigrate/` |
| Frontend pages / components / types+client / routes / sidebar | `frontend/src/pages/`, `components/`, `lib/{types,api}.ts`, `App.tsx`, `components/Sidebar.tsx` |

`lib/types.ts` is the single source of truth for shared data shapes —
never re-declare in components. PascalCase types, camelCase props,
`?:` for `omitempty` fields.

## Demo realism

The three demo workloads share one load model (diurnal curve,
incidents, log-normal latency, correlated errors, real histogram
buckets, saturation metrics) — details in
[docs/DEMO-REALISM.md](docs/DEMO-REALISM.md). **Rule:** any new demo
scenario/metric reads from the load model (`L` / `DemoLoad`), never
its own fixed probability or uniform latency.

## Self-observability

`/admin/stats` reads `GetSystemStats(ctx)`; AI usage lands in
`ai_calls` → `/ai`. New ingest path or expensive endpoint = register
a counter.

## Anti-patterns (don't)

- Don't bypass `s.serveCached` on hot reads.
- Don't create new schema for user-saved state (use `saved_views`).
- Don't fetch full catalogues for pickers.
- Don't poll faster than 10s (except `/api/health`) or ignore `document.hidden`.
- Don't add backwards-compat shims when removing a feature.
- Don't propose data redaction features.
- Don't fix typing with `as any`.
- Don't write `// TODO` without a release-tag context — ship it or queue it via `kuyruk`.
- Don't add high-cardinality `String` metric dimensions — `LowCardinality(String)`.

Decision log (architectural calls, v0.5.208 → v0.6.8):
[docs/DECISIONS.md](docs/DECISIONS.md).

# Coremetry Development Rules

## Backend (Go)
- Veri yazma işlemlerinde ClickHouse Async Insert (`async_insert=1`) mekanizmasını bozma.
- Her yeni eklenen API endpoint'i için mutlaka `internal/api/api.go` içine tip güvenli rota ekle.
- Arka plan işçilerinde lider kilidini (Leader Lock) korumak için mutlaka Redis mutex yapısını kullan.

## Frontend (TypeScript/React)
- Grafik çizimleri için kesinlikle Chart.js veya ağır kütüphaneler ekleme, sadece `uPlot` kullan.
- State güncellemelerini gerçek zamanlı SSE (Server-Sent Events) veriyolu ile senkronize et.
