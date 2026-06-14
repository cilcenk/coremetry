# /spec — Correlated Signals: one pivot surface

**Status:** proposed · **Series target:** v0.8.x · **Author:** eng (worktree spec, no code written)

## Problem / Why

Coremetry's differentiator is the OTel promise: one `trace_id` / `service` / time-window stitches traces ↔ logs ↔ metrics. The plumbing all exists — but **scattered**. During an incident the operator manually hops: sees a metric spike on `/explore`, clicks an exemplar ◆ to `/trace?id=`, switches to the Trace page's Logs sub-tab, then opens `/logs?traceId=` to widen. Each hop is a separate page, a separate mental reset. [[project-correlation-differentiator]]: the Datadog/Dynatrace edge is *automatic* anomaly→root-cause, and "the pieces exist unassembled." This spec **assembles** them into one pivot surface — a context drawer that, given any single signal, shows the correlated other two without a page change.

This is **synthesis, not new capability**. Every join already has an endpoint; the gap is a unified pivot model + one surface that renders all three lenses side-by-side and lets the operator re-anchor.

## What (the pivot model)

A **pivot anchor** is one of three shapes, each carrying enough context to derive the other two:

| Anchor (FROM) | Carries | Derives Y | Derives Z |
|---|---|---|---|
| **Trace** (`traceId`) | trace_id, service, [from,to] | Logs sharing trace_id | Metric series for the trace's service over [from,to] (+ the span timeline already in TracePeekDrawer) |
| **Log line** (`traceId?`, `service`, `ts`) | trace_id (if W3C-populated), service, ts | Trace by trace_id (or service+window trace list if no trace_id) | ±30m log context (existing) + service metric series around ts |
| **Metric point** (`service`, `ts`, `metricKind`) | service, ts, breached metric | Exemplar trace(s) for (service, window, kind) | Logs for service around ts, severity-bucketed |

The **join keys**, in priority order, are exactly the three the codebase already trusts:
1. **`trace_id`** — the only cross-signal key OTel carries everywhere (this is literally why `dql.parseJoin` hard-codes `JoinKey = "trace.id"` and rejects anything else, `internal/dql/dql.go:196`). When present, it's an exact join — no time fuzz.
2. **`service.name`** — the fallback when no trace_id (a raw metric data point has no trace_id on the wire today; see `useExemplars` note in `lib/otel/hooks.ts:52-57`).
3. **time-window `[from,to]`** — bounds every service-keyed derivation; already the universal currency via `useUrlRange` and the `RangeParams` API convention.

The model is deliberately the same one `RootCause` already implements for a Problem (`internal/api/rootcause.go:19-31`): fan out to N existing reads in parallel, each soft-fails to empty, render whatever came back. The new surface generalises that bundle from "anchored on a Problem" to "anchored on any of the three signal shapes."

## Files

