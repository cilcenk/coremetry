package api

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// Correlated Signals (task #6) — one pivot surface that, given any single
// signal (trace / log / metric), assembles the correlated OTHER two without a
// page change. This is SYNTHESIS, not new capability: every join already has an
// endpoint; this orchestrates them into one cached bundle the way rootcause.go
// orchestrates a Problem's sub-reads. Read-only, open (writes no state).
//
// The pivot anchor is one of three shapes, each carrying enough context to
// derive the other two. The join keys, in priority order, are exactly the three
// the codebase already trusts:
//   1. trace_id — the only cross-signal key OTel carries everywhere; exact join
//      when present (no time fuzz).
//   2. service.name — the fallback when no trace_id (a raw metric data point has
//      no trace_id on the wire today).
//   3. time-window [from,to] — bounds every service-keyed derivation.
//
// HONESTY (task #6, spec Risk §1): the METRIC anchor is intentionally DEFERRED
// in v1. A raw OTLP metric point carries no trace_id, so its "derived trace" is
// fuzzy (service+window), not exact — shipping it claiming an exact metric→trace
// pivot erodes the differentiator. The handler accepts kind=metric only to
// return the RED + logs lenses (clearly service+window-joined); it does NOT
// fabricate an exact exemplar pivot. Trace + Log anchors (real / near-real
// trace_id joins) are the shippable v1.

// CorrelationKind is the pivot anchor's signal shape.
type CorrelationKind string

const (
	CorrelateTrace  CorrelationKind = "trace"
	CorrelateLog    CorrelationKind = "log"
	CorrelateMetric CorrelationKind = "metric"
)

// CorrelationAnchor is what the operator pivoted FROM, echoed back so the
// drawer's anchor header + join-key chip can render which join is being trusted.
type CorrelationAnchor struct {
	Kind    CorrelationKind `json:"kind"`
	TraceID string          `json:"traceId,omitempty"`
	Service string          `json:"service,omitempty"`
	TsNs    int64           `json:"tsNs,omitempty"`
	FromNs  int64           `json:"fromNs"`
	ToNs    int64           `json:"toNs"`
	// JoinKey is the strongest join the bundle actually used:
	//   "trace_id"       — exact cross-signal join (no time fuzz)
	//   "service+window" — fuzzy join (the operator must see this)
	JoinKey string `json:"joinKey"`
}

// CorrelationTrace is the condensed trace lens — enough for the service-timeline
// mini-waterfall + a header, without re-loading the full /trace page. Derived
// from the same GetTrace spans the /api/traces/{id} endpoint returns.
type CorrelationTrace struct {
	TraceID     string              `json:"traceId"`
	RootName    string              `json:"rootName"`
	Service     string              `json:"service"`
	DurationMs  float64             `json:"durationMs"`
	SpanCount   int                 `json:"spanCount"`
	Services    []string            `json:"services"`
	ErrSpans    int                 `json:"errSpans"`
	StartTimeNs int64               `json:"startTimeNs"`
	EndTimeNs   int64               `json:"endTimeNs"`
	// Spans is the raw span list (capped) so the drawer's extracted
	// ServiceTimeline sub-component renders the SAME per-service density bars
	// TracePeekDrawer does — no second derivation. Capped to keep the bundle
	// bounded for very large traces.
	Spans []chstore.SpanRow `json:"spans"`
}

// CorrelationContext is the assembled pivot bundle. Every lens is best-effort:
// a lens with no data soft-fails to nil/empty, exactly like rootcause.go — a
// partial bundle still helps the operator.
type CorrelationContext struct {
	Anchor   CorrelationAnchor           `json:"anchor"`
	Trace    *CorrelationTrace           `json:"trace,omitempty"`
	Logs     []*logstore.LogRecord       `json:"logs"`              // always present (possibly empty)
	Metrics  []chstore.SpanMetricSeries  `json:"metrics"`           // anchor service RED series (possibly empty)
	Exemplar *chstore.Exemplar           `json:"exemplar,omitempty"` // metric anchor: representative trace to pivot INTO (fuzzy)
}

// correlateSpanCap bounds the trace lens span list so a pathological 10k-span
// trace doesn't blow the bundle up. The drawer's mini-waterfall reads density,
// not every span — the same reasoning behind TracePeekDrawer's 500-log cap.
const correlateSpanCap = 2000

// correlateLogCap bounds the logs lens. Matches TracePeekDrawer's 500.
const correlateLogCap = 500

