## What

Bring Coremetry's trace explore to Dynatrace/Honeycomb parity on three axes. **First, the correction:** most of what the task framed as "missing" already ships — verify before scoping:

| Capability | Status today | Where |
|---|---|---|
| Advanced DSL filter chips (`=`/`!=`/`LIKE`/`IN`/`>`/`EXISTS`, `resource.`/`span.` scopes, custom attrs) | **EXISTS** | `FilterBuilder.tsx`, `chstore/filterexpr.go` (`FilterExpr.SQL()`, `ApplyFilters`) |
| `tracesAggregate` (groupBy builtin + custom `groupAttr`, RED stats, MV fast-path) | **EXISTS** | `GET /api/traces/aggregate` → `getTraceAggregate` → `GetTraceAggregate`/`AggregateFilter` |
| Attribute-KEY autocomplete (sampled discovery) | **EXISTS** | `GET /api/attribute-keys` → `getAttributeKeys`/`attributeKeysSQL` (200k sample) |
| **Trace attribute-VALUE autocomplete** | **EXISTS** | `GET /api/attribute-values` → `getAttributeValues` over `spans`, time-bounded + `LIMIT` + `max_execution_time=30`, with `acache` HLL/ZSET cardinality classification + `freeText` "~N distinct" fallback |
| Datadog-style tag/facet explorer | **EXISTS** | `GET /api/spans/facets` → `GetSpanFacets` |
| Trace-shape clustering | **EXISTS** | `GET /api/traces/shapes` → `GetTraceShapes` |
| Trace-level service-AND (must contain spans from every service) | **EXISTS** | `TraceFilter.RequireServices` (HAVING fan-in) |

The three **real** gaps:

1. **Value autocomplete is decoupled from the active query window and has no long-tail path for high-cardinality keys.** `FilterBuilder` hard-codes `api.attributeValues(k, '1h', 200, typedValue)` — it ignores the operator's selected time range, and for keys the `acache` policy classifies `CardHLL` (e.g. `session.id`, `user.id`, request-id-style headers) the cache returns `freeText:true` with no values and the picker falls through to a CH scan that the FilterBuilder never wires up as a *searchable* dropdown. So at billion-span scale a `user.id` filter is a blind free-text box.
2. **The query builder is flat AND-only.** `FilterExpr[]` is conjunction-joined (`ApplyFilters` `wc.add()` per expr). There is no OR, no nested grouping — operators can't express `(http.status >= 500 OR db.system = oracle) AND env = prod`. Dynatrace/Honeycomb both ship grouped boolean builders.
3. **No span-relationship / structural operators.** `RequireServices` is *set membership over a trace* — "this trace touches services {A,B}". There is **no** parent-of / child-of / ancestor / sequence operator. An operator cannot ask "traces where `frontend` calls `payment` **directly**" or "spans where a `db` CLIENT span is a child of a `checkout` SERVER span". No parent-child structural MV exists; this requires a bounded self-join over raw `spans`.

Scope: ship (1) and (2) as one coherent FilterBuilder/Explore upgrade; ship (3) as a separate, additive `relations` filter mode (own backend path, own MV-bypass justification). All three behind the existing `/traces` + `/explore` surfaces — no new top-level page.

---

## Files (shipping order — backend → frontend, per the 11-step checklist)

### Gap 1 — range-bound + long-tail value autocomplete

