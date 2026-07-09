package api

// Logs read + pattern handlers. Split out of api.go for code
// organisation (behaviour-preserving). Everything here routes
// through the logstore.Store abstraction (CH or external ES) so a
// handler never ties itself to one backend. Shared helpers
// (writeJSON, writeErr, parseTime, parseInt, parseDuration,
// s.serveCached, s.audit) stay in api.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// logsTailCadence is how often the streamLogs handler issues a forward read.
// The read is a cheap bounded delta (time>=cursor, no count, no PIT), but each
// connected live tab drives its own poll — at billion-doc ES scale a fleet of
// open Logs tabs is real backend load. The ~10s OTel ingest lag means a faster
// tick surfaces nothing sooner anyway, so 10s (the CLAUDE.md polling floor)
// halves the per-tab Elasticsearch query rate with zero perceptible latency
// cost (v0.8.x — operator asked to ease ES request pressure).
const logsTailCadence = 10 * time.Second

// v0.8.350 (HA 🟡6) — Go-side ceilings for the logs read paths that
// degrade on a slow/unreachable backend (ErrBackendSlow → 200
// {degraded:true}, extending the v0.8.331 trace-pivot contract).
// The ES transport's 15s ResponseHeaderTimeout is the tighter wire
// bound; these keep the CH backend + goroutine lifetimes bounded too.
const (
	logsSearchBudget  = 30 * time.Second // /logs main search
	logsContextBudget = 10 * time.Second // /logs/context, per half-window
)

// tailStep is the pure cursor-advance for the live-tail loop. Given the
// current forward cursor `sinceNs`, the ids already emitted at exactly that
// ns (`boundary`), and the oldest-first batch a forward read returned, it
// returns the rows to emit (same-ns boundary dups removed), the next cursor,
// the rebuilt boundary set, and whether the batch saturated the per-tick
// `cap` (→ a gap marker). Extracted + unit-tested because it carries the two
// load-bearing invariants: never silently drop a row, and always make
// forward progress (even when >cap rows share one nanosecond).
func tailStep(
	sinceNs int64, boundary map[int64]struct{},
	rows []*logstore.LogRecord, cap int,
) (emit []*logstore.LogRecord, nextSince int64, nextBoundary map[int64]struct{}, gap bool) {
	maxTs := sinceNs
	for _, lg := range rows {
		if lg.Timestamp > maxTs {
			maxTs = lg.Timestamp
		}
	}
	for _, lg := range rows {
		if lg.Timestamp == sinceNs {
			if _, seen := boundary[lg.ID]; seen {
				continue // already emitted this boundary row on a prior tick
			}
		}
		emit = append(emit, lg)
	}
	gap = len(rows) >= cap
	switch {
	case maxTs > sinceNs:
		// Advanced to a newer ns — the boundary is now the ids at maxTs.
		nextSince = maxTs
		nextBoundary = map[int64]struct{}{}
		for _, lg := range rows {
			if lg.Timestamp == maxTs {
				nextBoundary[lg.ID] = struct{}{}
			}
		}
	case gap:
		// A whole saturated batch at the boundary ns (>cap rows at one ns) —
		// can't advance by timestamp; step past it. The gap marker tells the
		// client some same-ns lines were skipped.
		nextSince = sinceNs + 1
		nextBoundary = map[int64]struct{}{}
	default:
		// No newer ns; remember everything at the boundary so the inclusive
		// `>=` re-read next tick doesn't re-emit it (handles late ingestion).
		nextSince = sinceNs
		nextBoundary = map[int64]struct{}{}
		for k := range boundary {
			nextBoundary[k] = struct{}{}
		}
		for _, lg := range rows {
			if lg.Timestamp == sinceNs {
				nextBoundary[lg.ID] = struct{}{}
			}
		}
	}
	return
}

