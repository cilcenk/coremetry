# Deployment Analysis Report Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a fleet-wide "deployment analysis report" — given an operator-supplied deployment timestamp, show every service that still has an open Problem which started after that timestamp, plus that service's still-active anomalies, still-open new errors, and a before/after RED comparison, with on-demand AI review per problem.

**Architecture:** One new read-only backend endpoint (`GET /api/deployment-report`) that composes four existing, already-tested ClickHouse read primitives (`OpenProblemsSnapshot`, `ListAnomalyEvents`, `ListExceptionGroups`, `GetServicesAggFilteredIn`) plus the existing `openProblemCountsCached`/`scoreHealth` pair — no new ClickHouse schema, no new MV. One new frontend page reusing the existing `useDataTable`, `Field`, `CopilotExplain`, `Topbar`, `Spinner`/`Empty` primitives, registered through the standard four-touchpoint (App.tsx/Sidebar.tsx/CommandPalette.tsx/i18n.ts) convention.

**Tech Stack:** Go 1.22+ (`internal/api`, `internal/chstore`), ClickHouse (read-only, via existing store methods), React + TypeScript (`frontend/src/pages`, `frontend/src/lib`).

## Global Constraints

- Single-binary release — this feature adds no new binary, mode, or service; it lives entirely inside the existing `internal/api` / `frontend` trees.
- CH query on `spans`/`metric_points`-derived data must be time-bounded — satisfied transitively: every store method this plan calls (`OpenProblemsSnapshot`, `ListAnomalyEvents`, `ListExceptionGroups`, `GetServicesAggFilteredIn`) already carries its own bounded WHERE + `SETTINGS max_execution_time`; this plan adds no new raw SQL.
- Every data table > 100 rows or otherwise tabular must use `useDataTable` (sort + resize) — never hand-rolled.
- `timeRangeToNs`-style absolute timestamps are unix **nanoseconds** everywhere in this codebase (confirmed via `parseTime`, `frontend/src/lib/utils.ts:timeRangeToNs`) — the report's `since` param follows this convention, NOT unix milliseconds (this supersedes the `unix_ms` wording in the design doc — same intent, corrected to match the codebase's actual convention).
- Admin write = audit entry — N/A, this endpoint is read-only, no audit call needed.
- No new schema for user-saved state — N/A, nothing is persisted (confirmed: live view only, per design doc).
- `npx tsc --noEmit` (frontend) and `go build ./...` (backend) must both pass before this is considered done; `go test ./...` must pass including the new tests this plan adds.

---

## File Structure

- **Create** `internal/api/deployment_report.go` — response types, pure helper functions (window math, problem filtering), and the HTTP handler.
- **Create** `internal/api/deployment_report_test.go` — table-driven tests for the two pure helpers (no CH connection required, matches the `cache_key_test.go` pattern).
- **Modify** `internal/api/api.go` — one new route registration line.
- **Modify** `frontend/src/lib/types.ts` — add `DeploymentReport`, `ServiceReportSection`, `REDStats` interfaces near the existing `Problem`/`AnomalyEvent`/`ExceptionGroup` interfaces.
- **Modify** `frontend/src/lib/api.ts` — add `getDeploymentReport(sinceNs)` client method.
- **Create** `frontend/src/pages/DeploymentReport.tsx` — the report page.
- **Modify** `frontend/src/App.tsx` — lazy import + route.
- **Modify** `frontend/src/components/Sidebar.tsx` — nav entry in the Triage group.
- **Modify** `frontend/src/components/CommandPalette.tsx` — palette entry.
- **Modify** `frontend/src/lib/i18n.ts` — `nav.deploymentReport` key, English + Turkish.
- **Create** `docs/DEPLOYMENT-REPORT-TESTING.md` — manual local testing walkthrough (final task).

---

### Task 1: Backend pure helpers — window math + problem filtering (TDD)

**Files:**
- Create: `internal/api/deployment_report.go`
- Create: `internal/api/deployment_report_test.go`

**Interfaces:**
- Produces: `filterOpenProblemsSince(snapshot map[string]*chstore.Problem, sinceNs int64) []chstore.Problem` — returns qualifying problems (StartedAt >= sinceNs), sorted by `StartedAt` descending for determinism.
- Produces: `redComparisonWindow(sinceNs, nowNs int64) (beforeFrom, beforeTo, afterFrom, afterTo time.Time)` — symmetric before/after window boundaries.

- [ ] **Step 1: Write the failing tests**

