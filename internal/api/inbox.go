package api

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// InboxItem is the unified shape every triage-worthy thing
// (Problem / Exception group / Anomaly event) collapses into.
// Kind discriminates which source it came from; the kind-
// specific blob carries the bits needed to drill-down.
//
// Designed so a single table on /inbox can show "everything
// needing a human" without operators tab-hopping between
// Problems / Exceptions / Anomalies pages — same priority
// blend, same age column, same assignee column. The per-source
// pages still exist as drill-down targets.
type InboxItem struct {
	ID             string   `json:"id"`              // composite: "<kind>:<nativeId>"
	Kind           string   `json:"kind"`            // problem | exception | anomaly
	Source         string   `json:"source"`          // human label: "Alert rule" / "Exception" / "Anomaly"
	Priority       string   `json:"priority"`        // P1 | P2 | P3
	PriorityReason string   `json:"priorityReason"`
	Severity       string   `json:"severity"`        // critical | warning | info
	Service        string   `json:"service"`
	Title          string   `json:"title"`           // rule name / exception type / pattern
	Description    string   `json:"description"`
	StartedAt      int64    `json:"startedAt"`       // unix ns
	LastSeen       int64    `json:"lastSeen"`        // unix ns; for problems == StartedAt
	Assignee       string   `json:"assignee,omitempty"`
	// OwnerTeam + SRETeam attached server-side from
	// service_metadata so the inbox can render team chips
	// without each row firing a per-service lookup. Empty when
	// no catalog row exists for the service. OwnerTeam mirrors
	// what's auto-set on Problem.Assignee at open time;
	// surfacing it on every row (even exceptions / anomalies)
	// keeps the column meaningful across kinds.
	OwnerTeam      string   `json:"ownerTeam,omitempty"`
	SRETeam        string   `json:"sreTeam,omitempty"`
	Status         string   `json:"status"`          // open | acknowledged | resolved (problems);
	                                                  // open | regressed (exceptions); active | cleared (anomalies)
	Clusters       []string `json:"clusters,omitempty"`
	// Kind-specific drill-down hints. Only one is populated per
	// row. Keeps the JSON shape skinny — frontend reads exactly
	// the one matching `kind`.
	Problem   *InboxProblemRef   `json:"problem,omitempty"`
	Exception *InboxExceptionRef `json:"exception,omitempty"`
	Anomaly   *InboxAnomalyRef   `json:"anomaly,omitempty"`
}

type InboxProblemRef struct {
	ID        string  `json:"id"`
	RuleID    string  `json:"ruleId"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

type InboxExceptionRef struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Message     string `json:"message"`
	Occurrences uint64 `json:"occurrences"`
}

type InboxAnomalyRef struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`         // "log_pattern" | "trace_op"
	Pattern      string  `json:"pattern"`
	PeakRatio    float64 `json:"peakRatio"`
	CurrentRatio float64 `json:"currentRatio"`
}

