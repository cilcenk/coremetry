---
name: scale-audit
description: Periodic regression catcher for Coremetry's production scale. Use when the user asks for a "scale audit", "performance sweep", or quarterly UX-health check. Surveys frontend pickers, tables, polling, cache keys, CH bounds, render traps, permission gating, AND the architectural invariants (MV reads, system_settings hydration, logstore routing, ai_calls attribution) against the 1000s-services / 10000s-ops / 1B-spans-day constraint in CLAUDE.md.
---

# /scale-audit — production-scale regression catcher

Coremetry runs at scale (see CLAUDE.md). Every UI surface +
backend cache key has to handle that without freezing the page
or serving cross-tenant cached responses. This skill runs the
checks that have surfaced real bugs in the past so regressions
don't accumulate between explicit audits.

The yardstick is CLAUDE.md's "Hard constraints", "Performance
budgets", and "Architectural invariants" sections.

## When to run

- Quarterly cadence — operator-driven
- After a multi-week feature sprint (a lot of new surfaces landed)
- When the operator reports a specific scale symptom and wants
  to know "what else is like this"

## Steps

Run each check, collect findings, present a single ranked report
at the end. Each finding should reference `file:line` so the
operator can click through. Use `Explore` subagent for the
broad greps — keeps the main session's context window clean.

### 1. Eager-loaded picker scan

Find any `<Combobox options={…}>` where the options array is
populated by an eager `api.X()` call returning the FULL catalogue
(no `q` / `limit` param). At scale, eager catalogues are the
single biggest cause of TTFI regression.

```
grep -rn "<Combobox options=" frontend/src
```

For each hit, look at how the array is populated. If the source is
`api.services()`, `api.operations()`, `api.metricNames()`, or any
similar "list everything" call without a query parameter — flag it.

**Expected pattern:** `ServicePicker`, `OperationPicker`,
`MetricNamePicker` (server-side debounced search). Anything that
hand-rolls eager loading is the regression.

### 2. Unbounded table scan

Find tables that render `array.map(...)` of >100 elements without
virtualisation OR `content-visibility: auto` OR server-side
pagination.

```
grep -rn "rows\.map\|items\.map\|data\.map" frontend/src/pages
```

For each match, check:
- Is the array paginated server-side (look for `limit` in the
  fetch)?
- Does the row CSS class use `content-visibility: auto`?
- Is the array bounded by construction (top-N from backend)?

If none of these, flag it — at 5K+ rows the page will lock.

### 3. setInterval / refetchInterval + document.hidden audit

```
grep -rn "setInterval\|refetchInterval" frontend/src
```

For each:
- **`setInterval` must pause when `document.hidden`** (CLAUDE.md
  performance budget). The pattern is:
  ```js
  setInterval(() => { if (!document.hidden) fetchOnce(); }, 30_000);
  ```
  If the callback runs unconditionally — flag it (v0.5.248
  introduced this rule across 4 surfaces).
- `refetchInterval` should have a `staleTime` that matches or
  slightly trails it (re-mount within window shouldn't double-
  fetch). Look for the pair; if `staleTime` is missing or much
  smaller, flag it.
- Polls under 10s on anything except `/api/health` (5s) should
  be questioned — usually means the endpoint should be SSE/
  WebSocket instead.

### 4. Cache key audit

```
grep -rn "serveCached" internal/api
grep -rn "fmt.Sprintf.*key" internal/api
```

For each cache key construction, verify:
- All inputs that affect the result are part of the key
- Sets/maps are hashed (sorted + FNV digest), not just summarised
  by length (`exN=%d` is the historical bug pattern — v0.5.187)
- Time-window inputs are bucketed (typically to the minute) so
  concurrent requests within the bucket share a round-trip

The "set length only" pattern is cache poisoning waiting to
happen — see v0.5.187 commit message for the historical incident.

### 5. ClickHouse query bounds + MV-bypass

```
grep -rn "max_execution_time\|LIMIT " internal/chstore
grep -rn "FROM spans\b\|FROM metric_points\b" internal/api internal/chstore
```

For each query that scans `spans` / `metric_points`:
- LIMIT present?
- `SETTINGS max_execution_time = N` clause?
- WHERE clause uses indexed columns (`time`, `service_name`)?
- Window-bounded by a `time >= ?` clause that can prune
  partitions?

