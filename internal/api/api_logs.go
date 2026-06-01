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
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/templater"
)

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	f := logstore.Filter{
		Service:     q.Get("service"),
		Cluster:     q.Get("cluster"),
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
	page, err := s.logs.Search(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{
		"total":      page.Total,
		"logs":       page.Logs,
		"nextCursor": page.NextCursor,
	})
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
	s.serveCached(w, r, "logs-fields", 60*time.Second, func() (any, error) {
		f, ok := s.logs.(fielder)
		if !ok {
			return map[string]any{"fields": []string{}, "backend": s.logs.Backend()}, nil
		}
		fields, err := f.ListFields(r.Context())
		if err != nil {
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
// runLogsEQL executes an Elastic Event Query Language sequence
// search against the configured ES logs backend. Editor-gated —
// EQL can return arbitrary field shapes from the index and we
// want admin/editor accountability via audit. CH backend
// surfaces the "not supported" error from CHStore.EQLSearch;
// the frontend hides the panel on non-ES backends. v0.5.468.
//
// Not cached — EQL queries are operator-driven ad-hoc; caching
// per-keystroke iterations would just churn Redis without
// helping anyone.
func (s *Server) runLogsEQL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
		From  int64  `json:"fromMs"`
		To    int64  `json:"toMs"`
		Size  int    `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	q := logstore.EQLQuery{
		Query: body.Query,
		Size:  body.Size,
	}
	if body.From > 0 {
		q.From = time.UnixMilli(body.From)
	}
	if body.To > 0 {
		q.To = time.UnixMilli(body.To)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	seqs, err := s.logs.EQLSearch(ctx, q)
	if err != nil {
		writeJSON(w, map[string]any{
			"sequences": []logstore.EQLSequence{},
			"error":     err.Error(),
		})
		return
	}
	if seqs == nil {
		seqs = []logstore.EQLSequence{}
	}
	s.audit(r, "logs.eql_run", "logs", "",
		fmt.Sprintf(`{"len":%d,"size":%d}`, len(body.Query), body.Size))
	writeJSON(w, map[string]any{
		"sequences": seqs,
		"backend":   s.logs.Backend(),
	})
}

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
	s.serveCached(w, r, "admin-elastic-indices", 30*time.Second, func() (any, error) {
		idx, err := s.logs.Indices(r.Context())
		if err != nil {
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
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		vals, err := s.logs.FieldValues(r.Context(), field, prefix, limit)
		if err != nil {
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

// getLogsSignificantPatterns runs the ES `significant_text`
// aggregation over (curStart, now) using the (baseStart, curStart)
// window as the baseline — surfaces tokens that just got rare-
// vs-usual. CH backend returns an empty list (no native
// equivalent at billion-row scale). 60s server cache fronts the
// expensive agg so a /logs reload hits Redis, not ES.
func (s *Server) getLogsSignificantPatterns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	curWindow := parseDuration(q.Get("window"), 15*time.Minute)
	if curWindow > time.Hour {
		curWindow = time.Hour
	}
	baseline := parseDuration(q.Get("baseline"), 24*time.Hour)
	if baseline > 24*time.Hour {
		baseline = 24 * time.Hour
	}
	if baseline <= curWindow {
		// Baseline must outweigh the cur window or the rare-vs-
		// baseline math is degenerate. Floor at 4x the cur
		// window so a 15min cur sees at least an hour of
		// background.
		baseline = 4 * curWindow
	}
	topN := parseInt(q.Get("topN"), 25)
	if topN <= 0 || topN > 100 {
		topN = 25
	}
	now := time.Now()
	curStart := now.Add(-curWindow)
	baseStart := curStart.Add(-baseline)

	key := fmt.Sprintf("logs-significant:cur=%s:bg=%s:topN=%d",
		curWindow, baseline, topN)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		// v0.5.390 — handler-level deadline. The ES backend now
		// soft-timeouts at 10s on its own (body timeout param),
		// so this 15s bound is defence in depth — covers the CH
		// backend (no native timeout knob) and any network
		// stall on the ES coordinator round-trip. Without it,
		// a wedged ES would keep the request open until the
		// frontend's 60s fetch budget cancels it, surfacing as
		// "context deadline exceeded" from the operator's view.
		// v0.5.424 — env-tunable handler deadline. Default 15s
		// works for most installs; billion-doc production can
		// bump to 25-45s. Should be < the frontend's 60s fetch
		// budget so the browser doesn't time out first.
		hd := 15 * time.Second
		if v := strings.TrimSpace(os.Getenv("COREMETRY_LOGS_PATTERNS_DEADLINE")); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				hd = d
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), hd)
		defer cancel()
		hits, err := s.logs.SignificantPatterns(ctx, curStart, baseStart, now, topN)
		// Graceful timeout: serve empty patterns + a hint flag
		// so the panel can render "still computing" rather than
		// the red error state. The 60s server cache means the
		// next poll lands on a fresh attempt, not the same
		// stuck call.
		if err != nil && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded")) {
			return map[string]any{
				"backend":  s.logs.Backend(),
				"window":   curWindow.String(),
				"baseline": baseline.String(),
				"patterns": []logstore.SignificantPattern{},
				"timedOut": true,
			}, nil
		}
		if err != nil {
			return nil, err
		}
		if hits == nil {
			hits = []logstore.SignificantPattern{}
		}
		// v0.5.336 — drop opaque per-request ids from the live
		// patterns panel. JWT fragments and long base64 session
		// ids score statistically high (they appear in many
		// docs that share a request boundary) but tell the
		// operator nothing readable. Filter once at the API
		// layer so both ES + CH backends benefit.
		filtered := hits[:0]
		for _, h := range hits {
			if templater.LooksLikeOpaqueID(h.Token) {
				continue
			}
			filtered = append(filtered, h)
		}
		hits = filtered
		return map[string]any{
			"backend":  s.logs.Backend(),
			"window":   curWindow.String(),
			"baseline": baseline.String(),
			"patterns": hits,
		}, nil
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
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		rows, err := s.store.ListLogTemplates(r.Context(), chstore.ListLogTemplatesFilter{
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

// getLogsSimilarTraces bucket-counts trace IDs whose logs are
// pattern-similar to a seed text (more_like_this on the body
// field). POST body shape: {"text":"...","limit":50}. Returns
// {traces:[{traceId,count}]} sorted descending by count.
// Read-only against Elasticsearch; ClickHouse backend gets a
// clean 400 (no similar-text support).
func (s *Server) getLogsSimilarTraces(w http.ResponseWriter, r *http.Request) {
	type similarRunner interface {
		SimilarTraces(ctx context.Context, seed string, limit int) ([]logstore.SimilarTrace, error)
	}
	runner, ok := s.logs.(similarRunner)
	if !ok {
		http.Error(w, `{"error":"similar-logs lookup requires the Elasticsearch backend"}`, http.StatusBadRequest)
		return
	}
	var body struct {
		Text  string `json:"text"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	traces, err := runner.SimilarTraces(r.Context(), body.Text, body.Limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if traces == nil {
		traces = []logstore.SimilarTrace{}
	}
	writeJSON(w, map[string]any{"traces": traces})
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
	beforeF := logstore.Filter{
		Service: service,
		From:    pivot.Add(-30 * time.Minute),
		To:      pivot,
		Limit:   n,
	}
	afterF := logstore.Filter{
		Service:   service,
		From:      pivot,
		To:        pivot.Add(30 * time.Minute),
		Limit:     n,
		Ascending: true,
	}
	key := fmt.Sprintf("logs-context:ts=%d:svc=%s:n=%d", ts, service, n)
	s.serveCached(w, r, key, 15*time.Second, func() (any, error) {
		beforePage, err := s.logs.Search(r.Context(), beforeF)
		if err != nil {
			return nil, err
		}
		afterPage, err := s.logs.Search(r.Context(), afterF)
		if err != nil {
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
func (s *Server) getLogsTimeseries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	f := logstore.Filter{
		Service:     q.Get("service"),
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
	key := fmt.Sprintf("logs-ts:svc=%s:sev=%d:trace=%s:from=%s:to=%s:b=%d:g=%s:q=%s",
		f.Service, f.SeverityMin, f.TraceID, q.Get("from"), q.Get("to"),
		bucketSec, groupBy, q.Get("search"))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.logs.Histogram(r.Context(), f, bucketSec, groupBy)
	})
}
