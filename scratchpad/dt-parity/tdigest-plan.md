# Unread-TDigest Percentile Surfacing — Executable Edit Plan

Verified against working tree at HEAD `4d510c71` (v0.9.256). Every line number below re-read from the main tree, not from the scratchpad `wt-*` worktrees.

---

## 0. Headline

**There is NO live wrong-quantile bug on any of the three pages.** All 14 `arrayElement(quantilesTDigestMerge(...))` read sites in `internal/chstore/` resolve to exactly the quantile their Go field name claims. See §2 for the full audit and the two asymmetries that *look* like bugs but are correct.

Two genuine live defects were found, neither a wrong number:
- **L1 (bug, ship first):** `/messaging` Trend column is dead 100% of the time *and* costs a real 200k-row ClickHouse query per range change.
- **L2 (dead payload):** `MessagingInstance.P95Ms` is selected, marshalled, and typed frontend-side — and rendered nowhere.

The **highest silent-wrong-number risk in this whole workstream is `/endpoints` P90**, because the two feeder paths carry *different quantile arg lists*. See §5 — it must not ship as a one-line index add.

---

## 1. The two index conventions (memorize before editing)

| Family | State arg list | 1-indexed map | Tables |
|---|---|---|---|
| **3-wide (summary family)** | `(0.5, 0.95, 0.99)` | 1=p50, 2=p95, 3=p99 | `service_summary_5m` :2293, `operation_summary_5m` :2324, `operation_group_summary_5m` :2367, `db_summary_5m` :2508, `db_caller_summary_5m` :2550, `db_statement_summary_5m` :2596, `messaging_summary_5m` :2794, `messaging_caller_summary_5m` :2830 (all `internal/chstore/store.go`) |
| **4-wide (doorway family)** | `(0.5, 0.9, 0.95, 0.99)` | 1=p50, **2=p90**, 3=p95, 4=p99 | `spanmetrics_1m` :2399, `spanmetrics_10s` :2417, `spanmetrics_1s` :2435 |

🔴 **Index 2 means p95 in one family and p90 in the other.** Copying an ordinal across families is the entire bug class. Both conventions are already documented in-tree: `internal/chstore/evaluator_reads.go:243` vs `:276-277`.

---

## 2. Live-bug audit (Q2) — result: CLEAN on quantile indices

Every site verified against its own feeding DDL:

**3-wide sites, all correct:** `dependencies.go:241` (3→p99), `:265` (3→p99), `:423` (3→p99), `:450` (3→p99), `:616` (3→p99), `:862/863/864` (1/2/3→p50/p95/p99), `db_trends.go:100` (3→p99), `dbqueries.go:140/141` (2/3), `dbstmt_detail.go:105/106/180/245` (2/3), `summary.go:452/516-518/563-565`, `repo.go:988-990/1057-1059`, `spanmetric.go:378-382/606-610/745-749` (confirmed reading `service_summary_5m` :408 / `operation_summary_5m` :633,:779 / `operation_group_summary_5m` via `mvTable` at `repo.go:959-961` — never spanmetrics).

**4-wide sites, all correct:** `endpoints.go:444` (4→p99 sparkline), `:456/457/458` (1/3/4→p50/p95/p99), `metricresolve.go:293-299` (1/2/3/4), `evaluator_reads.go:281` (1/3/4).

### The two "looks wrong, is right" asymmetries — do not "fix" these
1. `endpoints.go:457` uses index **3** for p95 while `endpoints.go:760` uses index **2** for the same p95. Correct: :457 reads a 4-wide state, :760 reads a 3-wide one.
2. `messaging_e2e.go:110` uses plain `quantiles(0.5,0.95,0.99)` and indexes the result **0-based in Go** (`qs[0]/qs[1]/qs[2]`, `messaging_e2e.go:183-185`). Different function, different convention, correct as-is. Never copy that offset into an `arrayElement`.