**MV bypass check (CLAUDE.md invariant #3):** Hot read endpoints
must hit the matching pre-aggregate (`service_summary_5m`,
`operation_summary_5m`, `topology_edges_5m`, `db_summary_5m`,
`db_caller_summary_5m`, `topology_root_flows_5m`). If a read
endpoint does `SELECT ... FROM spans GROUP BY service_name`
when `service_summary_5m` already pre-computes it — flag it as
critical. Raw-spans aggregates at billion-row scale are a bug,
not a perf nit.

Anything that does `GROUP BY` without a LIMIT on the spans table
at billion-row scale is a tombstone.

### 6. Render-time recomputation traps

```
grep -rn "timeRangeToNs\b" frontend/src
```

For each usage:
- Is it inside a `useMemo([range])` or a stable hook return?
- Or is it being called in JSX / inside an IIFE on every render?

The latter pattern (re-evaluating `timeRangeToNs(range)` on every
render with `now()` ticking inside) causes useEffect-dependent
fetches to re-fire continuously — see v0.5.184 (Explore facets
spinner) for the historical incident.

### 7. Permission gating + audit consistency

For each new admin action / settings surface added since the
last audit:
- `api.go` route uses `auth.RequireRole(auth.RoleAdmin, …)` OR
  `auth.RequireAnyRole(editorRoles, …)`
- Every mutation handler calls `s.audit(r, "kind.action", ...)`
- Frontend button hides / disables based on `user.role`
- Viewer can still SEE the state (read-only chip), not blank

Spot-check 3-5 random Settings + per-row action surfaces.

### 8. Settings hydration + ai_calls attribution

Two newer invariants from CLAUDE.md worth checking:

**system_settings hydration on boot:**
```
grep -rn "LoadPersisted\|SavePersisted" internal/
```
Every config service (tempo, copilot, kibana, etc.) should
`LoadPersisted(ctx, store)` at boot in `main.go` and
`SavePersisted(...)` on admin PUT. Hand-rolled config tables /
hard-coded defaults that never hydrate from `system_settings` —
flag.

**Copilot attribution:**
```
grep -rn "copilot\.Explain\b\|copilotExplain\b" internal/api
```
Every Copilot route should go through `s.copilotExplain(r, ...)`
(records the `ai_calls` row for `/ai` attribution). Direct
`s.copilot.Explain(r.Context(), ...)` calls silently break the
attribution — flag.

**Logstore routing:**
```
grep -rn "chstore\..*Pattern\|chstore\..*Significant\|chstore\..*Templates" internal/anomaly internal/api
```
Detector / pattern / template queries must go through
`logstore.Store` (CountPatterns batched form, _msearch on ES,
tokenbf_v1 on CH). Direct `chstore` calls for these tie the
detector to one backend — flag.

## Output

Present findings as a single ranked report:

```
## Scale-audit — YYYY-MM-DD

### 🔴 Critical (ship a fix this sprint)
- [file.tsx:42](file://path) — operations Combobox eager-loads
  api.operations(), 10k-op service unreachable past top-500.
  Fix: swap to OperationPicker (existing component).
- [file.go:88](file://path) — cache key uses exN=len(set);
  cross-set poisoning at 2+ tenants.
  Fix: replace with FNV digest helper.
- [file.go:120](file://path) — `FROM spans GROUP BY service_name`
  on hot path. Fix: read service_summary_5m.

### 🟡 Risk (queue but not blocking)
- [file.tsx:200](file://path) — setInterval polls without
  document.hidden guard. Fix: wrap callback per CLAUDE.md.

### ✅ Clean
- All pickers using <X>Picker pattern (audit pass)
- All polls have matching staleTime
- ai_calls attribution wraps every Copilot route
```

Limit each section to top 10 findings; if you have more, surface a
count and offer to dump the rest on request. The operator triages
from the top.

## Don't

- Don't fix anything in the same call — this skill is REPORT only.
  Fixes are separate, deliberate commits per CLAUDE.md.
- Don't expand the scope beyond the 8 checks above. Adding "code
  smells" or "stylistic preferences" turns the audit into noise.
- Don't re-flag the same finding across runs without referencing
  the prior audit's commit/issue. If a finding wasn't fixed, that
  may be a deliberate defer.
