# Deployment Analysis Report — Design

## Problem

After a deployment, an operator wants one holistic view answering: "what
broke because of this deploy, and is it still broken?" Today they'd have to
manually cross-reference the Problems page, the Anomalies page, and the
Exceptions inbox, filtering each by time and by every service individually.
Coremetry has no operator-settable "deployment happened at X" marker and no
composed multi-section report of any kind.

## Goals

- One report, generated on demand, covering the whole fleet — no per-service
  selection.
- Operator supplies the deployment timestamp manually (no deploy-detection
  dependency).
- Only services with problems that (a) started after that timestamp and (b)
  are still open right now appear in the report.
- For each included service, show the anomalies and new errors that started
  after the deploy and are still active — plus a before/after health
  (RED) comparison.
- Per-problem AI review, generated on demand (not bulk, not automatic).
- Report state is pure URL state — reopening the URL with the same
  timestamp recomputes fresh; nothing is snapshotted or persisted.

## Non-goals

- No PDF/export/print handling.
- No new "deployment marker" data model or deploy-detection changes —
  reuses existing `Deploy`/`Rollout` inference only implicitly (not at all,
  in v1); the timestamp is whatever the operator types in.
- No auto-generated AI review for every problem in the report.
- No report history/snapshot persistence.
- No per-service selection UI — the report is always fleet-wide.

## Inclusion algorithm

A service qualifies for the report if and only if:

```
EXISTS Problem WHERE Problem.Service = service
  AND Problem.StartedAt >= since
  AND Problem.Status IN ('open', 'acknowledged')   -- i.e. ResolvedAt IS NULL
```

This single filter on the existing `problems` table captures both "started
after deployment" and "still exists at report time" in one pass. No new
table, no new column — implementation extends `ProblemFilter`
(`internal/chstore/problem.go`) with a new optional `StartedAfter *int64`
field, pushed into the existing `ListProblems` SQL WHERE clause alongside
the status filter, rather than filtering post-query in Go.

Anomalies and new errors do **not** independently qualify a service — a
service with an active anomaly or new error since the deploy but no
qualifying open Problem is excluded from the report entirely. This was a
deliberate, confirmed choice: Problems are the trigger, anomalies/errors are
supporting evidence attached to a service that already qualified.

For each qualifying service, pull two more signals scoped to that service
only:

- **Anomalies**: `AnomalyEvent` rows via `ListAnomalyEvents` filtered to
  `Service = service AND StartedAt >= since`, kept only if currently active
  (`LastSeen >= now - 10m`, the existing computed-status rule — no new
  status field).
- **New errors**: `ExceptionGroup` rows filtered to
  `Service = service AND FirstSeen >= since AND State IN ('new',
  'acknowledged')` (excludes resolved/ignored).

## RED delta window

Each qualifying service gets a before/after comparison pulled from
`service_summary_5m` (the same MV `/api/services` already reads):

- **After window**: `[since, now]`
- **Before window**: `[since - (now - since), since]` — mirrors the after
  window's duration, so a deploy from 10 minutes ago compares 10 minutes of
  before against 10 minutes of after, and a deploy from 3 days ago compares
  3 days against 3 days. Avoids a hardcoded lookback that's wrong at either
  extreme.

Metrics shown: error rate, P99 latency, throughput (span count / sec) —
`Before` and `After` structs with the same three fields.

Edge case: if `since` is so recent that the before-window start would
predate any retained data, the before-window simply returns whatever data
exists in range (ClickHouse naturally returns fewer rows; no special
handling needed).

## Backend

New file: `internal/api/deployment_report.go`.

Route: `GET /api/deployment-report?since=<unix_ms>` (registered alongside
the other `/api/*` routes in `api.go`, following the existing route-table
convention).

```go
type REDStats struct {
    ErrorRate  float64 `json:"errorRate"`
    P99Ms      float64 `json:"p99Ms"`
    Throughput float64 `json:"throughput"` // spans/sec
}

type ServiceReportSection struct {
    Service    string                    `json:"service"`
    Health     string                    `json:"health"` // red|yellow|green, reuse scoreHealth
    Before     REDStats                  `json:"before"`
    After      REDStats                  `json:"after"`
    Problems   []chstore.Problem         `json:"problems"`
    Anomalies  []chstore.AnomalyEvent    `json:"anomalies"`
    NewErrors  []chstore.ExceptionGroup  `json:"newErrors"`
}

type DeploymentReport struct {
    Since       int64                   `json:"since"`
    GeneratedAt int64                   `json:"generatedAt"`
    Services    []ServiceReportSection  `json:"services"`
}
```

