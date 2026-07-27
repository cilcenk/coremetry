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

	// v0.9.255 — enrichment results the inbox was already PAYING for and
	// then dropping. listInbox runs EnrichProblemsWithRunbooks /
	// WithDeploys before mapping (three CH round-trips per poll), but
	// problemToInbox never copied the results out, so every triage row
	// arrived without the two facts an operator reaches for first:
	// "is there a runbook" and "did something just deploy". The queries
	// were billed and the answers thrown away.
	//
	// Problem-kind only for now: exceptions and anomalies have their own
	// deploy correlation paths and are not enriched on this route.
	RunbookURL   string                `json:"runbookUrl,omitempty"`
	RecentDeploy *chstore.RecentDeploy `json:"recentDeploy,omitempty"`
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
	// open (default) | all | ignored
	//
	// v0.9.254 — `ignored` added. Until now an exception group silenced
	// with Ignore was reachable from NO inbox pivot: pickExceptionState
	// returned "" for both open and all, and the store's default view
	// excludes ignored (exception_inbox.go). The only surface that could
	// show them — and the only place with an Unignore button — was the
	// /problems page's Ignored tab. Retiring that page without this
	// would have made ignoring PERMANENT and irreversible: a group
	// silenced by mistake could never be found again.
	//
	// `ignored` is its own pivot rather than folding into `all` on
	// purpose. Ignoring is a deliberate silencing act; dumping those
	// rows back into the everyday "all" view would re-add exactly the
	// noise the operator silenced.
	statusFilter := strings.TrimSpace(q.Get("status"))
	switch statusFilter {
	case "open", "all", "ignored":
		// ok
	default:
		statusFilter = "open"
	}
	limit := parseInt(q.Get("limit"), 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	// v0.9.319 — server-side sort. The page sorted the RETURNED rows
	// client-side, so "Last seen ascending" meant "the oldest of the
	// priority-ranked top 300", not "the oldest in the queue". Every column
	// but priority silently answered a different question than the one the
	// header claimed. Ids match the frontend COLS exactly.
	sortID, sortDir := normalizeInboxSort(q.Get("sort"), q.Get("dir"))
	// v0.9.320 — occurrence floor, the /problems one (v0.9.315) finally
	// applied to the surface that is supposed to REPLACE /problems. Operator:
	// "1 defa Java timeout aldığı için problems'ta exception gözüküyor" —
	// and the merged triage queue was showing exactly the same one-off rows.
	// Negative/absent → the default floor; an explicit 0 means "show all"
	// and is honoured, which is why this can't use parseInt's default alone.
	minOcc := normalizeInboxMinOcc(q.Get("minOcc"))

	// v0.9.221 — :v2: marks the response-shape change (bare array → object
	// with the total). Without the bump a pre-upgrade array could still be
	// sitting under this key and would deserialize into the new shape as an
	// empty page.
	cacheKey := inboxListKey(statusFilter, service, search, ownerTeam, sreTeam, env, limit, sortID, sortDir, minOcc)
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
	// v0.9.318 — every narrowing filter below runs on the MERGED list, so the
	// per-source fetch has to cover the candidates those narrows will cut
	// down. See inboxSourceLimit.
	// A non-default sort needs the candidates too: ordering a truncated slice
	// by a key the truncation did not use returns the top of the SLICE, not
	// the top of the queue.
	narrowed := service != "" || search != "" || env != "" || ownerTeam != "" || sreTeam != "" ||
		sortID != inboxSortDefault || sortDir != "desc"
	srcLimit := inboxSourceLimit(limit, narrowed)

	s.serveCached(w, r, cacheKey, 15*time.Second, func(ctx context.Context) (any, error) {
		items := make([]InboxItem, 0, 256)
		// scanCapped: a source came back exactly full, so there were more
		// candidates than we looked at and the narrow below is answering over
		// a slice. Travels with the response — the "no silent caps" rule
		// (v0.9.221) applied to the SCAN, not just the final page.
		scanCapped := false

		// v0.5.245 — service filter is now case-insensitive
		// substring across all three sources. Per-source SQL
		// filters dropped (each capped at 200 rows so a wider
		// fan-out is cheap); the substring narrow happens once
		// over the merged item list below. Operator typing
		// "java" now matches "java-demo", "java-frontend",
		// etc. without remembering the exact service name.
		// ── Problems ─────────────────────────────────────────────
		//
		// v0.9.254 — skipped entirely on the `ignored` pivot. That view is
		// exception-only: Problems are MUTED and anomalies are SILENCED,
		// different verbs backed by different state, so folding them in
		// would make one view mean three things. Skipping also keeps four
		// CH round-trips (list + three enrichers) off a rarely-opened view.
		var probs []chstore.Problem
		if statusFilter != "ignored" {
			var err error
			probs, err = s.store.ListProblems(ctx, chstore.ProblemFilter{
				Status: pickStatus(statusFilter), Limit: srcLimit,
			})
			if err != nil {
				return nil, err
			}
			if len(probs) >= srcLimit {
				scanCapped = true
			}
		}
		// Same enrichment chain Problems UI runs through, so the
		// derived priority lines up exactly. No-ops on the empty slice
		// the `ignored` pivot leaves behind.
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
			State: pickExceptionState(statusFilter), Limit: srcLimit,
		}
		exGroups, err := s.store.ListExceptionGroups(ctx, exFilter)
		if err != nil {
			return nil, err
		}
		if len(exGroups) >= srcLimit {
			scanCapped = true
		}
		for _, g := range exGroups {
			items = append(items, exceptionToInbox(g))
		}

		// ── Anomaly events ───────────────────────────────────────
		// 24h window matches the Anomalies page default. ListAnomaly
		// EventsByService isn't a thing — filter client-side.
		var evs []chstore.AnomalyEvent
		if statusFilter != "ignored" {
			var err error
			evs, err = s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{Limit: srcLimit})
			if err != nil {
				return nil, err
			}
			if len(evs) >= srcLimit {
				scanCapped = true
			}
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

		// Occurrence floor — last narrow, so `hidden` is honest (see
		// applyInboxMinOcc).
		items, hiddenByMinOcc := applyInboxMinOcc(items, minOcc)

		// Rank the WHOLE candidate set before the cap (v0.9.318 scan fix +
		// v0.9.319 server sort). Sorting after the cap would rank a page,
		// which is the same lie the filters used to tell.
		sortInboxItems(items, sortID, sortDir)
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
			// scanCapped says "there were more candidates than I looked at",
			// which is a DIFFERENT statement from truncated ("more matches
			// than I returned"). Under a search the second can be false while
			// the first is true — that combination is precisely the case
			// where an empty table must not be read as an empty queue.
			"scanCapped": scanCapped,
			// Never silent: the floor says what it hid so the UI can offer
			// those rows in one click. A rare exception that fires once can
			// still be the important one.
			"minOcc":         minOcc,
			"hiddenByMinOcc": hiddenByMinOcc,
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

// inboxNarrowScan is the per-source candidate ceiling used when a narrowing
// filter is active. All three sources are small ReplacingMergeTree state
// tables (problems, exception_groups, anomaly_events) read with FINAL — a
// 2000-row slice off one is a state-table read, not a spans scan.
const inboxNarrowScan = 2000
const inboxBaseScan = 200

// inboxSourceLimit decides how many rows to pull from EACH source before the
// merge.
//
// v0.9.318 — until now this was a hardcoded 200 per source while every
// narrowing filter (service / q / env / owner / sre) ran on the MERGED list
// AFTERWARDS. That ordering is a lie of exactly the shape this codebase keeps
// paying for: the filter narrows a slice that was already truncated, so a row
// matching "OOMKill" sitting at rank 400 of its source is invisible — the
// operator searches, sees an empty table, and concludes the queue is clean.
// The filter looked like it searched the queue; it searched the first page.
//
// Two independent defects fixed here:
//
//   - narrowed → scan the candidate set, not a page of it. The narrow can only
//     REMOVE rows, so to answer honestly you need the candidates first.
//   - unnarrowed → per-source must at least cover the requested limit. With
//     200/source a limit=300 request (which is what the page asks for) could
//     not be satisfied from a single source even when that source held 300
//     genuinely open rows.
//
// Pure so the arithmetic is pinned by test rather than by reading the handler.
func inboxSourceLimit(limit int, narrowed bool) int {
	n := inboxBaseScan
	if narrowed {
		n = inboxNarrowScan
	}
	if limit > n {
		n = limit
	}
	return n
}

// inboxListKey builds the /api/inbox cache key. Pure + hoisted so
// cache_key-style tests can pin it (canonical: cache_key_test.go).
//
// EVERY input that changes the response is in the key. v0.9.251 added
// `q`: a free-text search that altered the rows but not the key would
// hand one operator's filtered page to another operator's unfiltered
// request — the v0.5.187 cross-poisoning shape, with a search box as
// the vector. The `:v2:` prefix marks the v0.9.221 response-shape
// change (bare array → object with the total).
func inboxListKey(status, service, search, ownerTeam, sreTeam, env string, limit int, sortID, sortDir string, minOcc uint64) string {
	return fmt.Sprintf("inbox:v3:status=%s:svc=%s:q=%s:owner=%s:sre=%s:env=%s:limit=%d:sort=%s:dir=%s:minOcc=%d",
		status, service, search, ownerTeam, sreTeam, env, limit, sortID, sortDir, minOcc)
}

// inboxDefaultMinOcc — a group has to have fired this many times before it
// counts as something to triage. A one-shot failure is a fact about the
// window, not a problem. Matches the /problems floor so the two surfaces
// can't disagree about what "noise" means.
const inboxDefaultMinOcc = 5

// normalizeInboxMinOcc parses ?minOcc=. Absent → the default floor; an
// explicit "0" → no floor ("show all"), which is the affordance that keeps
// the filtering non-silent. Garbage → the default, never an error: a
// hand-edited URL should still open a usable queue.
func normalizeInboxMinOcc(raw string) uint64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return inboxDefaultMinOcc
	}
	n := parseInt(raw, -1)
	if n < 0 {
		return inboxDefaultMinOcc
	}
	return uint64(n)
}