1. **`internal/api/api.go` — `getAttributeValues`** (~L2397). Add `from`/`to` query params (fall back to `since` for back-compat with FilterBuilder callers that still pass `since=1h`). Pass the explicit window into both the well-known-column path and the array-lookup path (replace `time >= now() - toIntervalSecond(?)` with a `time >= ? AND time <= ?` bound when `from`/`to` are present). For **very wide windows on high-cardinality keys**, add an inner sample bound mirroring `attrKeysSampleRows`: wrap the array-lookup `FROM coremetry.spans WHERE …` in a `SELECT … LIMIT <attrValuesSampleRows>` subquery so the `GROUP BY v` is O(sample), not O(window). Keep `max_execution_time=30`. Cache key must hash the new `from`/`to` (currently `attr-values:%s:since=%s:limit=%d:q=%s` — extend, don't replace, and keep it stable).
2. **`internal/api/api.go` — new `attrValuesSampleRows` const** next to `attrKeysSampleRows` (~L2343). Document the sample is value-popularity-ordering only, not exact counts (same honesty caveat as `attributeKeysSQL`).
3. **`internal/acache/acache.go` — `GetAttributeValues`** (~L717). No behaviour change to the HLL path, but the `freeText:true` return needs the HTTP layer to *still* offer a CH-backed `q` search (today the FilterBuilder treats `freeText` as "give up"). The fix is on the API + FE side; acache stays the source of the "~N distinct" estimate. Confirm `approxCount` is plumbed to the response so the picker can render "~12k distinct — type to search".
4. **`internal/api/api.go` — `getAttributeValues` response shape**: when the bare key is `CardHLL` and the acache hit is `freeText`, return `{ freeText: true, approxCount: N, values: [] }` and on a non-empty `q` ALWAYS run the bounded CH search (so the long-tail value is reachable). Today the acache HIT short-circuits before the CH fallback; gate the short-circuit on `q == ""`.
5. **`internal/api/attribute_values_sql_test.go` (new)** — table-driven, per CLAUDE.md #11. Pin: (a) `from`/`to` bound present when window passed; (b) inner `LIMIT <sample>` present on the array-lookup path for wide windows; (c) `max_execution_time = 30` present on both paths; (d) cache key changes when `from`/`to` change (no length-only collapse). Cite this release tag + the "blind user.id box" symptom in the header comment.

### Gap 2 — grouped AND/OR query builder

6. **`internal/chstore/filterexpr.go` — `FilterGroup` type + `BuildFilterGroupSQL`** (new, alongside `FilterExpr`). A group is `{ join: "AND"|"OR", filters: FilterExpr[], groups: FilterGroup[] }` (one level of nesting is enough for parity; document the depth cap). `BuildFilterGroupSQL` recursively emits `(a AND b OR (c AND d))` reusing `FilterExpr.SQL()` per leaf. **Keep `ApplyFilters([]FilterExpr)` untouched** for every existing caller (facets, aggregate, attr-keys, DQL) — add a parallel `ApplyFilterGroup(*whereClause, FilterGroup)` so the flat path is a zero-risk no-op. Backward-compat: a `FilterGroup{join:"AND", filters: <legacy []FilterExpr>}` is byte-identical to today's output (assert in test).
7. **`internal/chstore/repo.go` — `TraceFilter`** (L1009) + **`AggregateFilter`** (L1772): add optional `FilterRoot *FilterGroup`. In `buildGetTracesWhere` (L1071) and `GetTraceAggregate` (L1851), if `FilterRoot != nil` call `ApplyFilterGroup`, else fall back to `ApplyFilters(f.Filters)`. **MV gate:** any `OR` across attribute keys disqualifies the `trace_summary_5m` fast-path exactly like `Search`/custom-attr does today (the MV can't satisfy arbitrary OR) — extend the existing fast-path condition in `GetTraces` (~L1200) and `GetTraceAggregate` (~L1812) to also require `FilterRoot == nil || FilterRoot.isFlatAnd()`.
8. **`internal/chstore/filtergroup_test.go` (new)** — assert flat-AND group == legacy SQL byte-for-byte; assert OR/nesting precedence parenthesisation; assert malformed/empty leaves are skipped (mirror `ApplyFilters`' silent-skip contract).
9. **`internal/api/api.go` — `parseFilters`/`parseFiltersAndDSL`** (~L8762): accept either the legacy `filters=[FilterExpr]` JSON OR a new `filterGroup=<FilterGroup>` JSON. Wire `getTraces`, `getTraceAggregate`, `spanFacets`, `getAttributeKeys`/`getAttributeValues` to prefer `filterGroup` when present. Cache keys for all of these must hash the raw `filterGroup` string (the FNV/sorted rule — `getAttributeKeys` already hashes `rawFilters`; extend it).

### Gap 3 — span-relationship operators (additive `relations` mode)

10. **`internal/chstore/relations.go` (new)** — `GetTracesByRelation(ctx, RelationFilter)`. `RelationFilter{ Parent FilterExpr[], Child FilterExpr[], Kind: "child-of"|"descendant-of"|"sequence", Direct bool, From, To, Limit }`. Query shape = **bounded self-join over raw `spans`** (no MV exists for parent-child; document the MV-bypass justification in `/clickhouse-schema` terms — structural parent/child cannot be precomputed in any `*_5m`). Skeleton:
    ```sql
    SELECT DISTINCT c.trace_id
    FROM spans AS c
    INNER JOIN spans AS p
      ON c.trace_id = p.trace_id AND c.parent_id = p.span_id   -- child-of: direct edge
    WHERE c.time >= ? AND c.time <= ?                            -- BOTH sides time-bounded
      AND p.time >= ? AND p.time <= ?
      AND <parent predicates on p.*> AND <child predicates on c.*>
    LIMIT ?                                                       -- hard cap
    SETTINGS max_execution_time = 30, join_algorithm = 'parallel_hash',
             max_bytes_in_join = 536870912                       -- spill, don't OOM
    ```
    `descendant-of` relaxes the join to `c.parent_id IN (p.span_id …)` transitively — **cap to a fixed depth (2)** rather than recursive CTE at billion-span scale; document that ancestor chains deeper than 2 hops are out of scope v1. `sequence` (A then B in the same trace, A.end ≤ B.start) is a self-join on `trace_id` with a time-order predicate. Resolve to `trace_id` list, then reuse the existing `GetTraces` page fetch (`trace_id IN (page ids)` rides the `idx_trace` bloom skip index — see `repo.go` L1543) so the list rendering is unchanged.
11. **`internal/api/api.go` — `getTracesByRelation` handler + route** `GET /api/traces/relations` (register in the trace block ~L427, next to `/api/traces/aggregate`). `serveCached` 30s; key hashes parent+child filter JSON (sorted+FNV) + kind + direct + window. Reject `>8` predicates per side and empty windows (400). No auth gate (read-only).
12. **`internal/chstore/relations_test.go` (new)** — pin: both join sides time-bounded; `LIMIT` present; `max_bytes_in_join` spill setting present; `child-of` uses `parent_id = span_id` (direct) vs `descendant-of` the depth-capped form; predicate-count cap enforced.

### Frontend (after backend green)

13. **`frontend/src/lib/types.ts`** — add `FilterGroup`, `RelationFilter`, `RelationKind`; extend `AttributeValuesResponse` with `{ freeText?: boolean; approxCount?: number }`.
14. **`frontend/src/lib/api.ts`** — `attributeValues(key, range, limit, q)` change `since` → range-aware (`from`/`to` from the page range, per the `timeRangeToNs` inside-useMemo rule); add `tracesByRelation(params)` → `GET /api/traces/relations`; allow `traces`/`tracesAggregate`/`spanFacets` to send `filterGroup`.
15. **`frontend/src/components/FilterBuilder.tsx`** — (a) thread the active range into the value-autocomplete effect (~L199) instead of `'1h'`; (b) when `attributeValues` returns `freeText`, render the `Combobox` in search-on-type mode with the "~N distinct — type to search" hint and keep firing the debounced `q` query (don't blank the dropdown); (c) wrap the flat chip row in an optional **group** affordance: a top-level `AND`/`OR` toggle + "add group" that nests one level. Persist `FilterGroup` to URL via the existing encode/decode (extend `encodeFilters`/`decodeFilters`). Keep the flat path as the default render so existing saved views/URLs still parse.
16. **`frontend/src/components/RelationBuilder.tsx` (new)** — two `FilterBuilder` instances (parent predicates / child predicates) + a kind selector (`child-of` / `descendant-of` / `sequence`) + a "direct only" checkbox. Compact, reuses the `<Button>` atom + `.field` styles.
17. **`frontend/src/pages/Traces.tsx`** — add a `relations` value to the `View` union (L59) alongside `list`/`aggregate`/`shapes`; render `RelationBuilder` and fetch via `api.tracesByRelation`, then reuse the existing list table for results. URL-reflect (`view=relations`, parent/child filter JSON) in the existing state→URL effect (~L192). Loading/error/empty via shared `<Spinner/>`/`<Empty/>` (Empty needs an icon).
18. **`frontend/src/pages/explore/`** — surface the grouped builder + relation mode in the Explore query row where FilterBuilder is already embedded (`QueryRow.tsx` / `QueryPanel.tsx`), so the unified Explore experience gets it too.

---

## API surface

```
GET /api/attribute-values?key=&from=&to=&since=&limit=&q=
    → [{ value, count }]                          (CardTrack / well-known / CH search)
    → { freeText:true, approxCount:N, values:[] } (CardHLL, q empty)
    → [{ value, count }]                          (CardHLL, q present → bounded CH search)
    NEW: from/to window-bound; q always reachable on high-card keys.

GET /api/traces?...&filterGroup=<FilterGroup-json>          (alt to filters=)
GET /api/traces/aggregate?...&filterGroup=<FilterGroup-json>
GET /api/spans/facets?...&filterGroup=<FilterGroup-json>
    NEW: grouped AND/OR filters; legacy filters= still honoured.

GET /api/traces/relations?parent=<FilterExpr[]-json>&child=<FilterExpr[]-json>
    &kind=child-of|descendant-of|sequence&direct=true&from=&to=&limit=
    → { traceIds:[…], traces:[TraceRow], hasMore }   (NEW)
```

`FilterGroup` JSON: `{ "join":"AND"|"OR", "filters":[FilterExpr], "groups":[FilterGroup] }` (depth ≤ 1 nested groups v1).

---

## Schema changes

**None.** All three gaps read existing `spans` columns (`parent_id`, `span_id`, `trace_id`, `attr_keys`/`attr_values`, `res_keys`/`res_values`, the well-known LowCardinality columns) and reuse `acache`'s existing Redis structures. No migration, no new MV.

Explicitly **rejected**: a precomputed parent-child edge MV. Structural relationships (`child-of`/`sequence`) are per-trace topology that a `*_5m` aggregate can't represent without exploding cardinality; the bounded self-join over `spans` (time-windowed + `LIMIT` + join-spill) is the correct shape per `/clickhouse-schema`. This is the one read that legitimately bypasses the MV-first invariant — documented at the handler.

---

## UX surface

- **FilterBuilder value picker** now respects the page range and degrades gracefully on high-cardinality keys: dropdown for `CardTrack`, "~N distinct — type to search" + live CH search for `CardHLL`/`session.id`/`user.id`. No more blind free-text box.
- **Grouped builder**: a top-level AND/OR toggle + one level of nesting; default render is the existing flat chip row so nothing regresses for current users/saved views.
- **`/traces` gets a `Relations` view** next to List / Aggregated / Shapes: two predicate builders + kind selector → trace list. "frontend → payment (direct child)" becomes one query.
- All loading/error/empty states use shared `<Spinner/>`/`<Empty/>`; all buttons use the `<Button>` atom; result table reuses the existing server-paged trace list.

---

## Risk

- **🔴 BIGGEST — trace-attr value autocomplete cardinality at billion-span scale.** `getAttributeValues` array-lookup path (`attr_values[indexOf(attr_keys, ?)] GROUP BY v`) over a wide window on a hot key is the exact shape that timed out for *keys* in v0.7.30. Mitigation: (a) inner `LIMIT <sample>` before the `GROUP BY` on the array path; (b) keep `max_execution_time=30`; (c) lean on `acache` to classify high-card keys as `CardHLL` so the common case never hits CH at all — only an explicit `q` search does, and that's bounded. Must ship with the SQL-shape regression test (#5) or it silently regresses to a full-window scan.
- **🟡 OR disqualifies the MV.** Gap 2 OR-groups fall to the raw-spans GROUP-BY path (same as `Search` today), which can hit the `tracesSpillSettings` 512 MiB external-group-by. Acceptable (it already happens for free-text search) but document it so operators understand an OR query is costlier than an AND query.
- **🟡 Self-join blow-up.** Gap 3's `spans ⋈ spans` on a wide window is the riskiest new query. Both sides MUST be time-bounded (a one-sided bound lets the planner scan the full table on the other), `max_bytes_in_join` must spill, `LIMIT` must cap output, and `descendant-of` must be depth-capped (no recursive CTE). Predicate-count cap (≤8/side) stops a malicious URL from fanning out the join.
- **🟡 Cache-key poisoning.** Every new param (`from`/`to`, `filterGroup`, relation predicates) must enter the cache key via the sorted+FNV rule. `getAttributeValues`/`getAttributeKeys` already hash their filter strings; extend, don't bypass. (Note: the existing `spanFacets` cache key concatenates raw nanos+filter string rather than FNV — out of scope to fix here, but don't copy that pattern into the new handlers.)
- **🟢 Back-compat.** Flat-AND `FilterGroup` must emit byte-identical SQL to legacy `[]FilterExpr` (pinned by test) so saved views / shared URLs / DQL / facets are untouched. `since=` stays honoured on `attribute-values`.

---

## Estimate

- **Gap 1 (range-bound + long-tail value autocomplete):** ~0.5 day. Mostly `getAttributeValues` + one regression test + FilterBuilder effect rewire. Lowest risk, highest daily-use payoff — ship first as its own release.
- **Gap 2 (grouped AND/OR builder):** ~1.5 days. `FilterGroup`/`BuildFilterGroupSQL` + MV-gate extension + byte-identical back-compat test + FilterBuilder nesting UI + URL codec. Medium risk (touches the shared filter SQL builder).
- **Gap 3 (span-relationship operators):** ~2 days. New `relations.go` self-join (the careful part), handler+route, regression test, `RelationBuilder.tsx`, Traces `relations` view. Highest risk; ship last and standalone.

Total ~4 days across 3 independent releases (CLAUDE.md aggressive cadence — never batch). Each is independently shippable and testable.

---

## Open questions

1. **Value-autocomplete window default.** Range-bind to the operator's selected range, or keep a cheap `1h` default and only widen on explicit "search all"? Wide windows on hot keys are the cardinality risk — I lean toward defaulting to the page range but capping the inner sample hard. Confirm the acceptable sample size (reuse `attrKeysSampleRows = 200_000`, or smaller for values since per-key cardinality is higher?).
2. **Grouped-builder nesting depth.** One level of nesting buys ~95% of real queries (`(A OR B) AND C`). Full arbitrary nesting is a bigger UI lift and a harder URL codec. Cap at depth 1 for v1?
3. **`descendant-of` semantics.** Depth-2 cap (direct child + grandchild) vs a true recursive ancestor walk. Recursive CTE over `spans` at billion-span scale is a non-starter; is depth-2 enough for the "service A eventually calls service B" question, or do operators need the full chain (in which case route them to the trace waterfall instead)?
4. **`sequence` operator scope.** "A then B in the same trace" — order by span start time only, or require A.end ≤ B.start (true happens-before)? The latter is more correct but needs both end timestamps in the join predicate. Start-order is cheaper; confirm which matches operator intent.
5. **Surface placement.** Relations as a 4th `/traces` view (proposed) vs a first-class Explore query type. Explore is the strategic home (per the Explore-v2 work), but `/traces` is where operators already filter. Ship in `/traces` first, fold into Explore second?
6. **acache HLL coverage.** Does the live `acache` policy already classify the headline high-card trace attrs (`session.id`, `user.id`, `http.request.header.*`, `enduser.id`) as `CardHLL`? If not, the long-tail path is the only protection — confirm the `NewStaticPolicy` denylist or rely on the default `CardHLL` for unknown keys (which the code already does — good).