// streamLogs is the live-tail SSE endpoint — a DEDICATED per-connection
// handler (the internal/sse Broker is a broadcast bus; a log tail is
// per-query with its own filters + forward cursor). On a ticker it issues
// a forward-tail read (logstore.Filter.SinceNs) and pushes only NEW rows.
// It seeds the cursor from ?since=<unix ns> (the client's newest buffered
// row, for reconnect catch-up) or from now. Same filter set as getLogs.
// Read-only: no serveCached (uncacheable live edge), no audit.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	base := logstore.Filter{
		Service:     q.Get("service"),
		Cluster:     q.Get("cluster"),
		Env:         strings.TrimSpace(q.Get("env")), // v0.8.400 — env-separation Phase 4
		Search:      q.Get("search"),
		SeverityMin: uint8(sev),
		TraceID:     q.Get("traceId"),
		SpanID:      q.Get("spanId"),
		Limit:       logstore.LogsTailMax,
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no") // disable NGINX buffering
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprint(w, ": ok\n\n")
	flusher.Flush()

	// v0.8.236 — shared broadcaster. Subscribe FIRST so tick batches
	// buffer in the bounded mailbox while the one-shot catch-up below
	// fills the [client since → group cursor] hole; nothing is lost in
	// between. The group owns the forward cursor + tailStep semantics
	// (inclusive >= re-read, boundary dedup, gap on saturation) — one
	// backend query per tick per DISTINCT filter, not per tab.
	sub, cursor, detach := s.tails.subscribe(base)
	defer detach()

	// Reconnect catch-up: one bounded read from the client's newest row
	// up to where the shared stream resumes. Same 1-dup edge at the
	// exact boundary ns as the old per-connection loop (which also
	// seeded an empty boundary set from ?since) — the UI keys rows by id.
	if v, err := strconv.ParseInt(q.Get("since"), 10, 64); err == nil && v > 0 && v < cursor {
		cctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		cf := base
		cf.SinceNs = v
		cf.To = time.Unix(0, cursor)
		page, err := s.logs.Search(cctx, cf)
		cancel()
		if err != nil {
			log.Printf("[logs-stream] catch-up failed (backend=%s): %v", s.logs.Backend(), err)
		} else {
			for _, lg := range page.Logs {
				if data, err := json.Marshal(lg); err == nil {
					fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
				}
			}
			if len(page.Logs) >= logstore.LogsTailMax {
				fmt.Fprint(w, "event: gap\ndata: {\"dropped\":true}\n\n")
			}
			flusher.Flush()
		}
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-sub.ch:
			for _, lg := range ev.logs {
				if data, err := json.Marshal(lg); err == nil {
					fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
				}
			}
			if ev.gap {
				// Fell behind (tick cap or a slow connection's dropped
				// batch) — signal rather than silently omit lines.
				fmt.Fprint(w, "event: gap\ndata: {\"dropped\":true}\n\n")
			}
			flusher.Flush()
		}
	}
}

// logsFieldStatsKey / logsTimeseriesKey — the /api/logs/fieldstats and
// /api/logs/timeseries cache keys, extracted pure alongside
// logsSearchKey (v0.8.400) for the same hash-ALL-inputs test.
func logsFieldStatsKey(field string, f logstore.Filter, fromRaw, toRaw string) string {
	return fmt.Sprintf("logs-fieldstats:f=%s:svc=%s:cl=%s:env=%s:sev=%d:trace=%s:span=%s:from=%s:to=%s:q=%s",
		field, f.Service, f.Cluster, f.Env, f.SeverityMin, f.TraceID, f.SpanID,
		fromRaw, toRaw, f.Search)
}

func logsTimeseriesKey(f logstore.Filter, fromRaw, toRaw string, bucketSec int, groupBy string) string {
	return fmt.Sprintf("logs-ts:svc=%s:env=%s:sev=%d:trace=%s:from=%s:to=%s:b=%d:g=%s:q=%s",
		f.Service, f.Env, f.SeverityMin, f.TraceID, fromRaw, toRaw,
		bucketSec, groupBy, f.Search)
}