Handler flow:
1. Parse+validate `since` (unix ms, required, must be in the past).
2. `ListProblems` with status open+acknowledged, filter to `StartedAt >=
   sinceNs` in Go (or extend `ProblemFilter`).
3. Group by `Service` → distinct qualifying service set. If empty, return
   `DeploymentReport{Services: []}` (200, not an error — "no regressions
   found" is a valid, good outcome).
4. For each qualifying service (bounded — realistically low cardinality
   since it's gated by open problems, not the whole fleet): fetch its
   anomalies, new errors, and RED before/after in parallel goroutines
   (existing pattern elsewhere in api.go for multi-query assembly — verify
   during implementation and follow whatever fan-out helper already
   exists, otherwise sequential is fine given the low expected N).
5. Compute `Health` via the existing `scoreHealth` used by `/api/services`.
6. Wrap the whole handler in `s.serveCached`, cache key = hash of `since`
   only, TTL ~30s (matches the Services page's cache TTL — this is a
   read-heavy, infrequently-changing-within-30s view, not a live poller).

Auth: `RequireAnyRole` (viewer/editor/admin) — read-only, no audit entry
(audit is for admin *writes* per CLAUDE.md; this endpoint writes nothing).

CH query bounds: every query already time-bounded by construction (`since`
→ `now`, or the before/after windows) — no new unbounded scan risk.

## AI review

No new backend work. The report renders real `Problem.ID` values from the
same `problems` table `/api/copilot/explain-problem/{id}` already reads.
Frontend renders the existing `<CopilotExplain kind="problem" id={p.ID}/>`
component per problem row — clicking it calls the existing endpoint, which
already runs the full enrichment chain (deploy correlation, root-cause
fusion, correlation context) documented in `copilotExplainProblem`
(`internal/api/api.go`). No bulk/automatic generation.

## Frontend

New page: `frontend/src/pages/DeploymentReport.tsx`.

- Route `/deployment-report`, added to `App.tsx`'s lazy route table.
- Sidebar entry in `Sidebar.tsx`, command palette entry in
  `CommandPalette.tsx` — the standard four-touchpoint registration.
- `since` lives in the URL (`?since=<unix_ms>`), written with
  `replace: true`, per the "URL is source of truth" convention. A
  `Field`-wrapped `<input type="datetime-local">` sets it; no server-side
  suggestion list in v1 (operator types the timestamp they know is
  correct).
- "Generate Report" is an explicit action (button) that triggers the fetch
  when `since` is present in the URL — no auto-fetch on every keystroke,
  no polling once loaded (matches ES-cost / fetch-on-open discipline;
  this is a point-in-time computed view, not a live dashboard).
- Empty state: `since` set but zero qualifying services → `<Empty/>` with
  a "no open problems since this deployment" message (a good-news state,
  not an error).
- Per-service section: health badge, RED before/after (small inline
  stat pair, not a full chart — uPlot not required for two numbers), then
  three `useDataTable`-backed tables (Problems, Anomalies, New Errors)
  following the `SlowQueries.tsx` template. Each Problem row has a
  `<CopilotExplain kind="problem" id={...}/>` button reused as-is.
- Loading/error states: standard `<Spinner/>` / error banner per the
  ship-checklist.

Types: add `DeploymentReport`, `ServiceReportSection`, `REDStats` to
`lib/types.ts`. Client method `getDeploymentReport(sinceMs)` in
`lib/api.ts`.

## Testing

- `go test ./internal/api/...` — table-driven test for the inclusion
  filter (problem started before `since` → excluded; started after but
  resolved → excluded; started after and still open → included) and for
  the RED before/after window math (symmetric window edge cases:
  `since` == now, `since` far in the past).
- `npx tsc --noEmit` (frontend gate).
- `go build ./...` (backend gate).
- Manual test doc (see below) — this feature is inherently
  data-dependent (needs real problems/anomalies/errors in a local CH
  instance), so automated coverage is limited to the pure inclusion/window
  logic; end-to-end behavior is verified manually.

## Manual test doc

A `docs/DEPLOYMENT-REPORT-TESTING.md` walkthrough will be written after
implementation, covering: seeding a local deploy scenario via the demo
generators (`cmd/demo/`), triggering a problem/anomaly/new-error after a
known timestamp, generating the report against that timestamp, and
verifying the inclusion/exclusion behavior and AI review button.
