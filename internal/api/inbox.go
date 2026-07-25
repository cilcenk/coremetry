package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

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
	ID             string `json:"id"`       // composite: "<kind>:<nativeId>"
	Kind           string `json:"kind"`     // problem | exception | anomaly
	Source         string `json:"source"`   // human label: "Alert rule" / "Exception" / "Anomaly"
	Priority       string `json:"priority"` // P1 | P2 | P3
	PriorityReason string `json:"priorityReason"`
	Severity       string `json:"severity"` // critical | warning | info
	Service        string `json:"service"`
	Title          string `json:"title"` // rule name / exception type / pattern
	Description    string `json:"description"`
	StartedAt      int64  `json:"startedAt"` // unix ns
	LastSeen       int64  `json:"lastSeen"`  // unix ns; for problems == StartedAt
	Assignee       string `json:"assignee,omitempty"`
	// OwnerTeam + SRETeam attached server-side from
	// service_metadata so the inbox can render team chips
	// without each row firing a per-service lookup. Empty when
	// no catalog row exists for the service. OwnerTeam mirrors
	// what's auto-set on Problem.Assignee at open time;
	// surfacing it on every row (even exceptions / anomalies)
	// keeps the column meaningful across kinds.
	OwnerTeam string `json:"ownerTeam,omitempty"`
	SRETeam   string `json:"sreTeam,omitempty"`
	Status    string `json:"status"` // open | acknowledged | resolved (problems);
	// open | regressed (exceptions); active | cleared (anomalies)
	Clusters []string `json:"clusters,omitempty"`
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
	Kind         string  `json:"kind"` // "log_pattern" | "trace_op"
	Pattern      string  `json:"pattern"`
	PeakRatio    float64 `json:"peakRatio"`
	CurrentRatio float64 `json:"currentRatio"`
}