// logsSearchKey builds the /api/logs cache key. Pure + extracted
// (v0.8.400) so the hash-ALL-inputs contract is table-testable — env
// joined the filter set and an env-filtered response must never
// cross-poison the unfiltered one inside the 15s TTL (the v0.5.187
// class; pinned by logs_env_key_test.go).
func logsSearchKey(f logstore.Filter, fromRaw, toRaw string) string {
	return fmt.Sprintf("logs:svc=%s:clu=%s:env=%s:sev=%d:trace=%s:span=%s:from=%s:to=%s:lim=%d:off=%d:cur=%s:q=%s",
		f.Service, f.Cluster, f.Env, f.SeverityMin, f.TraceID, f.SpanID,
		fromRaw, toRaw, f.Limit, f.Offset, f.Cursor, f.Search)
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	f := logstore.Filter{
		Service:     q.Get("service"),
		Cluster:     q.Get("cluster"),
		Env:         strings.TrimSpace(q.Get("env")), // v0.8.400 — env-separation Phase 4
		Search:      q.Get("search"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		SeverityMin: uint8(sev),
		TraceID:     q.Get("traceId"),
		SpanID:      q.Get("spanId"),
		Limit:       parseInt(q.Get("limit"), 100),
		Offset:      parseInt(q.Get("offset"), 0),
		// v0.7.22 (SAFE-CORE) — opaque keyset cursor. The UI passes
		// back the prior response's nextCursor; backend-owned format.
		Cursor: q.Get("after"),
	}
	// v0.8.x — cache the static log search (15s). This was the ONLY ES-backed
	// read still hitting Elasticsearch uncached on every request; the live edge
	// is served by the SSE tail (streamLogs), not this path, so concurrent
	// operators, pagination round-trips and rapid filter toggles all fanned out
	// straight to ES. Key hashes EVERY filter input; raw from/to strings keep a
	// relative window stable within the TTL (mirrors getLogsTimeseries).
	// (operator asked to ease ES request pressure).
	key := logsSearchKey(f, q.Get("from"), q.Get("to"))
	s.serveCached(w, r, key, 15*time.Second, func(ctx context.Context) (any, error) {
		// v0.8.330 (pivot Phase 2) — the TRACE-LOGS branch (?traceId=, the
		// Trace Logs tab) must never block or 5xx the trace view on a slow/
		// unreachable log backend: bound the round-trip at 3s
		// (logstore.SearchWithTimeout) and degrade to an HTTP 200 empty
		// payload with {degraded, reason} instead of an error. Caching the
		// degraded result for the 15s TTL is deliberate back-pressure — a
		// struggling backend isn't re-hammered by every tab refresh. Genuine
		// query errors still fail loudly (only ErrBackendSlow degrades), and
		// the non-trace search path below is byte-for-byte unchanged.
		if f.TraceID != "" {
			page, err := logstore.SearchWithTimeout(ctx, s.logs, f, 0)
			if err != nil {
				if errors.Is(err, logstore.ErrBackendSlow) {
					log.Printf("[logs] trace-logs degraded (backend=%s, trace=%q): %v",
						s.logs.Backend(), f.TraceID, err)
					return map[string]interface{}{
						// `items` is the Phase 2 degraded contract the Phase 3
						// client reads; `total`/`logs` keep today's client
						// rendering an honest empty list, not a crash.
						"items":    []*logstore.LogRecord{},
						"total":    0,
						"logs":     []*logstore.LogRecord{},
						"degraded": true,
						"reason":   "log backend slow/unreachable",
					}, nil
				}
				log.Printf("[logs] search failed (backend=%s, service=%q, trace=%q): %v",
					s.logs.Backend(), f.Service, f.TraceID, err)
				return nil, err
			}
			return logsSearchPayload(page), nil
		}
		// v0.8.350 (HA 🟡6) — the MAIN search path now degrades like the
		// trace branch above: slow/unreachable backend → 200
		// {degraded:true} + empty list instead of a 5xx, so an ES
		// brownout renders "backend slow" on /logs, not a red error
		// wall. 30s Go-side ceiling (the ES transport's 15s response-
		// header timeout is the tighter bound); caching the degraded
		// payload for the 15s TTL is deliberate back-pressure. Genuine
		// query errors still fail loudly (only ErrBackendSlow degrades).
		page, err := logstore.SearchWithTimeout(ctx, s.logs, f, logsSearchBudget)
		if err != nil {
			if errors.Is(err, logstore.ErrBackendSlow) {
				log.Printf("[logs] search degraded (backend=%s, service=%q): %v",
					s.logs.Backend(), f.Service, err)
				return map[string]interface{}{
					"total":    0,
					"logs":     []*logstore.LogRecord{},
					"degraded": true,
					"reason":   "log backend slow/unreachable",
				}, nil
			}
			// Surface the full backend error (ES carries the authz/index reason
			// in the response body via res.String()) in the pod log so the
			// operator can grep "[logs]" instead of only seeing it in the API
			// response.
			log.Printf("[logs] search failed (backend=%s, service=%q, trace=%q): %v",
				s.logs.Backend(), f.Service, f.TraceID, err)
			return nil, err
		}
		return logsSearchPayload(page), nil
	})
}

// logsSearchPayload maps a logstore Page to the /api/logs wire shape.
// envUnapplied (v0.8.400) is included only when set — the ES backend's
// honest "?env= was requested but no environment field resolved" flag;
// the /logs page renders the warning chip off it instead of silently
// showing an unfiltered result (the v0.8.398 honesty pattern).
func logsSearchPayload(page *logstore.Page) map[string]interface{} {
	out := map[string]interface{}{
		"total":      page.Total,
		"logs":       page.Logs,
		"nextCursor": page.NextCursor,
	}
	if page.EnvUnapplied {
		out["envUnapplied"] = true
	}
	return out
}

// getLogsFields surfaces the searchable field paths discovered
// from the logs backend's index mapping. Used by the /logs UI's
// "available fields" hint so the operator sees what they can
// filter by without guessing. Only the Elasticsearch backend
// implements ListFields; ClickHouse returns an empty list (its
// shape is fixed and the UI already knows it).
//
// Cached 60s — mappings rarely change and an open /logs tab
// re-fetches on focus.
func (s *Server) getLogsFields(w http.ResponseWriter, r *http.Request) {
	type fielder interface {
		ListFields(ctx context.Context) ([]string, error)
	}
	s.serveCached(w, r, "logs-fields", 60*time.Second, func(ctx context.Context) (any, error) {
		// Unwrap (v0.8.232): the Switchable wrapper doesn't forward
		// optional capabilities — assert on the live inner store.
		f, ok := logstore.Unwrap(s.logs).(fielder)
		if !ok {
			return map[string]any{"fields": []string{}, "backend": s.logs.Backend()}, nil
		}
		fields, err := f.ListFields(ctx)
		if err != nil {
			log.Printf("[logs] field discovery failed (backend=%s): %v", s.logs.Backend(), err)
			return nil, err
		}
		if fields == nil {
			fields = []string{}
		}
		return map[string]any{"fields": fields, "backend": s.logs.Backend()}, nil
	})
}

// getLogsFieldValues returns top values of a single keyword
// field matching a typed prefix (v0.5.464). Powers the /logs
// search box autocomplete that fires when the operator types
// "service.name:" — the dropdown then shows real service names
// from the indexed data. Sub-ms latency via ES _terms_enum on
// keyword subfields; CH backend returns an empty list.
//
// Cached briefly (30s) — values change slowly and the operator
// will type new prefixes in rapid succession (each is a fresh
// cache key).

// adminElasticIndices returns per-index doc count, size, health,
// and ILM lifecycle (policy + phase) for the configured logs
// backend's index pattern. Powers /admin/elastic. CH backend
// returns nil — the frontend renders a "logs backend isn't
// Elasticsearch" empty state instead of crashing. v0.5.466.
//
// Admin-only — exposes cluster metadata that's broadly fine to
// share but not interesting to a viewer. 30s cache: indices
// don't churn often.
func (s *Server) adminElasticIndices(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "admin-elastic-indices", 30*time.Second, func(ctx context.Context) (any, error) {
		idx, err := s.logs.Indices(ctx)
		if err != nil {
			log.Printf("[logs] indices failed (backend=%s): %v", s.logs.Backend(), err)
			return nil, err
		}
		if idx == nil {
			idx = []logstore.IndexInfo{}
		}
		return map[string]any{
			"backend": s.logs.Backend(),
			"indices": idx,
		}, nil
	})
}