// applyInboxMinOcc drops exception rows below the floor and reports how many
// it hid.
//
// Exception-kind only: Problems come from alert rules and anomalies from
// detectors — neither carries an occurrence count, and silently dropping a
// firing P1 alert because it fired once would be the opposite of triage.
//
// Applied LAST, after every narrow, so the hidden count means "rows that
// passed everything else and failed only the floor". Counting at map time
// would overstate it by including rows the service/env filter would have
// removed anyway — and an inflated "42 hidden" is its own kind of lie.
func applyInboxMinOcc(items []InboxItem, minOcc uint64) ([]InboxItem, int) {
	if minOcc == 0 {
		return items, 0
	}
	kept := items[:0]
	hidden := 0
	for _, it := range items {
		if it.Kind == "exception" && it.Exception != nil && it.Exception.Occurrences < minOcc {
			hidden++
			continue
		}
		kept = append(kept, it)
	}
	return kept, hidden
}

// inboxSortDefault is the historical rank: priority desc, most-recent first
// within a priority. Preserved exactly so an operator who never touches a
// header sees the page they saw before v0.9.319.
const inboxSortDefault = "priority"

// normalizeInboxSort validates the ?sort=/?dir= pair against the columns the
// page actually renders. Anything unknown falls back to the default rather
// than erroring: a stale link from an older build must still open a usable
// queue, not a 400.
func normalizeInboxSort(id, dir string) (string, string) {
	switch id {
	case "priority", "source", "service", "detail", "lastSeen", "assignee":
	default:
		id = inboxSortDefault
	}
	if dir != "asc" {
		dir = "desc"
	}
	return id, dir
}