// correlateKeyDigest is the cache-key fragment for the correlation bundle.
// PINNED by cache_key_test.go (the v0.5.187 anti-pattern guard): the bundle
// hashes ALL SIX inputs (kind, traceId, service, tsBucket, fromBucket,
// toBucket, metricKind) via SORTED + FNV — NOT length, NOT concatenation
// order-sensitivity. Two anchors that differ in any one input must produce
// distinct digests, or they cross-poison each other's cached bundle.
//
// Time inputs are bucketed to the minute by the CALLER (so concurrent triage
// clicks within the same minute share the trip, the rootcause.go minute-bucket
// trick); the digest itself is permutation-invariant over its fragment set so
// the order we list inputs here can never change the key.
func correlateKeyDigest(parts ...string) string {
	// Sort so the digest is a pure function of the SET of (label=value)
	// fragments — independent of the argument order at the call site. Each
	// fragment is "label=value" so an empty value still occupies a distinct
	// slot (a missing service must not collide with a missing traceId).
	cp := append([]string(nil), parts...)
	sort.Strings(cp)
	h := fnv.New64a()
	for _, p := range cp {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// getCorrelationContext assembles the cross-signal pivot bundle for one anchor.
// Read-only, open (same posture as /api/problems, /api/correlations — it writes
// no state, so the CLAUDE.md feature-checklist auth/audit gates 3–4 don't
// apply). Fans out to the EXISTING reads (GetTrace, logs.Search, QuerySpanMetric,
// FindExemplar) in PARALLEL; each sub-read SOFT-FAILS to a nil/empty lens rather
// than failing the whole bundle. Cached 30s keyed on all inputs (sorted+FNV,
// time-bucketed to the minute).
//
// Query:
//   ?kind=trace|log|metric
//   &traceId=<hex>          (kind=trace required; kind=log optional)
//   &service=<name>         (kind=metric required; kind=log/trace derived)
//   &tsNs=<unix-ns>         (kind=log|metric — the pivot instant)
//   &from=<ns>&to=<ns>      (or range — derives [from,to])
//   &metricKind=error|latency|throughput  (kind=metric — picks exemplar kind)
func (s *Server) getCorrelationContext(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := CorrelationKind(strings.TrimSpace(q.Get("kind")))
	switch kind {
	case CorrelateTrace, CorrelateLog, CorrelateMetric:
		// ok
	default:
		http.Error(w, "kind must be trace|log|metric", http.StatusBadRequest)
		return
	}

	traceID := strings.TrimSpace(q.Get("traceId"))
	service := strings.TrimSpace(q.Get("service"))
	tsNs, _ := strconv.ParseInt(q.Get("tsNs"), 10, 64)
	metricKind := strings.TrimSpace(q.Get("metricKind"))

	// Per-kind required-input validation. A trace anchor needs a trace_id; a
	// metric anchor needs a service; a log anchor needs at least one of the
	// two (a trace_id gives the exact join, a service+ts the fuzzy one).
	switch kind {
	case CorrelateTrace:
		if traceID == "" {
			http.Error(w, "traceId required for kind=trace", http.StatusBadRequest)
			return
		}
	case CorrelateMetric:
		if service == "" {
			http.Error(w, "service required for kind=metric", http.StatusBadRequest)
			return
		}
	case CorrelateLog:
		if traceID == "" && service == "" {
			http.Error(w, "traceId or service required for kind=log", http.StatusBadRequest)
			return
		}
	}

	// ── Resolve the analysis window ───────────────────────────────────────────
	// Priority: explicit from/to → ±30m around the pivot ts (the getLogsContext
	// convention) → default last hour. The ±30m symmetric window matches the
	// log-context surface so the metric/log lenses read the same span of time
	// the operator already trusts for "what happened around this instant".
	// We read from/to off the raw query (parseFromTo defaults a missing pair to
	// now()..now(), which would zero the window — so we branch on presence).
	var from, to time.Time
	rawFrom := parseTime(q.Get("from"))
	rawTo := parseTime(q.Get("to"))
	switch {
	case !rawFrom.IsZero() && !rawTo.IsZero():
		from, to = rawFrom, rawTo
	case tsNs > 0:
		pivot := time.Unix(0, tsNs)
		from = pivot.Add(-30 * time.Minute)
		to = pivot.Add(30 * time.Minute)
	default:
		to = time.Now()
		from = to.Add(-time.Hour)
	}

	// ── Cache key: ALL inputs, sorted + FNV, time bucketed to the minute ──────
	tsBucket := int64(0)
	if tsNs > 0 {
		tsBucket = time.Unix(0, tsNs).Truncate(time.Minute).Unix()
	}
	key := "correlate:" + correlateKeyDigest(
		"kind="+string(kind),
		"trace="+traceID,
		"svc="+service,
		"ts="+strconv.FormatInt(tsBucket, 10),
		"from="+strconv.FormatInt(from.Truncate(time.Minute).Unix(), 10),
		"to="+strconv.FormatInt(to.Truncate(time.Minute).Unix(), 10),
		"mk="+metricKind,
	)

	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		out := CorrelationContext{
			Anchor: CorrelationAnchor{
				Kind:    kind,
				TraceID: traceID,
				Service: service,
				TsNs:    tsNs,
				FromNs:  from.UnixNano(),
				ToNs:    to.UnixNano(),
				// trace_id present ⇒ exact join; else the service+window fuzzy
				// join — surfaced to the operator via the drawer's join chip.
				JoinKey: joinKeyFor(traceID),
			},
			Logs:    []*logstore.LogRecord{},
			Metrics: []chstore.SpanMetricSeries{},
		}

		var wg sync.WaitGroup
		var mu sync.Mutex // guards out.Anchor.Service derivation (the only shared write)

		// (a) Trace lens — load the trace spans, condense to the timeline shape.
		// Only when we have a trace_id (the exact join). Derives the anchor
		// service from the root span when the caller didn't pass one (a log
		// anchor with a trace_id but no explicit service).
		if traceID != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				spans, err := s.store.GetTrace(r.Context(), traceID)
				if err != nil || len(spans) == 0 {
					return
				}
				ct := condenseTrace(traceID, spans)
				mu.Lock()
				out.Trace = ct
				if out.Anchor.Service == "" {
					out.Anchor.Service = ct.Service
				}
				mu.Unlock()
			}()
		}

		// (b) Logs lens — trace_id join when present (exact), else service+window
		// (fuzzy). logstore.Search routes to ES (term query on trace.id) or CH
		// (tokenbf_v1 skip index) — both indexed paths, verified billion-doc
		// safe. Bounded by correlateLogCap.
		//
		// CRITICAL (verified against logstore internals): for a trace_id join we
		// pass NO time window — exactly the TracePeekDrawer pattern
		// (api.logs({traceId, limit}) with no from/to). The ES backend
		// deliberately ignores the window when TraceID is set (elasticsearch.go:
		// "a trace link can be older than any default slice"), but the CH backend
		// AND-applies from/to with trace_id (repo.go GetLogs) — so passing our
		// default window would silently DROP an older trace's logs on CH installs.
		// Leaving From/To zero makes trace_id the sole filter on both backends.
		// The service+window fuzzy join keeps the window (it's the only bound).
		wg.Add(1)
		go func() {
			defer wg.Done()
			lf := logstore.Filter{Limit: correlateLogCap}
			if traceID != "" {
				lf.TraceID = traceID
			} else {
				lf.Service = service
				lf.From = from
				lf.To = to
			}
			page, err := s.logs.Search(r.Context(), lf)
			if err != nil || page == nil {
				return
			}
			mu.Lock()
			if page.Logs != nil {
				out.Logs = page.Logs
			}
			mu.Unlock()
		}()

		// (c) Metrics lens runs in the SECOND wave (below), not here: a trace
		// anchor's metric service AND its honest time window are both only known
		// after the trace lens (a) resolves — the RED series must cover the
		// period the TRACE occurred in, not the default window around now().

		// (d) Exemplar — ONLY for the metric anchor, and clearly fuzzy: the
		// representative bad trace for (service, window, kind) the operator can
		// pivot INTO. FindExemplar is the same lossy shortcut useExemplars
		// documents (service+window, not a true OTLP exemplar). We surface it,
		// but the anchor JoinKey already tells the UI this is service+window.
		if kind == CorrelateMetric && service != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ex, err := s.store.FindExemplar(r.Context(), chstore.ExemplarReq{
					Service: service, From: from, To: to,
					Kind: exemplarKindForMetric(metricKind),
				})
				if err == nil && ex != nil {
					mu.Lock()
					out.Exemplar = ex
					mu.Unlock()
				}
			}()
		}

		wg.Wait()

		// ── Metrics lens (second wave, sequential) ───────────────────────────
		// Runs after the trace lens so we know BOTH the service and the honest
		// window. The metric service is the anchor service (explicit, or the
		// trace's root service derived in (a)). The window: for a TRACE anchor,
		// the trace's own span window widened ±15m so the RED series brackets the
		// period the trace actually occurred in (the request window is anchored
		// on now() and would show the wrong slice for an older trace). For
		// metric/log anchors, the request window (±30m around the pivot, or the
		// explicit from/to) is already the right one.
		if svc := out.Anchor.Service; svc != "" {
			mFrom, mTo := from, to
			if out.Trace != nil {
				ts := time.Unix(0, out.Trace.StartTimeNs)
				te := time.Unix(0, out.Trace.EndTimeNs)
				mFrom = ts.Add(-15 * time.Minute)
				mTo = te.Add(15 * time.Minute)
				// Reflect the trace-derived window back onto the anchor so the
				// drawer's chart x-axis AND its "Open in Explore →" handoff land
				// on the SAME window the RED series was computed over (the trace's
				// period, not the now()-anchored request default).
				out.Anchor.FromNs = mFrom.UnixNano()
				out.Anchor.ToNs = mTo.UnixNano()
			}
			if series := s.redSeries(r, svc, mFrom, mTo); len(series) > 0 {
				out.Metrics = series
			}
		}

		return out, nil
	})
}