**Backend (one new thin handler + its glue — everything else is reuse):**
- `internal/api/correlate.go` *(new)* — `getCorrelationContext(w, r)`. Reads anchor params (`kind=trace|log|metric`, `traceId?`, `service?`, `tsNs?`, `range`), resolves the join key, fans out to **existing store methods** under one `s.serveCached` (cache key hashes ALL inputs — sorted + FNV per the cache.go convention, `internal/api/cache.go:208`), returns a `CorrelationContext` bundle. No new CH query is written here — it orchestrates `s.store.FindExemplar`, `s.store.QuerySpanMetric`/`QueryMetric`, and `s.logs.Search` exactly as `rootcause.go` orchestrates its sub-reads. Pattern-cloned from `getProblemRootCause`.
- `internal/api/api.go` — register `mux.HandleFunc("GET /api/correlate/context", s.getCorrelationContext)` in the read block next to `/api/spans/exemplar` and `/api/problems/{id}/rootcause` (~lines 497–518). Read-only, open (same posture as `/api/problems`, `/api/correlations`). **No** auth gate / audit entry (it writes no state — per the CLAUDE.md "when you ship a feature" checklist, gates 3–4 don't apply to a pure read).

**Backend (only if the metric entry point must be honest — see Sequencing):**
- A real metric→trace_id exemplar resolver. Today `FindExemplar(ExemplarReq{Service,Operation,From,To,Kind})` (`internal/api/api.go:4002`) returns the *matching* trace for a (service,window,kind) — which is the lossy shortcut `useExemplars` documents. That's good enough for v1 of the metric pivot; a true OTLP-exemplar resolver is a *follow-on*, not a blocker. **Flag, don't build, in this spec.**

**Frontend (one new shared component + two type/client lines + entry-point wiring):**
- `frontend/src/components/CorrelationContextDrawer.tsx` *(new)* — the unified pivot surface (Modal/drawer). Props: `anchor: PivotAnchor | null; onClose`. Renders three lenses (Trace / Logs / Metrics) plus a "re-anchor" affordance. **Reuses** `TracePeekDrawer`'s service-timeline + log-list rendering (extract its two inner blocks into shared sub-components rather than duplicate), and the `lib/otel` hooks (`useCorrelatedLogs`, `useExemplars`) for the joins — *do not re-derive*. Loading/error/empty via shared `<Spinner/>`/`<Empty/>`.
- `frontend/src/lib/types.ts` — add `PivotAnchor` (discriminated union on `kind`) and `CorrelationContext` (the bundle shape). Source of truth per the type-discipline rule.
- `frontend/src/lib/api.ts` — add `correlateContext(anchor)` method next to `problemRootCause`/`logsContext` (~lines 500–702). Wraps `GET /api/correlate/context`.
- Entry-point wiring (small edits, no new pages required for v1):
  - `frontend/src/pages/Explore.tsx:676` — the exemplar ◆ click currently does `navigate('/trace?id='+id)`. Change to open the drawer anchored on that trace (keeps the operator in Explore). This is the single highest-traffic pivot.
  - `frontend/src/pages/Logs.tsx` — the existing `👁` peek (currently `TracePeekDrawer`, `Logs.tsx:770`) becomes a CorrelationContextDrawer anchored on the log line (trace_id if present, else service+ts).
  - `frontend/src/pages/Trace.tsx` — add a "Correlate ◆" action next to the existing `≡ Logs` DrillButton (`Trace.tsx:294`) that opens the drawer anchored on the trace's service+window, surfacing the *metric* lens the Trace page doesn't currently show.

**Optional (defer to a follow-on once the drawer proves out):**
- `frontend/src/App.tsx` + `frontend/src/components/Sidebar.tsx` — a standalone `/correlate` route under the "Investigate"/Signals section that opens the drawer full-page from a deep link (`?kind=trace&traceId=…&range=…`). The drawer is built shareable-URL-first so this is a thin wrapper. **Drawer first, route second.**

## API surface

```
GET /api/correlate/context
  ?kind=trace|log|metric
  &traceId=<hex>            (kind=trace required; kind=log optional)
  &service=<name>          (kind=metric required; kind=log/trace optional, derived)
  &tsNs=<unix-ns>          (kind=log|metric — the pivot instant)
  &range=<encoded>         (or from/to ns; derives [from,to])
  &metricKind=error|latency|throughput   (kind=metric — picks exemplar kind)
→ 200 CorrelationContext {
    anchor:   { kind, traceId?, service?, tsNs?, fromNs, toNs },
    trace?:   { traceId, rootName, service, durationMs, spanCount, services[], errSpans, serviceTimeline[] },
    logs?:    LogRow[]            // trace_id join when present, else service+window, severity-bucketed counts in header
    metrics?: SpanMetricSeries[]  // the anchor service's RED series over [from,to] (rate/error_rate/p99)
    exemplar?: Exemplar           // for kind=metric — the representative bad trace to pivot INTO
  }
```

- **Cache:** `s.serveCached(w,r,key,30s,fn)`; `key := fmt.Sprintf("correlate:%s:%x", kind, fnvDigest(sorted(traceId,service,tsBucket,fromBucket,toBucket,metricKind)))` — every input hashed, time bucketed to the minute so concurrent triage clicks share the trip (the `rootcause:%s:%d` minute-bucket trick, `rootcause.go:90`).
- **Reuses verbatim:** `s.store.FindExemplar` (`ExemplarReq`), `s.store.QuerySpanMetric` / `QueryMetric` (the same path DQL and Explore hit, so one cache + one MV story), `s.logs.Search` (`logstore.Filter` with `TraceID` or `Service`+`From`/`To`). The trace lens reuses the `/api/traces/{id}` shape already loaded by `api.trace(id)`.
- **No new SQL string.** Every read is an existing typed chstore/logstore method with `LIMIT` + `max_execution_time` + time-bounded WHERE already enforced inside it. (Verified: `DistinctTraceIDsForFilters` caps at limit, `getLogsContext` bounds ±30m, `QuerySpanMetric` is MV-backed.)

## Schema changes

**None.** No CH table, no MV, no migration. The pivot is pure orchestration over existing reads — the whole point is that the data already lands and the joins already work. (Confirmed against the `/clickhouse-schema` invariants: MV-first reads, no raw-spans aggregate.) `saved_views` is **not** touched in v1; if a "save this correlation view" affordance is wanted later it slots into `saved_views(page='correlate', …)` per invariant #5 — flagged as a follow-on, not built here.

## UX surface

**A context drawer (`CorrelationContextDrawer`), opened from any signal, three-lens.** Modal/right-drawer (ESC + backdrop close — same affordance as `TracePeekDrawer`, so muscle memory carries). The page underneath keeps its filter/range state.

Layout, top→bottom:
1. **Anchor header** — what you pivoted FROM (e.g. "Trace `c9ea…` · checkout-api · 1.2s · 3 errors") + the resolved join key chip (`trace_id` exact / `service+window` fuzzy — operator must see which join they're trusting).
2. **Three lenses** as collapsible sections, ordered by anchor:
   - **Trace lens** — the service-timeline mini-waterfall (extracted from `TracePeekDrawer`) + "Open full trace →" (`/trace?id=`).
   - **Logs lens** — chronological log lines sharing the trace_id (offset-from-trace-start, severity-coloured — `TracePeekDrawer`'s existing list), with severity-bucket counts in the section header; "Widen to /logs →" (`/logs?traceId=`).
   - **Metrics lens** — the anchor service's RED series (rate / error_rate / p99) over [from,to] in a uPlot chart with the pivot `ts` marked; "Open in Explore →" (`/explore` with the service+range prefilled). **uPlot only**, per the project chart constraint.
3. **Re-anchor** — clicking an exemplar ◆ in the Metrics lens, or a trace_id in the Logs lens, *re-opens the drawer anchored on that signal* (the pivot mesh: from Z you can pivot again, never leaving the drawer). This is the synthesis move RootCausePanel can't do — it's Problem-bound and one-shot.

`range` carries via `useUrlRange`'s global-window persistence (`lib/useUrlRange.ts`), so opening the drawer and then "Open in Explore →" lands on the *same* window. Drawer state is reflected in URL params (`?correlate=trace:c9ea…`) so a correlation view is shareable / back-button-safe — built shareable-first so the optional `/correlate` route is a free follow-on.

States: `<Spinner/>` while the bundle loads; per-lens soft-fail (a lens with no data shows an inline "no correlated logs in window" rather than blanking the drawer — mirrors RootCausePanel's partial-bundle render, `RootCausePanel.tsx:19-20`); `<Empty/>` (with icon — the Empty-needs-icon gotcha) only when *all three* lenses are empty.

## Risk

- **Honesty of the metric→trace pivot (highest).** A raw OTLP metric data point carries no trace_id today (`useExemplars` is explicit: it surfaces "the same set a metric data point would exemplar" via a `service+window` trace query, not true exemplars, `hooks.ts:52-57`). So the metric entry point's "derived trace" is **fuzzy** (service+window), not exact. Mitigation: the anchor-header join-key chip makes the fuzziness visible; the metric lens degrades gracefully. **Do not ship the metric entry point claiming an exact trace pivot** until a real exemplar resolver lands.
- **Log↔trace correlation requires W3C context populated.** If the collector doesn't ship logs with `trace_id`/`span_id` (the `TraceLogsPanel` empty-state already warns about this, `Trace.tsx:512`), the Logs lens falls back to service+window — same fuzzy-join caveat. Surface it the same way.
- **Cache poisoning.** The bundle hashes 6 inputs into one key — must use sorted+FNV, not length, or two anchors of the same cardinality cross-poison (the v0.5.187 incident). Ship the cache-key as a pure function with a table-driven test (the `cache_key_test.go` pattern) since this is a multi-input key.
- **ES vs CH log-backend divergence.** The Logs lens hits `logstore.Store.Search` which routes to ES or CH; at 10B logs/day [[project-elastic-scale]] the trace_id filter must use the indexed path. Verify the ES `Search` for a `TraceID` filter uses a term query (not a substring `Search`) — it does today (the `getLogsContext` / `useCorrelatedLogs` path already relies on this), but re-check before shipping the lens at billion-doc scale per [[feedback-ch-vs-es-divergence]].
- **Scope creep into RootCausePanel.** Tempting to "just generalise RootCausePanel." Resist: RootCausePanel is bubble-up/blast-radius-centric and Problem-bound (mounted only from `AnomaliesPage.tsx:1028`). The correlation drawer is signal-pivot-centric. They share the *bundle-fan-out pattern*, not the component. Extract shared sub-renderers (service timeline, log list) into `components/traces/` rather than coupling the two.

## Sequencing — what must land first

1. **(prereq, already done)** Cross-signal `trace_id` join — `dql.parseJoin` + `DistinctTraceIDsForFilters` + the `lib/otel` correlation hooks. ✅ present. This spec *synthesises* these; it does not re-build them.
2. **(this spec, v1)** `GET /api/correlate/context` + `CorrelationContextDrawer` for the **trace** and **log** anchors (both have a real or near-real trace_id join). Wire the three entry points (Explore ◆, Logs 👁, Trace "Correlate ◆"). This is shippable on existing endpoints with zero schema work.
3. **(follow-on, separate release)** Honest **metric** anchor — gated on a real metric→exemplar→trace_id resolver. Until then the metric lens is read-only RED series + a *labelled-fuzzy* service-window trace list (acceptable, but don't oversell it).
4. **(follow-on)** Standalone `/correlate` route + Sidebar entry + `saved_views(page='correlate')` persistence. The drawer is built URL-shareable-first so this is thin.

## Estimate

- Backend `getCorrelationContext` (orchestration only, pattern-cloned from `rootcause.go`) + route + cache-key test: **~0.5 day**.
- `CorrelationContextDrawer` + extracting `TracePeekDrawer`'s two sub-blocks to shared components + uPlot metrics lens: **~1.5 days**.
- Types + api client + 3 entry-point edits + tsc/build/audit gates: **~0.5 day**.
- **v1 total ≈ 2.5 days.** Metric-anchor honesty (the exemplar resolver) and the standalone route are explicitly out of this estimate — separate follow-on releases.

## Open questions

1. **Drawer vs. full page for v1?** Spec recommends drawer-first (keeps operator in-context, lowest-friction pivot). Confirm a standalone `/correlate` deep-link route is a *follow-on*, not a v1 requirement.
2. **Metric anchor in v1 or deferred entirely?** Two options: (a) ship the trace+log anchors now, add metric anchor only when a real exemplar resolver exists; (b) ship metric anchor now with a visible "fuzzy join" chip. Recommendation: **(a)** — an APM that claims metric→trace correlation and delivers a fuzzy service-window list erodes the differentiator more than omitting it.
3. **How wide is the metric lens window?** The anchor's [from,to] from `range`, or a tighter symmetric window around `tsNs` (like `getLogsContext`'s ±30m)? Recommend ±30m around the pivot for the metric/log lenses (matches the log-context convention) while keeping the *range* for the "Open in Explore" handoff.
4. **Re-anchor depth.** Should the pivot mesh allow unbounded re-anchoring (trace→log→trace→…), or cap depth to avoid a confusing breadcrumb? Recommend a simple 1-level "back to previous anchor" rather than a full stack for v1.
5. **Does the metric lens reuse the Explore panel builder or a standalone uPlot?** Reusing `QuerySpanMetric` keeps one cache+MV story; reusing the Explore *rendering* may overcouple. Recommend: same endpoint, standalone small uPlot chart in the drawer (the `/frontend-dashboard-panel` conventions apply if it grows).