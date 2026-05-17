---
name: scale-audit
description: Periodic regression catcher for Coremetry's production scale. Use when the user asks for a "scale audit", "performance sweep", or quarterly UX-health check. Surveys frontend pickers, tables, polling, and cache keys against the 1000s-services / 10000s-ops / 1B-spans-day constraint in CLAUDE.md.
---

# /scale-audit — production-scale regression catcher

Coremetry runs at scale (see CLAUDE.md). Every UI surface +
backend cache key has to handle that without freezing the page
or serving cross-tenant cached responses. This skill runs the
checks that have surfaced real bugs in the past so regressions
don't accumulate between explicit audits.

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

### 3. setInterval / refetchInterval audit

```
grep -rn "setInterval\|refetchInterval" frontend/src
```

For each:
- `setInterval` should pause when `document.hidden`. If the
  callback doesn't check visibility, flag it.
- `refetchInterval` should have a `staleTime` that matches or
  slightly trails it (re-mount within window shouldn't double-
  fetch). Look for the pair; if `staleTime` is missing or much
  smaller, flag it.
- Polls under 10s on anything except `/api/health` should be
  questioned — usually means the endpoint should be SSE/WebSocket
  instead.

### 4. Cache key audit

```
grep -rn "serveCached" internal/api
grep -rn "fmt.Sprintf.*key" internal/api
```

For each cache key construction, verify:
- All inputs that affect the result are part of the key
- Sets/maps are hashed (sorted + FNV digest), not just summarised
  by length (`exN=%d` is the historical bug pattern)
- Time-window inputs are bucketed (typically to the minute) so
  concurrent requests within the bucket share a round-trip

The "set length only" pattern is cache poisoning waiting to
happen — see v0.5.187 commit message for the historical incident.

### 5. ClickHouse query bounds

```
grep -rn "max_execution_time\|LIMIT " internal/chstore
```

For each query that scans `spans` / `metric_points`:
- LIMIT present?
- max_execution_time SETTINGS clause?
- WHERE clause uses indexed columns (`time`, `service_name`)?
- Window-bounded by a `time >= ?` clause that can prune
  partitions?

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

### 7. Permission gating consistency

For each new admin action / settings surface added since the
last audit:
- `api.go` route uses `auth.RequireRole(auth.RoleAdmin, …)` OR
  `auth.RequireAnyRole(editorRoles, …)`
- Frontend button hides / disables based on `user.role`
- Viewer can still SEE the state (read-only chip), not blank

Spot-check 3-5 random Settings + per-row action surfaces.

## Output

Present findings as a single ranked report:

```
## Scale-audit — YYYY-MM-DD

### 🔴 Critical (ship a fix this sprint)
- [file.tsx:42](file://path) — operations Combobox eager-loads
  api.operations(), 10k-op service unreachable past top-500.
  Fix: swap to OperationPicker (existing component).

### 🟡 Risk (queue but not blocking)
- [file.go:88](file://path) — cache key uses exN=len(set);
  potential cross-set poisoning at 2+ tenants.
  Fix: replace with FNV digest helper.

### ✅ Clean
- All pickers using <X>Picker pattern (audit pass)
- All polls have matching staleTime
```

Limit each section to top 10 findings; if you have more, surface a
count and offer to dump the rest on request. The operator triages
from the top.

## Don't

- Don't fix anything in the same call — this skill is REPORT only.
  Fixes are separate, deliberate commits per CLAUDE.md.
- Don't expand the scope beyond the 7 checks above. Adding "code
  smells" or "stylistic preferences" turns the audit into noise.
- Don't re-flag the same finding across runs without referencing
  the prior audit's commit/issue. If a finding wasn't fixed, that
  may be a deliberate defer.