```go
// internal/api/deployment_report_test.go
package api

import (
	"testing"
	"time"

	"coremetry/internal/chstore"
)

func TestFilterOpenProblemsSince_ExcludesStartedBeforeDeploy(t *testing.T) {
	sinceNs := int64(1000)
	snapshot := map[string]*chstore.Problem{
		"rule1|svcA": {ID: "p1", Service: "svcA", StartedAt: 500}, // before deploy
	}
	got := filterOpenProblemsSince(snapshot, sinceNs)
	if len(got) != 0 {
		t.Fatalf("expected 0 problems (started before deploy), got %d", len(got))
	}
}

func TestFilterOpenProblemsSince_IncludesStartedAfterDeploy(t *testing.T) {
	sinceNs := int64(1000)
	snapshot := map[string]*chstore.Problem{
		"rule1|svcA": {ID: "p1", Service: "svcA", StartedAt: 1500}, // after deploy
	}
	got := filterOpenProblemsSince(snapshot, sinceNs)
	if len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("expected 1 problem p1, got %+v", got)
	}
}

func TestFilterOpenProblemsSince_BoundaryIsInclusive(t *testing.T) {
	sinceNs := int64(1000)
	snapshot := map[string]*chstore.Problem{
		"rule1|svcA": {ID: "p1", Service: "svcA", StartedAt: 1000}, // exactly at deploy
	}
	got := filterOpenProblemsSince(snapshot, sinceNs)
	if len(got) != 1 {
		t.Fatalf("expected StartedAt == since to be included (inclusive boundary), got %d", len(got))
	}
}

func TestFilterOpenProblemsSince_SortedByStartedAtDesc(t *testing.T) {
	sinceNs := int64(1000)
	snapshot := map[string]*chstore.Problem{
		"rule1|svcA": {ID: "older", Service: "svcA", StartedAt: 1500},
		"rule2|svcA": {ID: "newer", Service: "svcA", StartedAt: 2500},
	}
	got := filterOpenProblemsSince(snapshot, sinceNs)
	if len(got) != 2 || got[0].ID != "newer" || got[1].ID != "older" {
		t.Fatalf("expected [newer, older] order, got %+v", got)
	}
}

func TestRedComparisonWindow_SymmetricDuration(t *testing.T) {
	// Deploy was 10 minutes ago.
	nowNs := int64(20 * time.Minute)
	sinceNs := int64(10 * time.Minute)
	beforeFrom, beforeTo, afterFrom, afterTo := redComparisonWindow(sinceNs, nowNs)

	afterDur := afterTo.Sub(afterFrom)
	beforeDur := beforeTo.Sub(beforeFrom)
	if afterDur != beforeDur {
		t.Fatalf("expected symmetric windows, before=%s after=%s", beforeDur, afterDur)
	}
	wantAfterDur := 10 * time.Minute
	if afterDur != wantAfterDur {
		t.Fatalf("expected after-window duration %s, got %s", wantAfterDur, afterDur)
	}
	if !afterTo.Equal(time.Unix(0, nowNs)) {
		t.Fatalf("expected afterTo == now, got %s", afterTo)
	}
	if !beforeTo.Equal(time.Unix(0, sinceNs)) {
		t.Fatalf("expected beforeTo == since (the deploy boundary), got %s", beforeTo)
	}
}

func TestRedComparisonWindow_DeployJustHappened(t *testing.T) {
	// since == now: after-window has zero duration, before-window mirrors it (also zero).
	nowNs := int64(5000)
	sinceNs := int64(5000)
	beforeFrom, beforeTo, afterFrom, afterTo := redComparisonWindow(sinceNs, nowNs)
	if !afterFrom.Equal(afterTo) {
		t.Fatalf("expected zero-width after window when since==now, got %s..%s", afterFrom, afterTo)
	}
	if !beforeFrom.Equal(beforeTo) {
		t.Fatalf("expected zero-width before window when since==now, got %s..%s", beforeFrom, beforeTo)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/... -run 'TestFilterOpenProblemsSince|TestRedComparisonWindow' -v`
Expected: FAIL — `filterOpenProblemsSince` and `redComparisonWindow` are undefined.

- [ ] **Step 3: Write the minimal implementation**