// joinKeyFor reports the strongest join key the bundle can use: an exact
// trace_id join, or the fuzzy service+window fallback. The drawer renders this
// as a chip so the operator always sees which join they're trusting.
func joinKeyFor(traceID string) string {
	if traceID != "" {
		return "trace_id"
	}
	return "service+window"
}

// redSeries fetches the anchor service's three RED series (rate / error_rate /
// p99) over [from,to] via the SAME QuerySpanMetric path the live chart + RED
// panel + DQL hit — one cache + one MV story. Each series is grouped by
// service.name so the MV fast-path (service_summary_5m, step ≥ 5m) applies and
// the result is one line per metric. Soft-fails per-query: a missing series
// just drops out of the bundle. The GroupKey[0] is overwritten with the metric
// label so the drawer's three lines are self-describing without a side channel.
func (s *Server) redSeries(r *http.Request, service string, from, to time.Time) []chstore.SpanMetricSeries {
	svcFilter := []chstore.FilterExpr{{Key: "service.name", Op: "=", Values: []string{service}}}
	out := make([]chstore.SpanMetricSeries, 0, 3)
	add := func(label, agg, field string) {
		f := chstore.SpanMetricFilter{
			Aggregation: agg, Field: field, Filters: svcFilter, From: from, To: to,
			GroupBy: []string{"service.name"},
		}
		rows, err := s.store.QuerySpanMetric(r.Context(), f)
		if err != nil || len(rows) == 0 {
			return
		}
		// One series per metric; relabel GroupKey so the UI legend reads
		// rate / error_rate / p99 instead of the service name three times.
		ser := rows[0]
		ser.GroupKey = []string{label}
		out = append(out, ser)
	}
	add("rate", "rate", "")
	add("error_rate", "error_rate", "")
	add("p99", "p99", "duration_ms")
	return out
}