// sortInboxItems ranks the merged triage set in place.
//
// Every branch keeps the priority-desc, lastSeen-desc tiebreak underneath, so
// two rows equal on the chosen column still arrive in triage order instead of
// whatever order the three sources happened to merge in. Stable on top of
// that, so repeated polls don't shuffle equal rows under the operator's
// cursor.
func sortInboxItems(items []InboxItem, id, dir string) {
	asc := dir == "asc"
	less := func(a, b InboxItem) bool {
		switch id {
		case "source":
			if a.Source != b.Source {
				return a.Source < b.Source
			}
		case "service":
			if !strings.EqualFold(a.Service, b.Service) {
				return strings.ToLower(a.Service) < strings.ToLower(b.Service)
			}
		case "detail":
			if !strings.EqualFold(a.Title, b.Title) {
				return strings.ToLower(a.Title) < strings.ToLower(b.Title)
			}
		case "lastSeen":
			if a.LastSeen != b.LastSeen {
				return a.LastSeen < b.LastSeen
			}
		case "assignee":
			if a.Assignee != b.Assignee {
				return a.Assignee < b.Assignee
			}
		default: // priority
			ra, rb := priorityRank(a.Priority), priorityRank(b.Priority)
			if ra != rb {
				return ra < rb
			}
		}
		// Tiebreak — ALWAYS triage order, never reversed with dir. A page
		// sorted by service ascending should still show P1 above P3 inside
		// one service; flipping the tiebreak with the header would bury the
		// urgent row at the bottom of its own group.
		ra, rb := priorityRank(a.Priority), priorityRank(b.Priority)
		if ra != rb {
			return ra > rb
		}
		return a.LastSeen > b.LastSeen
	}
	sort.SliceStable(items, func(i, j int) bool {
		if asc {
			return less(items[i], items[j])
		}
		// Descending is the mirror of the primary key only; the tiebreak
		// above is already triage-ordered, so re-running less() with the
		// arguments swapped would invert it too. Compare primaries here and
		// fall through to less() when they are equal.
		if inboxPrimaryEqual(items[i], items[j], id) {
			return less(items[i], items[j])
		}
		return less(items[j], items[i])
	})
}

// inboxPrimaryEqual reports whether two rows tie on the chosen sort column —
// the point at which the (never-reversed) triage tiebreak takes over.
func inboxPrimaryEqual(a, b InboxItem, id string) bool {
	switch id {
	case "source":
		return a.Source == b.Source
	case "service":
		return strings.EqualFold(a.Service, b.Service)
	case "detail":
		return strings.EqualFold(a.Title, b.Title)
	case "lastSeen":
		return a.LastSeen == b.LastSeen
	case "assignee":
		return a.Assignee == b.Assignee
	default:
		return priorityRank(a.Priority) == priorityRank(b.Priority)
	}
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
	switch inboxStatus {
	case "ignored":
		// v0.9.254 — the ONLY pivot that reaches silenced groups. The
		// store's default view (and therefore "all") excludes them.
		return chstore.ExStateIgnored
	case "all":
		return "" // store default — still excludes 'ignored'
	default:
		return "open"
	}
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
		// v0.9.255 — see the field comments: these were computed by the
		// enrichment chain in listInbox and then discarded here.
		RunbookURL:   p.RunbookURL,
		RecentDeploy: p.RecentDeploy,
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
