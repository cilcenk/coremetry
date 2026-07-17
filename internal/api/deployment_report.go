package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
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

// intersectServices keeps only the services in svcOrder that also appear
// in teamSvcs, preserving svcOrder's ordering. nil teamSvcs (the
// servicesForTeam "no team constraint" sentinel) is passed through
// unfiltered; a non-nil-but-empty teamSvcs (a team with zero member
// services) correctly collapses the result to empty rather than
// leaving it unfiltered — that distinction is load-bearing, see
// servicesForTeam's doc comment.
func intersectServices(svcOrder, teamSvcs []string) []string {
	if teamSvcs == nil {
		return svcOrder
	}
	want := make(map[string]bool, len(teamSvcs))
	for _, s := range teamSvcs {
		want[s] = true
	}
	out := make([]string, 0, len(svcOrder))
	for _, svc := range svcOrder {
		if want[svc] {
			out = append(out, svc)
		}
	}
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

// nonNilSlice ensures a possibly-nil slice serializes as `[]`, not
// `null`, in the JSON response. A service with no anomalies/new-errors
// left its map lookup (anomaliesBySvc[svc] / errorsBySvc[svc]) as a nil
// slice; encoding/json renders nil slices as `null`, and the frontend's
// `s.anomalies.map(...)` threw "Cannot read properties of null (reading
// 'map')" on exactly that shape (operator-reported).
func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
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
// timestamp, optionally narrowed to services owned by ownerTeam and/or
// on-call'd by sreTeam (empty = no narrowing on that axis — same
// semantics as the Problems/Services team filter). Pulled out of the
// HTTP handler so it's independently testable against a real store
// without an http.Request in play.
func (s *Server) buildDeploymentReport(ctx context.Context, sinceNs int64, ownerTeam, sreTeam string) (*DeploymentReport, error) {
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

	// 1b. Owner/SRE team narrowing (mirrors the Problems inbox's
	// ?owner=/?sre= — v0.8.310 servicesForTeam/matchesTeamFilter).
	// Resolved from the operator-curated catalog, AND'd on top of the
	// inclusion gate: "qualifies via an open post-deploy problem AND
	// belongs to this team". A team with no matching qualifying
	// service returns an empty report, never an unfiltered one.
	if ownerTeam != "" || sreTeam != "" {
		mds, err := s.store.ListServiceMetadata(ctx)
		if err != nil {
			return nil, err
		}
		svcOrder = intersectServices(svcOrder, servicesForTeam(mds, ownerTeam, sreTeam))
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
			Problems:  nonNilSlice(bySvc[svc]),
			Anomalies: nonNilSlice(anomaliesBySvc[svc]),
			NewErrors: nonNilSlice(errorsBySvc[svc]),
		})
	}

	return &DeploymentReport{Since: sinceNs, GeneratedAt: nowNs, Services: sections}, nil
}

// getDeploymentReport handles GET /api/deployment-report?since=<unix_ns>
// [&ownerTeam=…][&sreTeam=…]. Read-only, no audit entry needed. `since`
// follows the codebase-wide absolute-timestamp convention (unix
// nanoseconds, same as `from`/`to` elsewhere — see parseTime) rather
// than milliseconds. ownerTeam/sreTeam mirror the Problems inbox's
// team filter (v0.8.310) — empty means no narrowing on that axis.
func (s *Server) getDeploymentReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sinceStr := q.Get("since")
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
	ownerTeam := strings.TrimSpace(q.Get("ownerTeam"))
	sreTeam := strings.TrimSpace(q.Get("sreTeam"))

	key := "deployment-report:since=" + sinceStr + ":owner=" + ownerTeam + ":sre=" + sreTeam
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.buildDeploymentReport(ctx, sinceNs, ownerTeam, sreTeam)
	})
}