// adminElasticErrors returns the ES backend's recent failed queries +
// cumulative error counter (v0.8.230, operator-requested: "when ES has
// a problem I want to see the queries it sent"). In-memory per-pod
// snapshot — deliberately NOT cached: it must reflect the error the
// operator just triggered, and the read is a mutex-guarded copy of ≤20
// entries (no CH/ES round-trip, so serveCached would add nothing). The
// CH backend (no Diagnoser) returns an empty payload so the panel
// renders its empty state.
func (s *Server) adminElasticErrors(w http.ResponseWriter, r *http.Request) {
	d := logstore.ESDiagnostics{RecentErrors: []logstore.ESQueryError{}}
	if diag, ok := logstore.Unwrap(s.logs).(logstore.Diagnoser); ok {
		d = diag.Diagnostics()
		if d.RecentErrors == nil {
			d.RecentErrors = []logstore.ESQueryError{}
		}
	}
	writeJSON(w, map[string]any{
		"backend":      s.logs.Backend(),
		"queryErrors":  d.QueryErrors,
		"recentErrors": d.RecentErrors,
	})
}

// adminLogstoreTraceContext — GET /api/admin/logstore/trace-context
// (v0.8.348, pivot Phase 1c SELF-DISCOVERY). The system verifies its OWN
// configured logs backend instead of anyone hand-querying prod: trace-id
// field mapping verdict (keyword ✓ / text ⚠ / absent — a text mapping
// silently kills the trace→log pivot's term clauses) + "% of logs with
// trace context" over the last 24h, overall and top-50 per service.
// Powers the /admin/elastic "Trace context" card + the /admin/stats
// snapshot line.
//
// Admin-only (cluster-mapping metadata, same posture as the elastic
// diagnostics above). 5m cache — the ES side is one field_caps + one
// size:0 aggregation over 24h of dailies; the CH side two bounded countIf
// scans. The key carries the backend name so a Settings-driven backend
// swap (Switchable) gets a fresh report instead of the other backend's
// cached one; backend failures come back as a typed
// {available:false, reason} report, never a raw 5xx (logstore contract).
func (s *Server) adminLogstoreTraceContext(w http.ResponseWriter, r *http.Request) {
	backend := s.logs.Backend()
	key := "admin-logstore-trace-context:" + backend
	s.serveCached(w, r, key, 5*time.Minute, func(ctx context.Context) (any, error) {
		rep := &logstore.TraceContextReport{
			Available: false,
			Reason:    "trace-context diagnostics not supported on this backend",
		}
		if d, ok := logstore.Unwrap(s.logs).(logstore.TraceContextDiagnoser); ok {
			got, err := d.TraceContextDiagnostics(ctx)
			if err != nil {
				rep = &logstore.TraceContextReport{Available: false, Reason: err.Error()}
			} else if got != nil {
				rep = got
			}
		}
		if rep.Fields == nil {
			rep.Fields = []logstore.TraceContextField{}
		}
		if rep.Services == nil {
			rep.Services = []logstore.TraceContextServiceCoverage{}
		}
		return map[string]any{"backend": backend, "report": rep}, nil
	})
}