// condenseTrace derives the trace-lens summary from the raw span list — the
// SAME logic TracePeekDrawer's useMemo does, but server-side so the drawer is
// one round-trip. Caps the span list at correlateSpanCap.
func condenseTrace(traceID string, spans []chstore.SpanRow) *CorrelationTrace {
	if len(spans) == 0 {
		return nil
	}
	// Root = the span with no parent, else the first by start time.
	root := spans[0]
	minStart := spans[0].StartTime
	maxEnd := spans[0].EndTime
	svcSet := map[string]struct{}{}
	errSpans := 0
	for _, sp := range spans {
		if sp.ParentSpanID == "" {
			root = sp
		}
		if sp.StartTime < minStart {
			minStart = sp.StartTime
		}
		if sp.EndTime > maxEnd {
			maxEnd = sp.EndTime
		}
		svcSet[sp.ServiceName] = struct{}{}
		if sp.StatusCode == "error" {
			errSpans++
		}
	}
	services := make([]string, 0, len(svcSet))
	for s := range svcSet {
		services = append(services, s)
	}
	sort.Strings(services)

	capped := spans
	if len(capped) > correlateSpanCap {
		capped = capped[:correlateSpanCap]
	}

	return &CorrelationTrace{
		TraceID:     traceID,
		RootName:    root.Name,
		Service:     root.ServiceName,
		DurationMs:  float64(maxEnd-minStart) / 1e6,
		SpanCount:   len(spans),
		Services:    services,
		ErrSpans:    errSpans,
		StartTimeNs: minStart,
		EndTimeNs:   maxEnd,
		Spans:       capped,
	}
}