// inbox unifies the three triage sources into one ranked list.
// Kept aggressively bounded — at 1000s of services, an inbox
// that returns 5k items isn't actionable. Default cap 200,
// max 500; the operator filters by priority/service/kind to
// shrink further. Cached 10s (matches Problems list cadence).
func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	ownerTeam := strings.TrimSpace(q.Get("ownerTeam"))
	sreTeam := strings.TrimSpace(q.Get("sreTeam"))
	statusFilter := strings.TrimSpace(q.Get("status")) // open (default) | all
	if statusFilter == "" {
		statusFilter = "open"
	}
	limit := parseInt(q.Get("limit"), 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	cacheKey := fmt.Sprintf("inbox:status=%s:svc=%s:owner=%s:sre=%s:limit=%d",
		statusFilter, service, ownerTeam, sreTeam, limit)
	s.serveCached(w, r, cacheKey, 10*time.Second, func() (any, error) {
		ctx := r.Context()
		items := make([]InboxItem, 0, 256)

		// v0.5.245 — service filter is now case-insensitive
		// substring across all three sources. Per-source SQL
		// filters dropped (each capped at 200 rows so a wider
		// fan-out is cheap); the substring narrow happens once
		// over the merged item list below. Operator typing
		// "java" now matches "java-demo", "java-frontend",
		// etc. without remembering the exact service name.
		// ── Problems ─────────────────────────────────────────────
		probs, err := s.store.ListProblems(ctx, chstore.ProblemFilter{
			Status: pickStatus(statusFilter), Limit: 200,
		})
		if err != nil {
			return nil, err
		}
		// Same enrichment chain Problems UI runs through, so the
		// derived priority lines up exactly.
		probs = s.store.EnrichProblemsWithRunbooks(ctx, probs)
		probs = s.store.EnrichProblemsWithClusters(ctx, probs, time.Hour)
		probs = s.store.EnrichProblemsWithDeploys(ctx, probs, 30*time.Minute)
		probs = chstore.EnrichProblemsWithPriority(probs)
		for _, p := range probs {
			// v0.8.287 — drop resolved Problems from the open inbox. pickStatus
			// fetched every status (see its comment), so the narrow happens here.
			if !inboxKeepsProblem(p.Status, statusFilter) {
				continue
			}
			items = append(items, problemToInbox(p))
		}

		// ── Exception groups ─────────────────────────────────────
		exFilter := chstore.ExceptionGroupFilter{
			State: pickExceptionState(statusFilter), Limit: 200,
		}
		exGroups, err := s.store.ListExceptionGroups(ctx, exFilter)
		if err != nil {
			return nil, err
		}
		for _, g := range exGroups {
			items = append(items, exceptionToInbox(g))
		}

		// ── Anomaly events ───────────────────────────────────────
		// 24h window matches the Anomalies page default. ListAnomaly
		// EventsByService isn't a thing — filter client-side.
		evs, err := s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{Limit: 200})
		if err != nil {
			return nil, err
		}
		for _, e := range evs {
			if statusFilter == "open" && e.Status != "active" {
				continue
			}
			items = append(items, anomalyToInbox(e))
		}

		// v0.5.245 — case-insensitive substring service filter
		// applied across the merged item set. Operator types
		// "java" → matches "java-demo", "java-frontend", etc.
		// Empty value (no filter) leaves items untouched.
		if service != "" {
			needle := strings.ToLower(service)
			filtered := items[:0]
			for _, it := range items {
				if strings.Contains(strings.ToLower(it.Service), needle) {
					filtered = append(filtered, it)
				}
			}
			items = filtered
		}

		// Team enrichment — one batch lookup over the service
		// catalog covers every row. Cheap (catalog is small,
		// cached upstream), and means we don't fire a per-row
		// GetServiceMetadata call. Empty values leave the chip
		// off in the UI.
		mdMap, _ := s.store.ListServiceMetadata(ctx)
		if len(mdMap) > 0 {
			for i := range items {
				if items[i].Service == "" {
					continue
				}
				md, ok := mdMap[items[i].Service]
				if !ok {
					continue
				}
				items[i].OwnerTeam = md.OwnerTeam
				items[i].SRETeam = md.SRETeam
			}
		}

		// Team filter — applies AFTER enrichment so the operator
		// can narrow to "the rows whose service is owned by team
		// X". Matches an empty value with empty (so the chip "—"
		// for unattributed rows works), and ignores case so URL
		// pastes between dashboards / chat don't surface a
		// false-negative on a mismatched capitalisation.
		if ownerTeam != "" || sreTeam != "" {
			filtered := items[:0]
			for _, it := range items {
				if ownerTeam != "" && !strings.EqualFold(it.OwnerTeam, ownerTeam) {
					continue
				}
				if sreTeam != "" && !strings.EqualFold(it.SRETeam, sreTeam) {
					continue
				}
				filtered = append(filtered, it)
			}
			items = filtered
		}

		// Stable rank: priority desc, then most-recent-activity.
		sort.SliceStable(items, func(i, j int) bool {
			ri := priorityRank(items[i].Priority)
			rj := priorityRank(items[j].Priority)
			if ri != rj {
				return ri > rj
			}
			return items[i].LastSeen > items[j].LastSeen
		})
		if len(items) > limit {
			items = items[:limit]
		}
		return items, nil
	})
}

// pickStatus translates the inbox filter into a Problem status.
// "open" inbox shows open + acknowledged (still in-flight);
// "all" passes through to the store's no-filter.
// inboxKeepsProblem decides whether a Problem row belongs in the inbox at the
// given inbox status pivot. Fixes the v0.8.287 leak: pickStatus fetches every
// status (a single-value CH filter can't express open+acknowledged), so the
// "open" pivot must drop resolved rows in Go. "all" keeps everything; an empty
// problem status is treated as active (never silently hide a pre-status row);
// an unknown inbox mode is treated as "open" (defensive). Pure + table-tested.
func inboxKeepsProblem(problemStatus, inboxStatus string) bool {
	if inboxStatus == "all" {
		return true
	}
	return problemStatus != "resolved"
}

func pickStatus(inboxStatus string) string {
	if inboxStatus == "all" {
		return ""
	}
	// ProblemFilter.Status takes a single value — "open" picks
	// open only, missing acknowledged. The Problems page handles
	// this by passing "" and filtering client-side; we do the
	// same here so the inbox sees both buckets.
	return ""
}

func pickExceptionState(inboxStatus string) string {
	if inboxStatus == "all" {
		return "" // store default excludes 'ignored'
	}
	return "open"
}

func priorityRank(p string) int {
	switch p {
	case "P1":
		return 3
	case "P2":
		return 2
	case "P3":
		return 1
	}
	return 0
}

// problemToInbox normalises a Problem row. Priority +
// PriorityReason ride through verbatim — enrichment already
// computed them so the inbox bucket matches the /problems
// page bucket exactly.
func problemToInbox(p chstore.Problem) InboxItem {
	// (Resolved-row filtering happens at the call site via inboxKeepsProblem —
	// v0.8.287; this normaliser assumes the row already passed that gate.)
	return InboxItem{
		ID:             "problem:" + p.ID,
		Kind:           "problem",
		Source:         "Alert rule",
		Priority:       p.Priority,
		PriorityReason: p.PriorityReason,
		Severity:       p.Severity,
		Service:        p.Service,
		Title:          p.RuleName,
		Description:    p.Description,
		StartedAt:      p.StartedAt,
		LastSeen:       p.StartedAt,
		Assignee:       p.Assignee,
		Status:         p.Status,
		Clusters:       p.Clusters,
		Problem: &InboxProblemRef{
			ID: p.ID, RuleID: p.RuleID, Metric: p.Metric,
			Value: p.Value, Threshold: p.Threshold,
		},
	}
}