func (s *Server) getLogsFieldValues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	field := strings.TrimSpace(q.Get("field"))
	prefix := q.Get("q")
	limit := parseInt(q.Get("limit"), 20)
	if field == "" {
		writeJSON(w, map[string]any{"values": []string{}})
		return
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	key := fmt.Sprintf("logs-field-values:%s:%s:%d", field, prefix, limit)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		vals, err := s.logs.FieldValues(ctx, field, prefix, limit)
		if err != nil {
			// Surfaced to the pod log (the UI swallows it below, so this is the
			// only place an ES authz/_terms_enum error on field-values shows up).
			log.Printf("[logs] field-values failed (backend=%s, field=%q): %v", s.logs.Backend(), field, err)
			// Don't 500 on bad field names — the autocomplete UI
			// already tolerates an empty list, and a typed
			// "lkjasdf:" shouldn't surface as a red banner.
			return map[string]any{"values": []string{}}, nil
		}
		if vals == nil {
			vals = []string{}
		}
		return map[string]any{"values": vals}, nil
	})
}

// getLogsTemplates surfaces the Drain-extracted log templates
// persisted by the templater puller. Sortable by first_seen
// (newest templates land first → "what just started firing?"),
// last_seen (recent activity) or total_count (busiest).
// 30s server cache fronts the read so the panel refresh is
// cheap.
func (s *Server) getLogsTemplates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sortBy := strings.TrimSpace(q.Get("sort"))
	if sortBy == "" {
		sortBy = "first_seen"
	}
	sinceDur := parseDuration(q.Get("since"), 24*time.Hour)
	limit := parseInt(q.Get("limit"), 50)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	since := time.Now().Add(-sinceDur).UnixNano()

	key := fmt.Sprintf("logs-templates:sort=%s:since=%s:limit=%d",
		sortBy, sinceDur, limit)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		rows, err := s.store.ListLogTemplates(ctx, chstore.ListLogTemplatesFilter{
			SinceNs: since,
			SortBy:  sortBy,
			Limit:   limit,
		})
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []chstore.LogTemplate{}
		}
		return rows, nil
	})
}