// inbox unifies the three triage sources into one ranked list.
// Kept aggressively bounded — at 1000s of services, an inbox
// that returns 5k items isn't actionable. Default cap 200,
// max 500; the operator filters by priority/service/kind to
// shrink further. Cached 15s — see the TTL comment at the
// serveCached call below for why it can't be 10.
func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	// v0.9.251 — free-text triage search. `service` stays a
	// service-only substring filter (older shared links and other
	// callers depend on it); `q` is the broader one the page's single
	// search box drives, matching the service, the TITLE and the
	// source label. Title was the gap: with thousands of open items an
	// operator could narrow to a service but never to "timeout" or
	// "OOMKilled".
	search := strings.TrimSpace(q.Get("q"))
	ownerTeam := strings.TrimSpace(q.Get("ownerTeam"))
	sreTeam := strings.TrimSpace(q.Get("sreTeam"))
	// v0.8.387 — global ?env= picker, service-scoped semantics shared
	// with /problems (envKeepsRow): keep rows whose service ran in the
	// env in the last hour, plus service-less (global) rows. Applied
	// post-merge so all three sources filter identically.
	env := strings.TrimSpace(q.Get("env"))
	statusFilter := strings.TrimSpace(q.Get("status")) // open (default) | all
	if statusFilter == "" {
		statusFilter = "open"
	}
	limit := parseInt(q.Get("limit"), 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	// v0.9.221 — :v2: marks the response-shape change (bare array → object
	// with the total). Without the bump a pre-upgrade array could still be
	// sitting under this key and would deserialize into the new shape as an
	// empty page.
	cacheKey := inboxListKey(statusFilter, service, search, ownerTeam, sreTeam, env, limit)
	// v0.9.228 — 10s → 15s. v0.9.220 gave the inbox list a 30s poll; at a 10s
	// TTL the SWR window is ttl×staleFactor = 30s and the Redis entry expires
	// at 30s too, so each poll arrived at age = 30s + previous latency —
	// always PAST the window, on an already-evicted key. Every single poll
	// therefore paid the full cold path: 8 sequential CH round-trips
	// (ListProblems → 3 enrichers → exceptions → anomalies → env members →
	// service metadata) inside the request. At 15s the window is 45s > 30s,
	// so the poll lands on STALE and returns in ~10ms while refreshing behind
	// it. Identical arithmetic to problems-count (api.go:9020), problems-list
	// (api.go:9132) and inbox-count (below) — the list was the one endpoint
	// that never got it.
	s.serveCached(w, r, cacheKey, 15*time.Second, func(ctx context.Context) (any, error) {
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

		// v0.9.251 — free-text search across the fields an operator
		// actually reads in the row: service, title, and the source
		// label ("Alert rule" / "Exception" / "Anomaly"). Applied on
		// the merged set like the service filter above, so all three
		// kinds match identically. Case-insensitive substring — no
		// tokenising: triage titles are short and operators paste
		// fragments ("OOMKill", "504") rather than words.
		if search != "" {
			needle := strings.ToLower(search)
			filtered := items[:0]
			for _, it := range items {
				if strings.Contains(strings.ToLower(it.Service), needle) ||
					strings.Contains(strings.ToLower(it.Title), needle) ||
					strings.Contains(strings.ToLower(it.Source), needle) {
					filtered = append(filtered, it)
				}
			}
			items = filtered
		}

		// Env filter (v0.8.387) — one cached-map lookup covers the whole
		// merged list (no per-poll query beyond it). Soft-fails to
		// UNFILTERED on a map error, matching envScopeProblems: a
		// transient CH blip must never hide a firing P1. envKeepsRow
		// pins the row semantics (empty-service rows always survive).
		if env != "" {
			if members, err := s.store.EnvMemberServices(ctx, env); err == nil {
				memberSet := make(map[string]bool, len(members))
				for _, m := range members {
					memberSet[m] = true
				}
				filtered := items[:0]
				for _, it := range items {
					if envKeepsRow(it.Service, memberSet) {
						filtered = append(filtered, it)
					}
				}
				items = filtered
			}
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
		// v0.9.221 — the cap used to truncate SILENTLY: the response was a
		// bare array, so 200 rows looked identical whether that was the whole
		// queue or the top slice of 900. Since the sort above is priority-desc,
		// what fell off was the low-priority tail — the operator cleared the
		// visible list and believed the queue was empty. CLAUDE.md's "no
		// silent caps" rule; the total travels with the page now.
		total := len(items)
		if len(items) > limit {
			items = items[:limit]
		}
		return map[string]any{
			"items":     items,
			"total":     total,
			"limit":     limit,
			"truncated": total > limit,
		}, nil
	})
}

// pickStatus translates the inbox filter into a Problem status.
// "open" inbox shows open + acknowledged (still in-flight);
// "all" passes through to the store's no-filter.
// inboxCount serves GET /api/inbox/count — the single triage badge total
// (v0.8.288, Option B Slice 1b). Sums the three inbox sources with the SAME
// "open" semantics the inbox uses: not-resolved Problems (open+acknowledged,
// consistent with inboxKeepsProblem), open Exception groups, and active
// Anomaly events. COUNT-only on small state tables (no enrichment/sort), 10s
// cache — cheap enough for the 30s sidebar poll at scale.
func (s *Server) inboxCount(w http.ResponseWriter, r *http.Request) {
	// v0.8.472 (perf dalga-1 #2) — ölçülen en büyük tekil gecikme (rozet
	// 24h p95 7.9s): 4 ARDIŞIK CH count'u 3 paralel sorguya indi
	// (open+ack tek IN'li FINAL taraması) → soğuk maliyet toplam yerine
	// max(). TTL 10s→15s: SWR penceresi 45s > 30s poll — rozet STALE
	// yolundan <10ms döner; problem/exception mutasyonları inbox:count'u
	// anında düşürür (read-your-writes).
	// v0.9.219 — env joins the key. The badge used to ignore ?env= while the
	// inbox LIST honoured it, so with an env picked the sidebar said 47 and
	// the page it linked to showed 12. Cache invalidation is prefix-based
	// (cacheInvalidatePrefix "inbox:count"), so every env variant still
	// drops on a problem/exception mutation.
	env := strings.TrimSpace(r.URL.Query().Get("env"))
	s.serveCached(w, r, inboxCountKey(env), 15*time.Second, func(ctx context.Context) (any, error) {
		return s.computeInboxCountFor(ctx, env)
	})
}

// inboxCountKey is shared by the handler and the warm loop (api.go) so the
// pre-warmed payload and the served one can never diverge.
func inboxCountKey(env string) string { return "inbox:count:env=" + env }

// inboxListKey builds the /api/inbox cache key. Pure + hoisted so
// cache_key-style tests can pin it (canonical: cache_key_test.go).
//
// EVERY input that changes the response is in the key. v0.9.251 added
// `q`: a free-text search that altered the rows but not the key would
// hand one operator's filtered page to another operator's unfiltered
// request — the v0.5.187 cross-poisoning shape, with a search box as
// the vector. The `:v2:` prefix marks the v0.9.221 response-shape
// change (bare array → object with the total).
func inboxListKey(status, service, search, ownerTeam, sreTeam, env string, limit int) string {
	return fmt.Sprintf("inbox:v2:status=%s:svc=%s:q=%s:owner=%s:sre=%s:env=%s:limit=%d",
		status, service, search, ownerTeam, sreTeam, env, limit)
}

// computeInboxCount — rozet toplamının tek hesabı; hem inboxCount
// handler'ı hem 25s warm-loop (v0.8.473) aynı fonksiyonu çağırır ki
// ısıtılan payload ile canlı payload asla ıraksamasın.
func (s *Server) computeInboxCount(ctx context.Context) (any, error) {
	return s.computeInboxCountFor(ctx, "")
}

// computeInboxCountFor scopes the badge to an environment using the SAME
// semantics as the inbox list (envKeepsRow): a service-less row always
// counts, otherwise the service must be an env member.
//
// On an env-map error the count stays UNFILTERED — identical to the list's
// soft-fail (inbox.go:184) and for the same reason: a transient CH blip must
// never hide a firing P1 behind a badge that silently reads 0.
func (s *Server) computeInboxCountFor(ctx context.Context, env string) (any, error) {
	// nil = no env constraint. Non-nil (possibly empty) = constrain.
	var envServices []string
	if env != "" {
		if members, err := s.store.EnvMemberServices(ctx, env); err == nil {
			envServices = members
			if envServices == nil {
				envServices = []string{} // resolved, but empty — keep it non-nil
			}
		}
	}
	var (
		probN, anN uint64
		exN        int64
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		probN, err = s.store.CountProblemsInStatuses(gctx, []string{"open", "acknowledged"}, envServices)
		return err
	})
	g.Go(func() error {
		// Exception groups always carry a service (they're derived from
		// spans), so there is no service-less bucket to preserve here: an
		// env that resolves to no services can only mean zero. The shared
		// filter treats an empty Services slice as "no constraint", so that
		// case is short-circuited rather than passed down.
		if envServices != nil && len(envServices) == 0 {
			exN = 0
			return nil
		}
		var err error
		exN, err = s.store.CountExceptionGroups(gctx, chstore.ExceptionGroupFilter{
			State:    pickExceptionState("open"),
			Services: envServices,
		})
		return err
	})
	g.Go(func() error {
		var err error
		anN, err = s.store.CountActiveAnomalyEvents(gctx, 0, envServices)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	problems := probN
	exceptions := uint64(exN)
	return map[string]any{
		"count":      problems + exceptions + anN,
		"problems":   problems,
		"exceptions": exceptions,
		"anomalies":  anN,
	}, nil
}

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
//	P1 — fresh (last_seen ≤ 5min) AND occurrences ≥ 500
//	P2 — fresh (last_seen ≤ 1h)   AND occurrences ≥ 100
//	     OR regressed (state="regressed")
//	P3 — everything else
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
//	P1 — peak ≥ 5x baseline (extraordinary spike)
//	P2 — peak ≥ 2x baseline (clear anomaly worth a look)
//	P3 — everything else (mostly cleared / mild)
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