```go
// internal/api/deployment_report.go
package api

import (
	"sort"
	"time"

	"coremetry/internal/chstore"
)

// filterOpenProblemsSince narrows an OpenProblemsSnapshot map down to the
// problems that started at or after sinceNs — the report's inclusion gate
// (see docs/superpowers/specs/2026-07-17-deployment-analysis-report-design.md).
// Sorted StartedAt descending (most recent regression first) so the report
// doesn't depend on Go's randomised map iteration order.
func filterOpenProblemsSince(snapshot map[string]*chstore.Problem, sinceNs int64) []chstore.Problem {
	out := make([]chstore.Problem, 0, len(snapshot))
	for _, p := range snapshot {
		if p.StartedAt >= sinceNs {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

// redComparisonWindow returns symmetric before/after windows around the
// deploy timestamp: after = [since, now], before = [since - (now-since),
// since]. A deploy from 10 minutes ago compares 10 minutes of before
// against 10 minutes of after; a 3-day-old deploy compares 3 days against
// 3 days. Avoids a hardcoded lookback that's wrong at either extreme.
func redComparisonWindow(sinceNs, nowNs int64) (beforeFrom, beforeTo, afterFrom, afterTo time.Time) {
	since := time.Unix(0, sinceNs)
	now := time.Unix(0, nowNs)
	dur := now.Sub(since)
	if dur < 0 {
		dur = 0
	}
	return since.Add(-dur), since, since, now
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/... -run 'TestFilterOpenProblemsSince|TestRedComparisonWindow' -v`
Expected: PASS (7 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/api/deployment_report.go internal/api/deployment_report_test.go
git commit -m "$(cat <<'EOF'
feat(deployment-report): add inclusion-filter + RED-window pure helpers

Table-driven tests lock the two invariants the deployment analysis
report's inclusion gate depends on: a problem only qualifies if it
started at/after the deploy timestamp, and the before/after RED
comparison window is symmetric around that timestamp.

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Backend response types + handler

**Files:**
- Modify: `internal/api/deployment_report.go` (append to the file created in Task 1)
- Modify: `internal/api/api.go` (one route line, near the other `/api/problems*` routes at api.go:643)

**Interfaces:**
- Consumes: `filterOpenProblemsSince`, `redComparisonWindow` (Task 1); `s.store.OpenProblemsSnapshot(ctx) (map[string]*chstore.Problem, error)`; `s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{SinceNs, Limit}) ([]chstore.AnomalyEvent, error)`; `s.store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{State, Limit}) ([]chstore.ExceptionGroup, error)`; `s.store.GetServicesAggFilteredIn(ctx, from, to time.Time, nameMatch string, serviceIn []string, sort, dir string, limit, offset int) ([]chstore.ServiceSummary, error)`; `s.openProblemCountsCached(ctx) (map[string]chstore.OpenProblemCounts, error)`; `scoreHealth(*chstore.ServiceSummary, chstore.OpenProblemCounts) (string, string)`; `chstore.EnrichProblemsWithPriority([]chstore.Problem) []chstore.Problem`; `s.serveCached(w, r, key, ttl, fn)`; `parseTime(string) time.Time`.
- Produces: `GET /api/deployment-report?since=<unix_ns>` → JSON `DeploymentReport`. `REDStats`, `ServiceReportSection`, `DeploymentReport` Go types (exported, `json` tags camelCase) for the frontend task to mirror in `types.ts`.

- [ ] **Step 1: Append the response types and handler to `internal/api/deployment_report.go`**

```go
// (append after redComparisonWindow in internal/api/deployment_report.go)

import (
	"context"
	"net/http"
	"time"

	"coremetry/internal/chstore"
)

// REDStats is the error-rate/latency/throughput triple shown for a
// service's before-deploy and after-deploy windows.
type REDStats struct {
	ErrorRate  float64 `json:"errorRate"`
	P99Ms      float64 `json:"p99Ms"`
	Throughput float64 `json:"throughput"` // spans/sec over the window
}

// ServiceReportSection is one qualifying service's slice of the
// deployment report: it has at least one still-open Problem that
// started at/after the report's `since` timestamp (the inclusion gate —
// see the design doc). Anomalies/NewErrors are supporting evidence for
// the SAME service, also scoped to started-after-deploy AND still
// active/open — they never independently qualify a service.
type ServiceReportSection struct {
	Service   string                   `json:"service"`
	Health    string                   `json:"health"`
	Before    REDStats                 `json:"before"`
	After     REDStats                 `json:"after"`
	Problems  []chstore.Problem        `json:"problems"`
	Anomalies []chstore.AnomalyEvent   `json:"anomalies"`
	NewErrors []chstore.ExceptionGroup `json:"newErrors"`
}

// DeploymentReport is the full response for GET /api/deployment-report.
type DeploymentReport struct {
	Since       int64                  `json:"since"`
	GeneratedAt int64                  `json:"generatedAt"`
	Services    []ServiceReportSection `json:"services"`
}

// redStatsFor converts a ServiceSummary + window duration into the
// compact REDStats shape. windowSec == 0 (since == now, the "deploy just
// happened" edge case) yields Throughput 0 rather than dividing by zero.
func redStatsFor(sv chstore.ServiceSummary, windowSec float64) REDStats {
	throughput := 0.0
	if windowSec > 0 {
		throughput = float64(sv.SpanCount) / windowSec
	}
	return REDStats{ErrorRate: sv.ErrorRate, P99Ms: sv.P99Ms, Throughput: throughput}
}

// buildDeploymentReport assembles the full report for a given deploy
// timestamp. Pulled out of the HTTP handler so it's independently
// testable against a real store without an http.Request in play.
func (s *Server) buildDeploymentReport(ctx context.Context, sinceNs int64) (*DeploymentReport, error) {
	nowNs := time.Now().UnixNano()

	// 1. Inclusion gate: services with a still-open Problem that
	// started at/after the deploy. OpenProblemsSnapshot is a single
	// bounded FINAL scan over the (small, triage-sized) problems
	// table — see chstore.OpenProblemsSnapshot doc comment.
	snapshot, err := s.store.OpenProblemsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	qualifying := filterOpenProblemsSince(snapshot, sinceNs)
	qualifying = chstore.EnrichProblemsWithPriority(qualifying)

	bySvc := map[string][]chstore.Problem{}
	var svcOrder []string
	for _, p := range qualifying {
		if _, ok := bySvc[p.Service]; !ok {
			svcOrder = append(svcOrder, p.Service)
		}
		bySvc[p.Service] = append(bySvc[p.Service], p)
	}
	if len(svcOrder) == 0 {
		return &DeploymentReport{Since: sinceNs, GeneratedAt: nowNs, Services: []ServiceReportSection{}}, nil
	}

	// 2. Anomalies: still-active events for the qualifying services,
	// started at/after the deploy. ListAnomalyEvents bounds on
	// last_seen >= SinceNs (a superset of "started after deploy" —
	// an anomaly active now that started earlier still has a recent
	// last_seen), so we narrow further in Go by StartedAt + Service.
	allAnomalies, err := s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
		SinceNs: sinceNs, Limit: 2000,
	})
	if err != nil {
		return nil, err
	}
	anomaliesBySvc := map[string][]chstore.AnomalyEvent{}
	for _, a := range allAnomalies {
		if a.Status == "active" && a.StartedAt >= sinceNs && bySvc[a.Service] != nil {
			anomaliesBySvc[a.Service] = append(anomaliesBySvc[a.Service], a)
		}
	}

	// 3. New errors: exception groups not yet closed out (new /
	// acknowledged / regressed — the same "open" convenience bucket
	// the inbox uses), first seen at/after the deploy, on a
	// qualifying service. ExceptionGroupFilter has no FirstSeen
	// param, so we narrow in Go same as anomalies above.
	allErrors, err := s.store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{
		State: "open", Limit: 500,
	})
	if err != nil {
		return nil, err
	}
	errorsBySvc := map[string][]chstore.ExceptionGroup{}
	for _, e := range allErrors {
		if e.FirstSeen >= sinceNs && bySvc[e.Service] != nil {
			errorsBySvc[e.Service] = append(errorsBySvc[e.Service], e)
		}
	}

	// 4. RED before/after, scoped to just the qualifying services —
	// two calls total, not one per service.
	beforeFrom, beforeTo, afterFrom, afterTo := redComparisonWindow(sinceNs, nowNs)
	beforeRows, err := s.store.GetServicesAggFilteredIn(ctx, beforeFrom, beforeTo, "", svcOrder, "", "", 0, 0)
	if err != nil {
		return nil, err
	}
	afterRows, err := s.store.GetServicesAggFilteredIn(ctx, afterFrom, afterTo, "", svcOrder, "", "", 0, 0)
	if err != nil {
		return nil, err
	}
	beforeBySvc := map[string]chstore.ServiceSummary{}
	for _, sv := range beforeRows {
		beforeBySvc[sv.Name] = sv
	}
	afterBySvc := map[string]chstore.ServiceSummary{}
	for _, sv := range afterRows {
		afterBySvc[sv.Name] = sv
	}
	beforeWindowSec := beforeTo.Sub(beforeFrom).Seconds()
	afterWindowSec := afterTo.Sub(afterFrom).Seconds()

	// 5. Health badge — reuse the exact /api/services scoring so the
	// report and the Services page never disagree on red/yellow/green.
	openCounts, err := s.openProblemCountsCached(ctx)
	if err != nil {
		return nil, err
	}

	sections := make([]ServiceReportSection, 0, len(svcOrder))
	for _, svc := range svcOrder {
		afterSv := afterBySvc[svc] // zero value if no spans in-window — fine, all-zero RED
		health, _ := scoreHealth(&afterSv, openCounts[svc])
		sections = append(sections, ServiceReportSection{
			Service:   svc,
			Health:    health,
			Before:    redStatsFor(beforeBySvc[svc], beforeWindowSec),
			After:     redStatsFor(afterSv, afterWindowSec),
			Problems:  bySvc[svc],
			Anomalies: anomaliesBySvc[svc],
			NewErrors: errorsBySvc[svc],
		})
	}

	return &DeploymentReport{Since: sinceNs, GeneratedAt: nowNs, Services: sections}, nil
}

// getDeploymentReport handles GET /api/deployment-report?since=<unix_ns>.
// Read-only, no audit entry needed. `since` follows the codebase-wide
// absolute-timestamp convention (unix nanoseconds, same as `from`/`to`
// elsewhere — see parseTime) rather than milliseconds.
func (s *Server) getDeploymentReport(w http.ResponseWriter, r *http.Request) {
	sinceStr := r.URL.Query().Get("since")
	since := parseTime(sinceStr)
	if since.IsZero() {
		http.Error(w, "missing or invalid since query param (unix nanoseconds)", http.StatusBadRequest)
		return
	}
	sinceNs := since.UnixNano()
	if sinceNs > time.Now().UnixNano() {
		http.Error(w, "since must be in the past", http.StatusBadRequest)
		return
	}

	key := "deployment-report:since=" + sinceStr
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.buildDeploymentReport(ctx, sinceNs)
	})
}
```

- [ ] **Step 2: Register the route in `internal/api/api.go`**

Find the block around line 643 (`mux.HandleFunc("GET    /api/problems", s.listProblems)`) and add the new route immediately after the existing `/api/problems/buckets` line:

```go
	mux.HandleFunc("GET    /api/problems", s.listProblems)
	mux.HandleFunc("GET    /api/problems/count", s.countProblems)
	mux.HandleFunc("GET    /api/problems/buckets", s.listProblemBuckets)
	// Deployment analysis report (v0.8.X) — fleet-wide, read-only.
	// Services qualify via a still-open Problem that started at/after
	// ?since; anomalies/new-errors are supporting evidence for those
	// same services only. See buildDeploymentReport for the composition.
	mux.HandleFunc("GET    /api/deployment-report", s.getDeploymentReport)
	mux.HandleFunc("GET    /api/problems/{id}/rootcause", s.getProblemRootCause)
```

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: no errors. If `chstore.EnrichProblemsWithPriority` or any store method signature doesn't match exactly what was used above, the compiler will name the mismatch — fix the call site to match the real signature (do not change the store method).

- [ ] **Step 4: Run the full backend test suite**

Run: `go test ./internal/api/... ./internal/chstore/... -v 2>&1 | tail -60`
Expected: PASS, including the Task 1 tests. No pre-existing test should regress (this task only adds a new file + one route line, doesn't touch existing handler code).

- [ ] **Step 5: Commit**

```bash
git add internal/api/deployment_report.go internal/api/api.go
git commit -m "$(cat <<'EOF'
feat(deployment-report): add GET /api/deployment-report endpoint

Composes four existing read-only store primitives (open-problems
snapshot, anomaly events, exception groups, services-agg-in) into one
fleet-wide report: services qualify via a still-open Problem that
started after the given deploy timestamp; anomalies and new errors
are supporting evidence for those same services, gated the same way.
No new ClickHouse schema — pure composition + two new pure helpers.

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Frontend types + API client method

**Files:**
- Modify: `frontend/src/lib/types.ts` — add types after the `AnomalyEvent` interface (around line 2498+, wherever it now ends after Task 2's backend work — search for `export interface AnomalyEvent` and insert after its closing brace).
- Modify: `frontend/src/lib/api.ts` — add the client method near the existing `problems:`/`problemsCount:` entries (around line 1577-1584).

**Interfaces:**
- Consumes: nothing new (mirrors the Go `DeploymentReport`/`ServiceReportSection`/`REDStats` JSON shape from Task 2 exactly — camelCase field names already match via the `json` tags).
- Produces: `DeploymentReport`, `ServiceReportSection`, `REDStats` TypeScript interfaces; `api.getDeploymentReport(sinceNs: number): Promise<DeploymentReport>`.

- [ ] **Step 1: Add the TypeScript types**

Insert into `frontend/src/lib/types.ts` immediately after the `AnomalyEvent` interface's closing brace (the `}` following the `rootCause?: RootCauseSummary;` field seen at line ~2532):

```typescript
// ── Deployment analysis report ──────────────────────────────────────────────
// Fleet-wide, read-only, generated on demand from an operator-supplied
// deploy timestamp — see GET /api/deployment-report. A service appears
// here only if it has a still-open Problem that started at/after the
// deploy; anomalies/newErrors are supporting evidence for that SAME
// service, also gated to "started after deploy AND still active/open".

export interface REDStats {
  errorRate: number;
  p99Ms: number;
  throughput: number; // spans/sec over the comparison window
}

export interface ServiceReportSection {
  service: string;
  health: 'red' | 'yellow' | 'green' | '';
  before: REDStats;
  after: REDStats;
  problems: Problem[];
  anomalies: AnomalyEvent[];
  newErrors: ExceptionGroup[];
}

export interface DeploymentReport {
  since: number;       // unix ns — the deploy timestamp the report was run against
  generatedAt: number; // unix ns — when this report was computed
  services: ServiceReportSection[];
}
```

- [ ] **Step 2: Add the API client method**

Insert into `frontend/src/lib/api.ts` immediately after the `problemsCount:` entry (line ~1584):

```typescript
  // Deployment analysis report — fleet-wide, generated on demand.
  // `sinceNs` is unix nanoseconds (same convention as from/to
  // elsewhere in this client), not milliseconds.
  deploymentReport: (sinceNs: number) =>
    get<DeploymentReport>(`/api/deployment-report?since=${sinceNs}`),
```

Confirm `DeploymentReport` is imported at the top of `api.ts` — check the existing type-import block (it already imports `Problem`, `ExceptionGroup`, `AnomalyEvent` etc. from `./types`; add `DeploymentReport` to that same import list).

- [ ] **Step 3: Typecheck**

Run: `cd frontend && npx tsc --noEmit`
Expected: no errors related to the new types/method. (Other pre-existing unrelated errors, if any, are out of scope — but there should be none introduced by this step.)

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/lib/api.ts
git commit -m "$(cat <<'EOF'
feat(deployment-report): add frontend types + API client method

Mirrors the backend DeploymentReport/ServiceReportSection/REDStats
JSON shape and adds api.deploymentReport(sinceNs) for the upcoming
report page.

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Frontend DeploymentReport page

**Files:**
- Create: `frontend/src/pages/DeploymentReport.tsx`

**Interfaces:**
- Consumes: `api.deploymentReport(sinceNs)` (Task 3); `DeploymentReport`/`ServiceReportSection`/`REDStats`/`Problem`/`AnomalyEvent`/`ExceptionGroup` types (Task 3 + existing); `useDataTable`, `DataTableHead`, `DataTableColgroup` from `@/components/DataTable`; `DataTableColumn` from `@/lib/dataTable`; `Topbar` from `@/components/Topbar`; `Spinner`, `Empty` from `@/components/Spinner`; `TableSkeleton` from `@/components/Skeleton`; `Button`, `Field` from `@/components/ui`; `CopilotExplain` from `@/components/CopilotExplain`; `useSearchParams` from `react-router-dom`.
- Produces: default-exported `DeploymentReportPage` component for `App.tsx` to lazy-import.

- [ ] **Step 1: Write the page**

```tsx
// frontend/src/pages/DeploymentReport.tsx
import { useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Button, Field } from '@/components/ui';
import { CopilotExplain } from '@/components/CopilotExplain';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { api } from '@/lib/api';
import type { DataTableColumn } from '@/lib/dataTable';
import type {
  DeploymentReport, ServiceReportSection, Problem, AnomalyEvent, ExceptionGroup,
} from '@/lib/types';

// /deployment-report — fleet-wide, on-demand. Given a deploy timestamp,
// shows every service that still has an open Problem which started
// after that timestamp, plus that service's still-active anomalies,
// still-open new errors, and a before/after RED comparison. No
// per-service selection (holistic by design) and no polling — this is
// a point-in-time computed view, fetched only when the operator asks
// for it via "Generate Report".
export default function DeploymentReportPage() {
  const [params, setParams] = useSearchParams();
  const sinceParam = params.get('since'); // unix ns, string

  // datetime-local has no native ns precision — the input works in local
  // ms, converted to ns (matching the codebase-wide absolute-timestamp
  // convention) only when the operator submits.
  const [pendingLocal, setPendingLocal] = useState(() => {
    if (!sinceParam) return '';
    const ms = Number(sinceParam) / 1_000_000;
    const d = new Date(ms - new Date().getTimezoneOffset() * 60000);
    return d.toISOString().slice(0, 16);
  });

  const generate = () => {
    if (!pendingLocal) return;
    const ms = new Date(pendingLocal).getTime();
    const ns = Math.round(ms * 1_000_000);
    setParams(prev => {
      const next = new URLSearchParams(prev);
      next.set('since', String(ns));
      return next;
    }, { replace: true });
  };

  const sinceNs = sinceParam ? Number(sinceParam) : null;

  const [report, setReport] = useState<DeploymentReport | null | undefined>(undefined);
  useMemo(() => {
    if (sinceNs === null) { setReport(undefined); return; }
    setReport(undefined);
    api.deploymentReport(sinceNs)
      .then(r => setReport(r))
      .catch(() => setReport(null));
  }, [sinceNs]);

  return (
    <>
      <Topbar title="Deployment analysis report" />
      <div id="content">
        <div style={{ color: 'var(--text2)', fontSize: 12, marginBottom: 12 }}>
          Pick the timestamp your deployment succeeded. The report shows every
          service that still has an open problem which started after that
          moment — plus its still-active anomalies, still-open new errors, and
          a before/after health comparison. Fleet-wide, no service picker.
        </div>

        <div className="controls" style={{ marginBottom: 16, alignItems: 'flex-end', gap: 12 }}>
          <Field
            label="Deployment succeeded at"
            type="datetime-local"
            value={pendingLocal}
            onChange={e => setPendingLocal(e.target.value)}
          />
          <Button variant="primary" size="sm" onClick={generate} disabled={!pendingLocal}>
            Generate report
          </Button>
        </div>

        {sinceNs === null && (
          <Empty icon="◷" title="Pick a deployment timestamp to generate the report" />
        )}
        {sinceNs !== null && report === undefined && <TableSkeleton cols={6} wideFirst />}
        {sinceNs !== null && report === null && (
          <Empty icon="✗" title="Failed to load the deployment report" />
        )}
        {sinceNs !== null && report && report.services.length === 0 && (
          <Empty icon="✓" title="No open problems since this deployment">
            Every service is clean relative to the timestamp you picked.
          </Empty>
        )}
        {sinceNs !== null && report && report.services.length > 0 && (
          <ReportBody report={report} />
        )}
      </div>
    </>
  );
}

function fmtPct(n: number) { return `${n.toFixed(1)}%`; }
function fmtMs(n: number) { return `${n.toFixed(0)}ms`; }
function fmtRps(n: number) { return `${n.toFixed(2)}/s`; }
function healthBadgeClass(h: string) {
  return h === 'red' ? 'b-err' : h === 'yellow' ? 'b-warn' : h === 'green' ? 'b-ok' : 'b-gray';
}

// Flattened row shapes — one row per (service, item) pair — so each of
// the three signal tables is a single fixed useDataTable instance
// regardless of how many services qualify (React hooks can't be called
// a variable number of times per render).
type ProblemRow = Problem & { __service: string };
type AnomalyRow = AnomalyEvent & { __service: string };
type ErrorRow = ExceptionGroup & { __service: string };

const SERVICE_COLS: DataTableColumn<ServiceReportSection>[] = [
  { id: 'service', label: 'Service', sortValue: r => r.service, naturalDir: 'asc', width: 200 },
  { id: 'health', label: 'Health', sortValue: r => r.health, naturalDir: 'asc', width: 90 },
  { id: 'errBefore', label: 'Err% before', sortValue: r => r.before.errorRate, numeric: true, width: 100 },
  { id: 'errAfter', label: 'Err% after', sortValue: r => r.after.errorRate, numeric: true, width: 100 },
  { id: 'p99Before', label: 'P99 before', sortValue: r => r.before.p99Ms, numeric: true, width: 100 },
  { id: 'p99After', label: 'P99 after', sortValue: r => r.after.p99Ms, numeric: true, width: 100 },
  { id: 'thBefore', label: 'Throughput before', sortValue: r => r.before.throughput, numeric: true, width: 130 },
  { id: 'thAfter', label: 'Throughput after', sortValue: r => r.after.throughput, numeric: true, width: 130 },
];

const PROBLEM_COLS: DataTableColumn<ProblemRow>[] = [
  { id: 'service', label: 'Service', sortValue: r => r.__service, naturalDir: 'asc', width: 160 },
  { id: 'severity', label: 'Severity', sortValue: r => r.severity, naturalDir: 'asc', width: 90 },
  { id: 'priority', label: 'Priority', sortValue: r => r.priority ?? 'P3', naturalDir: 'asc', width: 80 },
  { id: 'ruleName', label: 'Rule', sortValue: r => r.ruleName, naturalDir: 'asc', width: 220 },
  { id: 'startedAt', label: 'Started', sortValue: r => r.startedAt, width: 160 },
];

const ANOMALY_COLS: DataTableColumn<AnomalyRow>[] = [
  { id: 'service', label: 'Service', sortValue: r => r.__service, naturalDir: 'asc', width: 160 },
  { id: 'kind', label: 'Kind', sortValue: r => r.kind, naturalDir: 'asc', width: 120 },
  { id: 'pattern', label: 'Pattern', sortValue: r => r.pattern, naturalDir: 'asc', width: 280 },
  { id: 'startedAt', label: 'Started', sortValue: r => r.startedAt, width: 160 },
  { id: 'currentRatio', label: 'Current ratio', sortValue: r => r.currentRatio, numeric: true, width: 110 },
];

const ERROR_COLS: DataTableColumn<ErrorRow>[] = [
  { id: 'service', label: 'Service', sortValue: r => r.__service, naturalDir: 'asc', width: 160 },
  { id: 'type', label: 'Type', sortValue: r => r.type, naturalDir: 'asc', width: 200 },
  { id: 'message', label: 'Message', sortValue: r => r.message, naturalDir: 'asc', width: 320 },
  { id: 'firstSeen', label: 'First seen', sortValue: r => r.firstSeen, width: 160 },
  { id: 'occurrences', label: 'Occurrences', sortValue: r => r.occurrences, numeric: true, width: 100 },
];

function ReportBody({ report }: { report: DeploymentReport }) {
  const problemRows: ProblemRow[] = report.services.flatMap(
    s => s.problems.map(p => ({ ...p, __service: s.service })));
  const anomalyRows: AnomalyRow[] = report.services.flatMap(
    s => s.anomalies.map(a => ({ ...a, __service: s.service })));
  const errorRows: ErrorRow[] = report.services.flatMap(
    s => s.newErrors.map(e => ({ ...e, __service: s.service })));

  const svcDt = useDataTable<ServiceReportSection>({
    storageKey: 'deployment-report-services', columns: SERVICE_COLS,
    rows: report.services, initialSort: { id: 'health', dir: 'desc' },
  });
  const problemDt = useDataTable<ProblemRow>({
    storageKey: 'deployment-report-problems', columns: PROBLEM_COLS,
    rows: problemRows, initialSort: { id: 'startedAt', dir: 'desc' },
  });
  const anomalyDt = useDataTable<AnomalyRow>({
    storageKey: 'deployment-report-anomalies', columns: ANOMALY_COLS,
    rows: anomalyRows, initialSort: { id: 'startedAt', dir: 'desc' },
  });
  const errorDt = useDataTable<ErrorRow>({
    storageKey: 'deployment-report-errors', columns: ERROR_COLS,
    rows: errorRows, initialSort: { id: 'firstSeen', dir: 'desc' },
  });

  return (
    <>
      <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
        Affected services ({report.services.length})
      </h3>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={svcDt} />
          <DataTableHead dt={svcDt} />
          <tbody>
            {svcDt.sortedRows.map(s => (
              <tr key={s.service}>
                <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{s.service}</td>
                <td><span className={`badge ${healthBadgeClass(s.health)}`}>{s.health || 'n/a'}</span></td>
                <td className="num mono">{fmtPct(s.before.errorRate)}</td>
                <td className="num mono" style={{ color: s.after.errorRate > s.before.errorRate ? 'var(--err)' : undefined }}>
                  {fmtPct(s.after.errorRate)}
                </td>
                <td className="num mono">{fmtMs(s.before.p99Ms)}</td>
                <td className="num mono" style={{ color: s.after.p99Ms > s.before.p99Ms ? 'var(--err)' : undefined }}>
                  {fmtMs(s.after.p99Ms)}
                </td>
                <td className="num mono">{fmtRps(s.before.throughput)}</td>
                <td className="num mono">{fmtRps(s.after.throughput)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
        Problems since deploy ({problemRows.length})
      </h3>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={problemDt} trailing={[160]} />
          <DataTableHead dt={problemDt} trailing={<th>AI review</th>} />
          <tbody>
            {problemDt.sortedRows.map(p => (
              <tr key={p.id}>
                <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{p.__service}</td>
                <td><span className="badge b-gray">{p.severity}</span></td>
                <td>{p.priority ?? 'P3'}</td>
                <td>{p.ruleName}</td>
                <td>{new Date(p.startedAt / 1_000_000).toLocaleString()}</td>
                <td><CopilotExplain kind="problem" id={p.id} label="AI review" /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
        Anomalies since deploy ({anomalyRows.length})
      </h3>
      {anomalyRows.length === 0 ? (
        <Empty icon="◇" title="No active anomalies since this deploy on the affected services" />
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={anomalyDt} />
            <DataTableHead dt={anomalyDt} />
            <tbody>
              {anomalyDt.sortedRows.map(a => (
                <tr key={a.id}>
                  <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{a.__service}</td>
                  <td><span className="badge b-gray">{a.kind}</span></td>
                  <td>{a.pattern}</td>
                  <td>{new Date(a.startedAt / 1_000_000).toLocaleString()}</td>
                  <td className="num mono">{a.currentRatio.toFixed(2)}x</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
        New errors since deploy ({errorRows.length})
      </h3>
      {errorRows.length === 0 ? (
        <Empty icon="◇" title="No new errors since this deploy on the affected services" />
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={errorDt} />
            <DataTableHead dt={errorDt} />
            <tbody>
              {errorDt.sortedRows.map(e => (
                <tr key={e.fingerprint}>
                  <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{e.__service}</td>
                  <td>{e.type}</td>
                  <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={e.message}>
                    {e.message}
                  </td>
                  <td>{new Date(e.firstSeen / 1_000_000).toLocaleString()}</td>
                  <td className="num mono">{e.occurrences}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
```

Confirmed: `DataTableColgroup`/`DataTableHead` both accept a `trailing` prop (`trailing?: number[]` and `trailing?: ReactNode` respectively, `frontend/src/components/DataTable.tsx:210,232`) with exactly the shape used above for the Problems table's AI-review column — no adjustment needed.

- [ ] **Step 2: Verify `Button`, `Field` are exported from `@/components/ui`**

Run: `grep -n "export" frontend/src/components/ui/index.ts | grep -E "Button|Field"`
Expected: both are re-exported. If `Field` isn't re-exported from the `ui` barrel, change the import in `DeploymentReport.tsx` to `import { Field } from '@/components/ui/Field';` instead.

- [ ] **Step 3: Typecheck**

Run: `cd frontend && npx tsc --noEmit`
Expected: no errors. Fix any prop-name mismatches surfaced (especially the `DataTableColgroup`/`DataTableHead` trailing-column question from Step 1) before proceeding.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/pages/DeploymentReport.tsx
git commit -m "$(cat <<'EOF'
feat(deployment-report): add DeploymentReport page

Datetime picker writes ?since= (URL is source of truth), fetches on
Generate (no polling — point-in-time computed view). Four fixed
useDataTable instances (services/problems/anomalies/errors) so the
table count never varies with how many services qualify. Per-problem
AI review reuses the existing CopilotExplain component as-is.

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Routing, sidebar, command palette, i18n registration

**Files:**
- Modify: `frontend/src/App.tsx`
- Modify: `frontend/src/components/Sidebar.tsx`
- Modify: `frontend/src/components/CommandPalette.tsx`
- Modify: `frontend/src/lib/i18n.ts`

**Interfaces:**
- Consumes: `DeploymentReportPage` default export (Task 4).
- Produces: `/deployment-report` route reachable via sidebar, command palette, and direct URL.

- [ ] **Step 1: Lazy import + route in `App.tsx`**

Add near the other triage-adjacent lazy imports (after line 47, `const Anomalies = lazy(...)`):

```tsx
const DeploymentReport  = lazy(() => import('./pages/DeploymentReport'));
```

Add the route after the `/anomalies` route (around line 133):

```tsx
            <Route path="/anomalies"      element={<Anomalies />} />
            <Route path="/deployment-report" element={<DeploymentReport />} />
```

- [ ] **Step 2: Sidebar entry**

In `frontend/src/components/Sidebar.tsx`, add to the `navGroup.triage` items array (after the `/anomalies` entry seen at line ~61):

```tsx
      { href: '/deployment-report', label: 'nav.deploymentReport', icon: Rocket },
```

Confirm `Rocket` (or another appropriate icon already imported from the icon library this file uses — check the top-of-file import block for the existing icon import source, e.g. `lucide-react`) is imported; if not already imported in this file, add it to the existing icon import statement.

- [ ] **Step 3: Command palette entry**

In `frontend/src/components/CommandPalette.tsx`, add to the `PAGES` array in the Triage section (after the `Anomalies` entry at line ~52):

```tsx
  { kind: 'page', label: 'Deployment Report', hint: 'What broke since a deploy', to: '/deployment-report' },
```

- [ ] **Step 4: i18n keys**

In `frontend/src/lib/i18n.ts`, add to the English block (after `'nav.anomalies': 'Anomalies',` at line 34):

```ts
  'nav.deploymentReport': 'Deployment Report',
```

Add to the Turkish block (after `'nav.anomalies':   'Anomaliler',` at line 136):

```ts
  'nav.deploymentReport': 'Dağıtım Raporu',
```

- [ ] **Step 5: Typecheck + smoke-check the route registration**

Run: `cd frontend && npx tsc --noEmit`
Expected: no errors.

Run: `cd frontend && npm run dev` (background) then confirm `http://localhost:5173/deployment-report` (or whatever port the dev server reports) renders the page shell (datetime field + Generate button) without a console error. Stop the dev server after confirming.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/App.tsx frontend/src/components/Sidebar.tsx frontend/src/components/CommandPalette.tsx frontend/src/lib/i18n.ts
git commit -m "$(cat <<'EOF'
feat(deployment-report): register route, sidebar, palette, i18n

Standard four-touchpoint registration so /deployment-report is
reachable from the Triage nav group, the command palette, and direct
URL — English + Turkish nav labels.

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Full verification gate + manual testing doc

**Files:**
- Create: `docs/DEPLOYMENT-REPORT-TESTING.md`

**Interfaces:**
- Consumes: the running local stack (Coremetry binary + ClickHouse + Redis), the demo generators under `cmd/demo/`.
- Produces: a manual testing walkthrough document; final green build/test/typecheck gate.

- [ ] **Step 1: Run the full backend gate**

Run: `go build ./... && go test ./...`
Expected: both succeed with no failures.

- [ ] **Step 2: Run the full frontend gate**

Run: `cd frontend && npx tsc --noEmit`
Expected: no errors.

- [ ] **Step 3: Write the manual testing doc**

```markdown
<!-- docs/DEPLOYMENT-REPORT-TESTING.md -->
# Testing the Deployment Analysis Report locally

How to exercise `/deployment-report` end-to-end on your machine —
seed a fake "deploy", make something break after it, confirm the
report picks it up, confirm it drops off once resolved, and confirm
the AI review button works.

## 1. Start the local stack

```bash
docker compose up -d clickhouse redis   # or your usual local CH+Redis setup
go run . 2>&1 | tee /tmp/coremetry.log  # COREMETRY_MODE=all is the default
```

Confirm the UI loads at `http://localhost:8080` (or your configured port)
and `/services` shows at least one service — if it's empty, run the demo
generator first:

```bash
go run ./cmd/demo   # populates synthetic services/traces/metrics
```

## 2. Note "now" as your fake deploy timestamp

Open the browser console on any Coremetry page and run:

```js
Date.now()  // e.g. 1752741600000 — copy this down, call it T0
```

This stands in for "the moment the deploy succeeded." Everything from
here on needs to fire strictly *after* T0 to be picked up by the report.

## 3. Trigger a problem that starts after T0

Easiest path: use an existing alert rule with a low threshold, or
temporarily lower one via Settings → Alert rules (e.g. set the
`error_rate` rule's threshold to something your demo traffic will
cross within a minute or two). Wait for `/problems` to show a new
**open** problem for some service — confirm its `startedAt` is after
T0 (the Problems table shows a relative "X ago" — anything newer than
your T0 note qualifies).

If you don't want to touch alert rules: the demo generator's incident
mode (`go run ./cmd/demo -incident` if available — check `cmd/demo
--help` for the exact flag name) synthesizes a burst of errors/latency
on one service, which the evaluator should pick up as a fresh Problem
within its normal poll interval.

## 4. Generate the report

Navigate to `/deployment-report` (via the sidebar's Triage group, the
command palette — ⌘K → "Deployment Report" — or directly). In the
"Deployment succeeded at" field, pick the date/time corresponding to
T0 (your browser's local time — the picker converts to the timestamp
the backend expects). Click **Generate report**.

**Expected:**
- The service with the fresh problem appears in "Affected services"
  with a red or yellow health badge.
- The same problem appears in "Problems since deploy" with the
  correct severity/rule/started time.
- Clicking **AI review** on that problem row calls the existing
  Copilot explain endpoint and renders a response inline (skip this
  check if Copilot isn't configured on your local install — the
  button self-hides when disabled).
- "Anomalies since deploy" / "New errors since deploy" show
  `Empty` states unless you also triggered one of those (optional,
  see step 5).

## 5. (Optional) Trigger an anomaly and a new error too

- **Anomaly**: cause a latency or error-rate spike on an operation via
  demo traffic; `/anomalies` should show it as `active` within its
  detector's poll window.
- **New error**: emit an exception with a stack trace/type never seen
  before on that service (the demo generator's error injection, or a
  one-off curl to a demo endpoint that throws). `/problems` (Exception
  inbox section) should show it with state `new`.

Re-generate the report (same T0) — both should now appear under the
qualifying service's sections.

## 6. Confirm the inclusion gate (negative case)

Pick a **future** `since` (a date/time after your test problem's
`startedAt`, i.e. after the problem started but you're now asking
"what happened after THIS later moment") — the report should show
**no** services (or fewer), since the problem started before your
new `since`. This confirms the `StartedAt >= since` filter is doing
its job, not just returning everything unconditionally.

## 7. Confirm the "still exists" gate (negative case)

Resolve the test problem (let the alert condition clear, or manually
resolve it if your install has a resolve action). Re-run the report
with the original T0. The service should **drop out of the report
entirely** — confirming resolved problems don't keep a service
qualifying even though they started after the deploy.

## 8. Confirm no polling / no auto-refresh

Leave the report open on a service with a still-open problem. Confirm
the network tab shows **no repeated requests** to
`/api/deployment-report` — it should fetch once on Generate and then
sit idle (unlike the live dashboards, this is a point-in-time
snapshot, not a poller).
```

- [ ] **Step 4: Commit**

```bash
git add docs/DEPLOYMENT-REPORT-TESTING.md
git commit -m "$(cat <<'EOF'
docs(deployment-report): add manual local testing walkthrough

Covers seeding a fake deploy timestamp, triggering a problem/anomaly/
new-error after it, confirming the inclusion gate (started-after +
still-open) in both the positive and negative direction, and
confirming the report doesn't poll.

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Notes (for the implementer, not a task)

- **Spec coverage:** inclusion gate (Task 2), RED delta (Tasks 1+2), AI review (Task 4, zero new backend code), holistic/no-service-picker (confirmed — the endpoint takes no `service` param), live-view/URL-state (Task 4's `useSearchParams` + no persistence), manual test doc (Task 6). All design-doc sections have a task.
- **Type consistency:** `DeploymentReport` / `ServiceReportSection` / `REDStats` field names are identical across Task 2 (Go, `json:"..."` tags) and Task 3 (TypeScript) — verified by hand, camelCase both sides. `DataTableColgroup`/`DataTableHead` `trailing` prop usage in Task 4 verified directly against `DataTable.tsx:210,232` — real props, correct shapes.