// getLogsContext returns ±N logs around a pivot timestamp, scoped
// to the same service. Datadog Context tab equivalent — operator
// clicks a log line, sees what was emitted just before and just
// after, without leaving /logs to rebuild a time-bounded filter
// by hand. Two parallel logstore.Search calls; one for the
// before window (DESC limit n) and one for the after window
// (ASC limit n). 30-minute symmetric window — wide enough to
// catch a slow incident, narrow enough that the search stays
// sub-second on a busy index.
func (s *Server) getLogsContext(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ts, _ := strconv.ParseInt(q.Get("ts"), 10, 64)
	if ts <= 0 {
		http.Error(w, "ts (unix ns) required", http.StatusBadRequest)
		return
	}
	service := q.Get("service")
	n := parseInt(q.Get("n"), 50)
	if n > 200 {
		n = 200
	}
	if n < 1 {
		n = 50
	}
	pivot := time.Unix(0, ts)
	// 30 minute symmetric window. Each Search returns the N logs
	// adjacent to the pivot in its half-window — at ingest pressure
	// both halves rarely hit the cap, but the hard ceiling keeps the
	// round-trip bounded.
	//
	// The "before" window is newest-first (default DESC): LIMIT n over
	// [pivot-30m, pivot] gives the n rows closest to (just before) the
	// pivot. The "after" window MUST be oldest-first (Ascending,
	// v0.7.83): a DESC LIMIT n over [pivot, pivot+30m] would return the
	// n NEWEST rows near pivot+30m — i.e. ~30 min after the pivot, with
	// every line emitted immediately after it missing. Ascending makes
	// LIMIT n return the n rows immediately AFTER the pivot. The client
	// (LogContextModal) re-sorts the union by timestamp for display.
	// v0.8.400 — env narrows both halves: the operator's scenario is the
	// SAME service name deployed in 3 environments, so context around a
	// pivot must not interleave the other envs' lines.
	env := strings.TrimSpace(q.Get("env"))
	beforeF := logstore.Filter{
		Service: service,
		Env:     env,
		From:    pivot.Add(-30 * time.Minute),
		To:      pivot,
		Limit:   n,
	}
	afterF := logstore.Filter{
		Service:   service,
		Env:       env,
		From:      pivot,
		To:        pivot.Add(30 * time.Minute),
		Limit:     n,
		Ascending: true,
	}
	key := fmt.Sprintf("logs-context:ts=%d:svc=%s:env=%s:n=%d", ts, service, env, n)
	s.serveCached(w, r, key, 15*time.Second, func(ctx context.Context) (any, error) {
		// v0.8.350 (HA 🟡6) — a slow/unreachable backend degrades the
		// modal to 200 {degraded:true} + empty halves instead of a 5xx.
		// Each half-window gets its own bounded budget (SearchWithTimeout
		// maps deadline/dial/transport failures to ErrBackendSlow; a
		// genuine query error still fails loudly).
		degradedPayload := func(cause error) map[string]any {
			log.Printf("[logs] context degraded (backend=%s, svc=%q): %v",
				s.logs.Backend(), service, cause)
			return map[string]any{
				"pivotTs":  ts,
				"service":  service,
				"before":   []*logstore.LogRecord{},
				"after":    []*logstore.LogRecord{},
				"degraded": true,
				"reason":   "log backend slow/unreachable",
			}
		}
		beforePage, err := logstore.SearchWithTimeout(ctx, s.logs, beforeF, logsContextBudget)
		if err != nil {
			if errors.Is(err, logstore.ErrBackendSlow) {
				return degradedPayload(err), nil
			}
			return nil, err
		}
		afterPage, err := logstore.SearchWithTimeout(ctx, s.logs, afterF, logsContextBudget)
		if err != nil {
			if errors.Is(err, logstore.ErrBackendSlow) {
				return degradedPayload(err), nil
			}
			return nil, err
		}
		before := beforePage.Logs
		after := afterPage.Logs
		if before == nil {
			before = []*logstore.LogRecord{}
		}
		if after == nil {
			after = []*logstore.LogRecord{}
		}
		return map[string]any{
			"pivotTs": ts,
			"service": service,
			"before":  before,
			"after":   after,
		}, nil
	})
}