// exceptionToInbox derives a triage priority for an exception
// group from occurrences (volume) + recency. The signals we
// have on an exception are different from those on a Problem,
// so the formula is bespoke but the bucket targets the same
// "now / today / when-convenient" semantics:
//
//   P1 — fresh (last_seen ≤ 5min) AND occurrences ≥ 500
//   P2 — fresh (last_seen ≤ 1h)   AND occurrences ≥ 100
//        OR regressed (state="regressed")
//   P3 — everything else
//
// "Fresh + high-volume" is the post-deploy spike pattern the
// oncall most wants to see; everything else is review-able
// later.
func exceptionToInbox(g chstore.ExceptionGroup) InboxItem {
	prio, reason := exceptionPriority(g)
	// We don't currently carry severity on the exception_groups
	// row; surface "—" so the column doesn't lie. Downstream UI
	// renders this as text not a badge.
	return InboxItem{
		ID:             "exception:" + g.Fingerprint,
		Kind:           "exception",
		Source:         "Exception",
		Priority:       prio,
		PriorityReason: reason,
		Severity:       "warning", // best fit until exception rows carry one
		Service:        g.Service,
		Title:          g.Type,
		Description:    inboxTruncate(g.Message, 240),
		StartedAt:      g.FirstSeen,
		LastSeen:       g.LastSeen,
		Assignee:       g.Assignee,
		Status:         g.State,
		Exception: &InboxExceptionRef{
			Fingerprint: g.Fingerprint, Type: g.Type, Message: g.Message,
			Occurrences: g.Occurrences,
		},
	}
}

func exceptionPriority(g chstore.ExceptionGroup) (string, string) {
	age := time.Now().UnixNano() - g.LastSeen
	freshMin := time.Duration(age) <= 5*time.Minute
	freshHour := time.Duration(age) <= time.Hour

	if g.State == "regressed" {
		return "P2", "regressed"
	}
	if freshMin && g.Occurrences >= 500 {
		return "P1", fmt.Sprintf("%d in last 5min", g.Occurrences)
	}
	if freshHour && g.Occurrences >= 100 {
		return "P2", fmt.Sprintf("%d in last hour", g.Occurrences)
	}
	return "P3", "steady"
}

// anomalyToInbox maps a detection score into a priority bucket.
// peak_ratio is how far above the historical baseline this
// metric got at its worst; current_ratio is right now. We use
// the peak for ranking — "worst hit so far" predicts how much
// the operator should care, even if the burst has subsided.
//
//   P1 — peak ≥ 5x baseline (extraordinary spike)
//   P2 — peak ≥ 2x baseline (clear anomaly worth a look)
//   P3 — everything else (mostly cleared / mild)
//
// Anomaly events don't have severity in the data model, so we
// derive one from the ratio bucket and surface it for visual
// consistency only.
func anomalyToInbox(e chstore.AnomalyEvent) InboxItem {
	prio, reason := anomalyPriority(e)
	sev := "info"
	if prio == "P1" {
		sev = "critical"
	} else if prio == "P2" {
		sev = "warning"
	}
	return InboxItem{
		ID:             "anomaly:" + e.ID,
		Kind:           "anomaly",
		Source:         "Anomaly",
		Priority:       prio,
		PriorityReason: reason,
		Severity:       sev,
		Service:        e.Service,
		Title:          fmt.Sprintf("%s · %s", e.Kind, e.Pattern),
		Description:    inboxTruncate(e.Sample, 240),
		StartedAt:      e.StartedAt,
		LastSeen:       e.LastSeen,
		Status:         e.Status,
		Clusters:       e.Clusters,
		Anomaly: &InboxAnomalyRef{
			ID: e.ID, Kind: e.Kind, Pattern: e.Pattern,
			PeakRatio: e.PeakRatio, CurrentRatio: e.CurrentRatio,
		},
	}
}

func anomalyPriority(e chstore.AnomalyEvent) (string, string) {
	if e.Status == "cleared" {
		return "P3", "cleared"
	}
	if math.IsNaN(e.PeakRatio) || e.PeakRatio <= 0 {
		return "P3", "no signal"
	}
	switch {
	case e.PeakRatio >= 5:
		return "P1", fmt.Sprintf("%.1fx baseline", e.PeakRatio)
	case e.PeakRatio >= 2:
		return "P2", fmt.Sprintf("%.1fx baseline", e.PeakRatio)
	default:
		return "P3", "mild"
	}
}

// inboxTruncate caps a string at n characters; the package's
// generic truncate() lives in api.go and has a different signature.
func inboxTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