### L1 — `/messaging` Trend column is permanently dead + wasteful (LIVE BUG)
- `components/DependenciesTable.tsx:206` declares `{ id: 'trend', ... }` **unconditionally** for both `kind='db'` and `kind='queue'`.
- `components/DependenciesTable.tsx:253-273` calls `api.dbTrends(from, to)` **unconditionally** on every `range` change → `GET /api/databases/trends` → `chstore.GetDBTrends` → `FROM db_summary_5m` (`db_trends.go:100-102`), `LIMIT 200000`.
- Join key written at `:263-267` is `dbSystem|instance|dbName`; `trendFor` at `:280-285` looks up `${r.system}|${nameOf(r)}|${r.dbName ?? ''}` — for a messaging row that is e.g. `kafka|orders-topic|`, which cannot exist in `db_summary_5m`.
- Result: `TrendCell` (`:586-593`) always renders the muted `—` with `title="no trend in this window"`, and the page pays a full DB-trends scan for nothing.
- There is no messaging trends handler at all: `internal/api/api.go:557` registers only `/api/databases/trends`.

### L2 — `MessagingInstance.P95Ms` is a dead payload
Selected at `dependencies.go:863` (index 2 ✓), assigned `:888`, typed at `frontend/src/lib/types.ts:602` (`p95DurationMs?`) — but `pages/Messaging.tsx:106-107` maps only `p99DurationMs`/`p50DurationMs`, `DepRow` (`DependenciesTable.tsx:19-67`) has no `p95DurationMs`, and no P95 column exists. Rides the JSON on every `/api/messaging` request, rendered nowhere.

---

## 3. `/messaging` — exact edits