// getLogsTimeseries powers the Logs source on the Data Explorer
// page. Returns one time-bucketed series per groupBy value (e.g.
// per service / per severity), routed through whichever logstore
// backend is wired in (CH or external ES). 30s cache absorbs
// chart-rerender bursts as the operator tweaks dimensions.
// getLogsFieldStats — top-5 values of one field for the /logs
// fields-panel accordion (v0.8.255, Discover revamp 2/7). ES-usage
// contract (operator: "elastic api kullanımını çok artırma"):
// fetch fires ONLY on accordion expand (never polled, never
// prefetched for the whole field list), 60s cache absorbs repeat
// expands, and the backend runs a single bounded terms agg
// (request_cache, soft timeout, size:0) — or a capped GROUP BY on
// the CH backend. Reuses s.logs — the same ES client + API-key auth
// as every other logs query; no new connection/credential config.
func (s *Server) getLogsFieldStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	field := strings.TrimSpace(q.Get("field"))
	if field == "" {
		http.Error(w, "field required", http.StatusBadRequest)
		return
	}
	sev, _ := strconv.Atoi(q.Get("severity"))
	f := logstore.Filter{
		Service:     q.Get("service"),
		Cluster:     q.Get("cluster"),
		Env:         strings.TrimSpace(q.Get("env")), // v0.8.400 — env-separation Phase 4
		Search:      q.Get("search"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		SeverityMin: uint8(sev),
		TraceID:     q.Get("traceId"),
		SpanID:      q.Get("spanId"),
	}
	key := logsFieldStatsKey(field, f, q.Get("from"), q.Get("to"))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		res, err := s.logs.FieldStats(tctx, f, field, 5)
		if err != nil {
			// v0.8.350 (HA 🟡6) — slow/unreachable backend degrades the
			// accordion to 200 {degraded:true} + empty values instead of
			// a 5xx (LogFieldStats carries degraded?/reason? in types.ts).
			if mapped := logstore.MapBackendSlow(err, tctx, ctx); errors.Is(mapped, logstore.ErrBackendSlow) {
				log.Printf("[logs] fieldstats degraded (backend=%s, field=%q): %v",
					s.logs.Backend(), field, err)
				return map[string]any{
					"field":    field,
					"total":    0,
					"values":   []logstore.FieldValueCount{},
					"degraded": true,
					"reason":   "log backend slow/unreachable",
				}, nil
			}
			return nil, err
		}
		return res, nil
	})
}

func (s *Server) getLogsTimeseries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	f := logstore.Filter{
		Service:     q.Get("service"),
		Env:         strings.TrimSpace(q.Get("env")), // v0.8.400 — env-separation Phase 4
		Search:      q.Get("search"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		SeverityMin: uint8(sev),
		TraceID:     q.Get("traceId"),
	}
	bucketSec := parseInt(q.Get("bucketSec"), 30)
	// v0.5.259 — was: floor 5s. Lowered to 1s so the operator can
	// see per-second log surges on a short window. ES + CH both
	// handle 1s date_histogram fine; the cost is bucket count
	// (max 86400 if operator picks 1s on a 24h window — uncached
	// but bounded).
	if bucketSec < 1 {
		bucketSec = 1
	}
	if bucketSec > 86400 {
		bucketSec = 86400
	}
	groupBy := strings.TrimSpace(q.Get("groupBy"))
	key := logsTimeseriesKey(f, q.Get("from"), q.Get("to"), bucketSec, groupBy)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// v0.8.3 — bound the Go goroutine on BOTH backends. CH already
		// self-caps at 30s (max_execution_time); this gives the ES path
		// the same ceiling so the api pod releases the goroutine +
		// response buffers even if the cluster keeps churning (the ES
		// soft-timeout in the query body is the tighter inner bound).
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		series, err := s.logs.Histogram(tctx, f, bucketSec, groupBy)
		if err != nil {
			// v0.8.350 (HA 🟡6) — slow/unreachable backend → 200 with an
			// EMPTY series list instead of a 5xx. This endpoint's wire
			// shape is a bare ARRAY (the /explore + /logs histogram
			// clients map over it), so unlike the object-shaped logs
			// paths there is no room for a degraded flag without a
			// breaking shape change — the chart renders empty and the
			// paired /logs search response carries the degraded banner.
			if mapped := logstore.MapBackendSlow(err, tctx, ctx); errors.Is(mapped, logstore.ErrBackendSlow) {
				log.Printf("[logs] timeseries degraded (backend=%s, svc=%q): %v",
					s.logs.Backend(), f.Service, err)
				return []logstore.LogSeries{}, nil
			}
			return nil, err
		}
		return series, nil
	})
}