### M-a (bug fix, ship first): gate the Trend column + its fetch on `kind`
- `components/DependenciesTable.tsx:206` → wrap in the existing conditional-spread idiom: `...(kind === 'db' ? [{ id: 'trend', label: 'Trend', width: 140 }] : [])`.
- `components/DependenciesTable.tsx:253-273` → early-return inside the effect when `kind !== 'db'` (keep the `useEffect` itself unconditional — the file's own comment at `:245-246` requires stable hook order). Set `setTrends(null)` in that branch so `TrendCell` never shows the loading state.
- `components/DependenciesTable.tsx:486` → the `<td>` must be gated by the same `kind === 'db'` condition or the row/header column counts desync.
- Honest outcome: `/messaging` loses a column that never carried data, and stops issuing one 200k-row CH query per range change.

### M-b (zero backend, zero CH cost): surface the already-selected P95
No SQL change. Index 2 at `dependencies.go:863` is already correct and already shipping.
1. `components/DependenciesTable.tsx:58` — after `p50DurationMs?: number;` add `p95DurationMs?: number;`
2. `pages/Messaging.tsx:107` — after `p50DurationMs: d.p50DurationMs,` add `p95DurationMs: d.p95DurationMs,`
3. `components/DependenciesTable.tsx:200` — extend the `kind === 'queue'` spread with
   `{ id: 'p95', label: 'P95', sortValue: (r: DepRow) => r.p95DurationMs ?? 0, numeric: true, naturalDir: 'desc', width: 84 } as DataTableColumn<DepRow>`
4. `components/DependenciesTable.tsx:465-472` — clone the P50 `<td>` block immediately after it, using `r.p95DurationMs`. Keep the `=== undefined ? '—'` guard (rolling-deploy honesty).
5. **No delta badge on the new column.** `MessagingInstance` has `PriorP50Ms`/`PriorP99Ms` only (`dependencies.go:96-97`); `mergeMessagingPrior` sets only those (`internal/api/api_databases.go:178-179`). Do not pass a `prior` to `TrendDelta` for P95 — a missing prior would render as a bogus 0-baseline delta.

### M-c: drawer P50/P95 (aggregate strip) — ADD columns, never repoint
🔴 The adversarial pass refuted "change `:423` to index 2". Correct instruction: **keep index 3 as p99** and add two new projections.
- `internal/chstore/dependencies.go:423` — replace the single line with three, in this order:
  ```
  arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
  arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
  arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
  ```
- `dependencies.go:356` — add `P50Ms float64 \`json:"p50DurationMs"\`` and `P95Ms float64 \`json:"p95DurationMs"\`` above `P99Ms`.
- `dependencies.go:417` — `var avgMs, p50Ms, p95Ms, p99Ms *float64`
- `dependencies.go:429` — `row.Scan(&out.SpanCount, &out.ErrorCount, &avgMs, &p50Ms, &p95Ms, &p99Ms)` (Scan is positional — order must match the SELECT exactly)
- `dependencies.go:434` — add `out.P50Ms = safeF(p50Ms)` / `out.P95Ms = safeF(p95Ms)`
- `frontend/src/lib/types.ts:392` — add `p50DurationMs?: number; p95DurationMs?: number;` (optional, rolling-deploy)
- `frontend/src/features/dependencies/DetailDrawer.tsx:154` — add two `<Stat label="P50" .../>` / `<Stat label="P95" .../>` tiles; render `—` when undefined.

### M-d: caller-table P95 — SHARED STRUCT, must be atomic
🔴 `DBCallerBreakdown` (`dependencies.go:130-139`) is shared by the `/databases` drawer **and** the `/messaging` drawer. `P95Ms` is a non-pointer `float64`, so any path that doesn't select it renders a hard `0.0ms` — a plausible wrong number, not a blank. **Both queries in one commit or neither.**
- `dependencies.go:265` (DB callers) — add `arrayElement(..., 2) / 1e6 AS p95_ms` **before** the existing index-3 p99 line
- `dependencies.go:281` — add `&bP95` between `&bAvg` and `&bP99`; `:280` widen the var decl
- `dependencies.go:450` (messaging callers) — same addition
- `dependencies.go:466` — same Scan widening; `:465` var decl
- `dependencies.go:138` — add `P95Ms float64 \`json:"p95DurationMs"\`` above `P99Ms`
- `frontend/src/lib/types.ts:318` — add `p95DurationMs: number;`
- `DetailDrawer.tsx:456` — add the P95 column def; `:533` — add the matching `<td>`

---

## 4. `/databases` — exact edits

All indices below **survived refutation as ADDITIONS**. Where a verdict said "fatal", it was rejecting *repointing an existing p99 line*, not adding a new projection.

### D-a: overview grid P50 + P95 (`GetDatabases`)
- `internal/chstore/dependencies.go:616` — replace with three projections, indices **1 / 2 / 3** → `p50_ms` / `p95_ms` / `p99_ms`. Keep p99 at 3.
- `dependencies.go:637` — `var avgMs, p50Ms, p95Ms, p99Ms *float64`
- `dependencies.go:638` — `rows.Scan(&r.System, &r.Instance, &r.DBName, &r.SpanCount, &r.ErrorCount, &avgMs, &p50Ms, &p95Ms, &p99Ms)`
- `dependencies.go:643` — add `r.P50Ms = safeF(p50Ms)` / `r.P95Ms = safeF(p95Ms)`
- `dependencies.go:32` — add `P50Ms float64 \`json:"p50DurationMs"\`` / `P95Ms float64 \`json:"p95DurationMs"\`` after `P99Ms`
- Zero extra scan cost: same `quantilesTDigestMerge` subexpression, CH evaluates it once (the v0.8.364 messaging precedent, `dependencies.go:862-864`).
- ⚠️ `discoverReceiverInstances` (`dependencies.go:752-803`) emits `DBInstance` rows from `metric_points` with no quantiles. Those rows already carry `P99Ms=0` and will carry `P50Ms=0`/`P95Ms=0`. They are tagged `Source="receiver"` and the frontend already badges them — acceptable, but do not let the new columns imply real latency for them.

**Frontend (required, or the fields ship invisible):**
- `frontend/src/lib/types.ts:260` — add `p50DurationMs?: number; p95DurationMs?: number;`
- `pages/Databases.tsx:88-99` `toRow` copies fields **explicitly** — add `p50DurationMs: d.p50DurationMs, p95DurationMs: d.p95DurationMs,` or the table never sees them.
- `components/DependenciesTable.tsx:199-201` — the P50 column is currently gated `kind === 'queue'`. Widen the gate (or drop it) and add the P95 column alongside; `:465-472` add/ungate the matching `<td>`s.

### D-b: drawer aggregate P95 (`GetDatabaseDetail`)
🔴 Verdict was **REFUTED** for "change `:241` to index 2" — that would put p95 into the field labelled P99 in `DetailDrawer.tsx:154`, a silent 5-40% under-report exactly on the fat-tailed DBs an SRE opens the drawer for. **Corrected instruction: keep index 3, add index 2 as a new column.**
- `dependencies.go:241` — add `arrayElement(..., 2) / 1e6 AS p95_ms` before the p99 line (index 3 stays)
- `dependencies.go:160` — add `P95Ms float64 \`json:"p95DurationMs"\``
- `dependencies.go:235` var decl / `:247` Scan / `:252` assign — widen in SELECT order
- `frontend/src/lib/types.ts:352` — add `p95DurationMs?: number;`
- `DetailDrawer.tsx:154` — add a P95 `<Stat/>`
- Caller-table half is **D-b is coupled to M-d** — `DBCallerBreakdown` is shared; ship M-d and the `:265` half in the same commit.

### D-c: `/databases/slow-queries` P50 — MUST touch both builders
Index **1** on the MV path survived. But `SlowQueryRow.P50Ms` would be a hard `0` on the raw fallback, and `DBQueryStat` (`dbqueries.go:21-42`) is *also* embedded by `GetTopDBQueries` (raw-only, `dbqueries.go:310`).
- `dbqueries.go:140` — add `arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,` **before** the p95 line; `:236` scan gains `&p50` at the matching position; `:244` gains `r.P50Ms = safeF(&p50)`
- `dbqueries.go:97` — raw builder `slowQueriesGlobalSQL` must gain `quantile(0.5)(duration / 1e6) AS p50_ms,` in the same relative position; `:182` Scan widened
- `dbqueries.go:310` — `GetTopDBQueries` shares the struct; add the same `quantile(0.5)` + Scan slot, or `/service` top-statements renders P50=0.00ms
- `DBQueryStat` (`dbqueries.go:31`) — add `P50Ms float64 \`json:"p50Ms"\`` above `P95Ms`
- Existing tests that pin these two builders **must be updated**: `dbqueries_mv_test.go:46`, `dbqueries_sql_test.go`
- Frontend: `SlowQueries.tsx:266` column block + the `useDataTable` COLS

### D-d (OPTIONAL, medium value): `db_trends` P95 series
Index **2** survived at `db_trends.go:100`. Cost: the trend payload is per-5m-bucket × up to 200k rows, so each column widens the response measurably.
- `db_trends.go:100` — add index-2 projection; `:121` var; `:123` Scan; `:139-142` `DBTrendPoint`; `:154` `CurP95Ms`
- `db_trends.go:16-21` `DBTrendPoint.P95Ms`, `:43-48` `DBTrend.CurP95Ms`
- `frontend/src/lib/types.ts:278` + `:300`
- **Only ship if the sparkline/health gauge will actually plot it.** Otherwise this creates a third dead payload.

---

## 5. `/endpoints` — 🔴 THE DANGEROUS ONE

### Facts
- MV path `GetEndpointsMV` reads 4-wide states. `endpoints.go:456/457/458` = indices 1/3/4. **Index 2 (= 0.9) is produced by all three spanmetrics tiers and read by nothing on this page.** That part of the brief is correct.
- Raw path `getEndpointsRaw` builds its OWN 3-wide state at `endpoints.go:744` — `quantilesTDigestState(0.5, 0.95, 0.99)`. **0.9 is absent entirely.** Reads at `:759/760/761` = 1/2/3, all correct today.
- Dispatch is invisible to the operator: `GetEndpoints` (`endpoints.go:279-281`) routes to raw whenever `q.forcesRaw()` — `q.Cluster != "" || q.Env != ""` (`endpoints.go:159-161`). The global Topbar env picker is a daily-use control since the v0.8.379-387 env-separation work, i.e. **the prod configuration**.
- `endpointsOrderBy` (`endpoints.go:223-241`) is explicitly shared by both paths (comment at `:221-222`).

### The three ways a naive P90 patch fails, all silent
| Naive edit | Result on the raw path |
|---|---|
| Add index 2 to both SELECTs | Returns **p95 labelled P90**. Monotonic, plausible, ~10-40% high on log-normal latency. P90 and P95 columns become byte-identical. No error. |
| Add index 4 to the raw SELECT | `arrayElement` on a constant out-of-range index returns the type **default 0.0** — column reads "0 ms" everywhere. Also collapses `impact` (`endpoints.go:240` = `calls * p99_ms * (1+err)`) if p99 is what shifted. |
| Widen `:744` to 4-wide but leave `:760`/`:761` at 2/3 | **Regresses two already-correct columns**: P95 silently becomes p90, P99 silently becomes p95. |

Also: adding `"p90Ms": "p90_ms"` to `endpointsOrderBy:232` while only one SELECT has the alias → CH `Unknown identifier: p90_ms` → the whole table 500s on the filtered path. And `GetEndpointsMV` has **no empty/error fallback** (`endpoints.go:476-479`), so it is a hard failure, not a degraded column.

### Ship shape A — RECOMMENDED (full parity, one atomic commit)
The raw widening costs nothing operationally: `endpoints.go:744` is an inline CTE over raw `spans`, **not a stored MV** — no DDL, no migration, no rolling-deploy read-error window, no `dropCombinedMV` dance.

All five SQL edits in **one commit**:
1. `endpoints.go:744` → `quantilesTDigestState(0.5, 0.9, 0.95, 0.99)(duration) AS q_state,`
2. `endpoints.go:759` → merge list → `(0.5, 0.9, 0.95, 0.99)`, index stays **1** (p50)
3. `endpoints.go:760` → merge list widened, index **2 → 3** (p95) ← *this is the shift the adversarial pass correctly refuted in isolation; it is valid ONLY together with edit 1*
4. `endpoints.go:761` → merge list widened, index **3 → 4** (p99) ← same
5. New `p90_ms` line at index **2** in BOTH SELECTs: after `endpoints.go:456` and after the new `:759`

Then:
- `endpoints.go:36` — add `P90Ms float64 \`json:"p90Ms"\`` next to `P50Ms`/`P95Ms`
- `endpoints.go:485` + `:489` + `:496` (MV scan) and `:792` + `:796` + `:804` (raw scan) — insert the pointer at the matching SELECT position. **Scan is positional; a missing pointer shifts every later column, including the three `[]float64` sparklines.**
- `endpoints.go:232` — add `"p90Ms": "p90_ms",` to the whitelist (safe only after both aliases exist)
- `frontend/src/lib/types.ts:2609` — `p90Ms?: number;` (optional, same rationale as the `p50Ms?`/`p95Ms?` comment at `:2606-2608`)
- `pages/Endpoints.tsx:77` — insert `{ id: 'p90Ms', label: 'P90', sortValue: r => r.p90Ms ?? 0, numeric: true, width: 72 },` between the P50 and P95 entries
- `pages/Endpoints.tsx:94` — add `'p90Ms'` to `SORT_KEYS` (must stay in lockstep with `endpointsOrderBy` — the file says so at `:63-65`)
- `pages/Endpoints.tsx:566` — add `{visibleCols.has('p90Ms') && <td className="num mono">{fmtMs(r.p90Ms)}</td>}`. `ALL_COL_IDS` (`:101`) is derived from `ENDPOINT_COLS`, so the `?cols=` codec picks it up automatically.
- `fmtMs` (`pages/Endpoints.tsx:1063`) already returns `'—'` for `undefined` — the honest fallback is free.

### Ship shape B — MV-only (if you refuse to touch raw SQL)
**Then `p90Ms` MUST NOT be a bare `float64`.** Declare it `*float64` with `json:"p90Ms,omitempty"` so the raw path omits it from the JSON, `r.p90Ms` is `undefined` in TS, and `fmtMs` renders `—`. And **do NOT add `"p90Ms"` to `endpointsOrderBy:232`** or to `SORT_KEYS:94` — sorting by P90 would 500 the filtered view. A non-sortable column is the honest cost of shape B.

🔴 **A bare `float64` P90 with an MV-only SELECT renders `0 ms` on every env/cluster-filtered row — indistinguishable from "this endpoint is fast". That is the single worst outcome available here. Do not ship it.**

### Tier note (verified, no divergence)
`endpointsSparkGrid` (`endpoints.go:347-356`) returns only `spanmetrics_1m` or `spanmetrics_10s`; both are 4-wide, so index 2 = 0.9 on either. `spanmetrics_1s` is **unreachable and must stay so**: it drops `http_route` (`store.go:2429-2430`) while the endpoints WHERE hard-requires `http_route != ''` (`endpoints.go:415`) — wiring it in would 500 with `Unknown identifier`.

---

## 6. Test matrix

Coremetry rule: every branch. `GetEndpointsMV`/`getEndpointsRaw` build SQL inline, so **extract the quantile projection blocks into pure builders first** (e.g. `endpointsQuantileProjMV() string` / `endpointsQuantileProjRaw() string`) — that is the only way to table-test them without a live CH. Precedent for the alternative (source-text scan): `tdigest_migration_test.go:19-33`.

### T1 — `/endpoints` path × shape × tier (new file `endpoints_quantile_index_test.go`)
| # | Path | `BySignature` | Tier | Asserts |
|---|---|---|---|---|
| 1 | MV | off | `spanmetrics_1m` | SELECT contains `(0.5, 0.9, 0.95, 0.99)` **and** exactly `,1)`→p50_ms, `,2)`→p90_ms, `,3)`→p95_ms, `,4)`→p99_ms |
| 2 | MV | on | `spanmetrics_1m` | same 4 ordinals survive the `opSigWrap` path projection; `opSigArgs()` placeholders precede the bucket args |
| 3 | MV | off | `spanmetrics_10s` | `endpointsSparkGrid(windowSec, fromAge)` returns `spanmetrics_10s` for `windowSec/SparklineBuckets < 60 && fromAge <= spanmetrics10sSafeAgeSec`; ordinals identical to #1 |
| 4 | MV | — | `spanmetrics_1s` | **negative test**: `endpointsSparkGrid` NEVER returns `spanmetrics_1s` (it has no `http_route`; wiring it would 500) |
| 5 | raw | off | `spans` | state is `quantilesTDigestState(0.5, 0.9, 0.95, 0.99)` **and** merges are 1/2/3/4 → p50/p90/p95/p99 (shape A only) |
| 6 | raw | on | `spans` | same, with `pathProj` = signature wrap |
| 7 | both | — | — | **lockstep invariant**: the state/MergeState arg list and every `-Merge` arg list in the same query are byte-identical strings |
| 8 | — | — | — | `endpointsOrderBy("p90Ms","desc") == "ORDER BY p90_ms DESC, service_name ASC, path ASC"` — add a row to `endpoints_sort_test.go:38-40` |
| 9 | both | — | — | the alias `p90_ms` appears in BOTH SELECT strings (guards the shared-mapper 500) |
| 10 | both | — | — | Scan-arity: count SELECT top-level projections == count of `rows.Scan` args (catches the positional shift) |

### T2 — `/databases`
| # | Path | Asserts |
|---|---|---|
| 11 | `GetDatabases` MV | `db_summary_5m` SELECT has indices 1/2/3 → p50/p95/p99, and `countIfState`↔merge finaliser unchanged |
| 12 | `GetDatabaseDetail` agg | `db_caller_summary_5m`, p99 stays at **3**, new p95 at **2** |
| 13 | `GetDatabaseDetail` callers | same ordinals; both `DBCallerBreakdown` producers (`:265` DB and `:450` messaging) selected the new column — the shared-struct guard |
| 14 | slow-queries **MV** | extend `dbqueries_mv_test.go:46`: `..., 1)`→p50_ms, `,2)`→p95_ms, `,3)`→p99_ms |
| 15 | slow-queries **raw** | extend `dbqueries_sql_test.go`: `quantile(0.5)(duration / 1e6)` present, alias order matches the Scan |
| 16 | dispatch branches | `slowQueriesUseMV(true, t-10m, t)=true`; `(true, t-2m, t)=false`; `(false, t-10m, t)=false` — and both resulting SQL strings expose `p50_ms` |
| 17 | `GetTopDBQueries` | raw-only third branch also emits `p50_ms` (shares `DBQueryStat`) |
| 18 | `db_trends` (if D-d ships) | index 2 → p95, index 3 → p99 unchanged, `countIfMerge` preserved |

### T3 — `/messaging`
| # | Asserts |
|---|---|
| 19 | `messaging_summary_5m` overview: 1/2/3 unchanged |
| 20 | `messaging_caller_summary_5m` detail agg: p99 stays **3**, new p50=**1**, p95=**2** |
| 21 | `messaging_e2e.go:110` keeps plain `quantiles()` + **0-based** Go slice `qs[0..2]` — pin the divergent convention so nobody "harmonizes" it into an `arrayElement` |
| 22 | vitest, `DependenciesTable`: `kind='queue'` column set contains `p50`,`p95` and **not** `trend`; `kind='db'` contains `trend` and (post D-a) `p50`,`p95` |
| 23 | vitest: with `kind='queue'`, `api.dbTrends` is **not** called |

### T4 — cross-cutting guard (highest ROI, write this one first)
A single table-driven test that regex-scans `internal/chstore/*.go` for `quantilesTDigestMerge\(([^)]*)\)\([a-z_]+\), (\d+)\)[^A]*AS (\w+)` and asserts the ordinal maps to the quantile named in the alias, given the arg list in the same expression. Allowlist the two `fmt.Sprintf("%d")` sites (`evaluator_reads.go:248/281`, `metricresolve.go` is literal). This catches every future family-crossing edit, which is the whole bug class.

---

## 7. Ship order

Aggressive cadence — one logical unit per tag, never batched (CLAUDE.md + `feedback-release-cadence-aggressive`).

| # | Tag | Slice | Risk | Gate |
|---|---|---|---|---|
| 1 | v0.9.257 | **M-a** — gate `/messaging` Trend column + its fetch (live bug L1: dead column + wasted 200k-row query) | none | vitest T22/T23 |
| 2 | v0.9.258 | **M-b** — `/messaging` P95 column (frontend only, zero CH cost, closes dead-payload L2) | none | tsc + T22 |
| 3 | v0.9.259 | **D-a** — `/databases` overview P50+P95 (backend 3 lines + `toRow` + column gate) | low | T11 |
| 4 | v0.9.260 | **M-c + M-d + D-b** — drawer P50/P95 for messaging + databases + the shared `DBCallerBreakdown` P95. **MUST be one commit** — the struct is shared and the field is non-pointer | med | T12/T13/T20 |
| 5 | v0.9.261 | **D-c** — slow-queries P50 across MV + raw + `GetTopDBQueries` | med | T14-T17 |
| 6 | v0.9.262 | **T4 guard test** + T1 pure-builder extraction, no behaviour change | none | `go test ./...` |
| 7 | v0.9.263 | **`/endpoints` P90, ship shape A** (5 SQL edits + 2 Scans + whitelist + FE), last and alone | 🔴 high | full T1 |
| — | — | **D-d** (`db_trends` P95 series) — hold until someone commits to plotting it | — | — |

### Must-NOT-ship list
- 🔴 **`/endpoints` P90 as an MV-only `float64`.** Renders `0 ms` on every cluster/env-filtered row. If shape A is not acceptable, use shape B: `*float64` + `json:",omitempty"` + optional TS field → `fmtMs` renders `—`, and **no** `endpointsOrderBy`/`SORT_KEYS` entry.
- 🔴 **`endpoints.go:760`/`:761` index shifts on their own.** Valid *only* in the same commit as the `:744` widening. Alone they silently relabel P95→p90 and P99→p95.
- 🔴 **`"p90Ms"` in `endpointsOrderBy:232` before both SELECTs carry the alias.** 500s the entire table on the filtered path (no fallback exists — `endpoints.go:476-479`).
- 🔴 **Repointing any existing p99 read** (`dependencies.go:241`, `:265`, `:423`, `:450`, `:616`, `db_trends.go:100`) from index 3 to index 2. Every "add p95" is an **additive column**, never a repoint.
- 🔴 **`DBCallerBreakdown.P95Ms` added to the struct but selected in only one of the two queries.** Non-pointer float → the un-edited path renders `0.0ms`.

---

## 8. Open questions (not assumptions)

1. **CH acceptance of the widened raw state.** `endpoints.go:744` going 3→4 quantiles is a pure-SQL edit with no DDL, but I did not execute it against a live ClickHouse. The type-compatibility reasoning (merge params must match state params, all in the same query text) is sound, but the first deploy should be watched. No migration risk either way — it is an inline CTE, not a stored MV.
2. **Marginal cost of the 4th centroid on the raw path.** TDigest state size is compression-driven, not quantile-count-driven, so I expect ~0 — but I did not benchmark it, and per `feedback-perf-benchmark-discipline` a single ad-hoc timing would lie. If you want a number, take a `query_log` median before/after on the same window.
3. **`countMerge` over `countIfState`.** `dependencies.go:238/262/420/447/613/859/917`, `repo.go:985/1055`, `sysstats.go:239/310`, `evaluator.go:1262/1281` all call `countMerge` on states declared `countIfState(status_code='error')` (`store.go:2291/2506/2548/2792/2828/...`), while `db_trends.go:99` and `summary.go:450` use `countIfMerge` and `db_trends.go:88-91` carries a comment saying `countMerge` "would silently read the wrong aggregate". Both forms are shipping in prod today, so CH evidently accepts both — but **I could not verify whether the counts agree.** Out of scope for this workstream; worth its own probe against live CH. If they diverge, error counts on `/databases` and `/messaging` are wrong today.
4. **Whether P50/P95 on `/databases` is actually wanted in the grid.** The columns are currently gated `kind === 'queue'` (`DependenciesTable.tsx:199-201`) — a deliberate v0.8.364 choice ("the DB grid keeps its existing shape"). D-a widens that gate. Given `feedback-operations-table-classic` / `feedback-tables-over-cards`, adding two columns to a daily triage table is a layout change the operator may reject. **Confirm before shipping D-a**, or ship it behind the `?cols=` codec default-hidden.
5. **D-d payload budget.** I did not measure the actual `/api/databases/trends` response size at prod cardinality. `LIMIT 200000` × one more float per bucket is the upper bound; whether that matters depends on real row counts.
6. **`compare=prior` for the new percentiles.** `mergeMessagingPrior` (`api_databases.go:162-179`) carries only `PriorP50Ms`/`PriorP99Ms`; `/endpoints` prior merge (`api.go:2712`, `:2727-2731`) carries only Calls/Errors/AvgMs/P99Ms. Delta badges for the new columns are a separate, optional follow-up — not in scope, and rendering one without a populated prior would show a false 0-baseline delta.