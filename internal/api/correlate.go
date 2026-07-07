package api

import (
	"context"
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
// derive the other two. The join keys, in priority order, are exactly the ones
// the codebase already trusts:
//   1. trace_id — the only cross-signal key OTel carries everywhere; exact join
//      when present (no time fuzz).
//   2. exemplar — a REAL representative trace for a metric anchor (the actual
//      slow/error trace that produced the p99/error spike), resolved off the
//      spanmetrics rollup's argMax(If) exemplar states — the same trace_id
//      Explore's ◆ glyphs use. Exact-enough to pivot INTO, just not the literal
//      data point the operator clicked. Applies to latency + error metric kinds.
//   3. service.name + time-window — the genuinely fuzzy fallback: no trace_id
//      and no representative span (a pure count/throughput metric, where no
//      single span "is" the value), so the other lenses are matched by
//      service + [from,to] only.
//
// HONESTY: the METRIC anchor is no longer deferred. For latency + error a
// metric→trace pivot lands on a REAL representative exemplar (slow / error
// trace from the rollup, falling back to the slowest/erroring raw span) — that
// IS the standard Datadog/Honeycomb exemplar, not a fabrication. Only a
// throughput/count metric (no representative span) keeps the honest weaker
// "service+window" join. Trace + Log anchors are unchanged (exact / near-real
// trace_id joins).

// CorrelationKind is the pivot anchor's signal shape.
type CorrelationKind string

const (
	CorrelateTrace  CorrelationKind = "trace"
	CorrelateLog    CorrelationKind = "log"
	CorrelateMetric CorrelationKind = "metric"
)

// Join-key labels the bundle reports back to the drawer's anchor chip. These
// are the exact strings the frontend special-cases, so keep them in sync with
// CorrelationContextDrawer.tsx.
const (
	// joinTraceID — exact cross-signal join on trace_id (no time fuzz).
	joinTraceID = "trace_id"
	// joinExemplar — a REAL representative trace from the spanmetrics rollup
	// (or the slowest/erroring raw span): exact-enough to pivot INTO. The
	// metric→trace pivot for latency + error anchors.
	joinExemplar = "exemplar"
	// joinServiceWindow — the genuinely-fuzzy fallback: no trace_id and no
	// representative span (throughput/count metric), so the lenses are matched
	// by service + time-window only.
	joinServiceWindow = "service+window"
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
	//   "exemplar"       — a real representative trace from the spanmetrics
	//                      rollup (or the slowest/erroring raw span): the
	//                      metric→trace pivot for latency/error anchors. Not the
	//                      literal data point, but exact-enough to pivot into.
	//   "service+window" — the genuinely-fuzzy fallback (throughput/count
	//                      metric, or no exemplar resolved): the operator must
	//                      see this.
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
	Exemplar *chstore.Exemplar           `json:"exemplar,omitempty"` // metric anchor: a REAL representative trace to pivot INTO (slow/error rollup exemplar, raw-span fallback)
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

	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		out := CorrelationContext{
			Anchor: CorrelationAnchor{
				Kind:    kind,
				TraceID: traceID,
				Service: service,
				TsNs:    tsNs,
				FromNs:  from.UnixNano(),
				ToNs:    to.UnixNano(),
				// trace_id present ⇒ exact join. Else, for a METRIC anchor whose
				// kind has a representative span (latency/error), the resolved
				// exemplar is a real trace ⇒ "exemplar". Otherwise the genuinely
				// fuzzy "service+window". All surfaced via the drawer's join chip.
				JoinKey: joinKeyFor(traceID, kind, metricKind),
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
				spans, err := s.store.GetTrace(ctx, traceID)
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
			page, err := s.logs.Search(ctx, lf)
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

		// (d) Exemplar — ONLY for the metric anchor: a REAL representative trace
		// for (service, window, kind) the operator can pivot INTO. PREFER the
		// spanmetrics rollup's argMax(If) exemplar (FindExemplarRollup — the same
		// trace_id Explore's ◆ glyphs use, one MV read, the most precise + cheapest
		// source). Fall back to FindExemplar (raw spans, still real: the
		// slowest/erroring span in the bucket) when the rollup has no exemplar for
		// that window (cutover/TTL/no-error). Both yield a real chstore.Exemplar;
		// soft-fail to nil either way. For latency/error the anchor JoinKey is
		// "exemplar"; for throughput it's "service+window" (no representative span).
		if kind == CorrelateMetric && service != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := chstore.ExemplarReq{
					Service: service, From: from, To: to,
					Kind: correlateExemplarKind(metricKind),
				}
				ex, err := s.store.FindExemplarRollup(ctx, req)
				if err != nil || ex == nil {
					// Rollup miss (pre-cutover / TTL'd / no error in window) →
					// raw-spans fallback. Still a real representative trace.
					ex, err = s.store.FindExemplar(ctx, req)
				}
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
			if series := s.redSeries(ctx, svc, mFrom, mTo); len(series) > 0 {
				out.Metrics = series
			}
		}

		return out, nil
	})
}

// joinKeyFor reports the strongest join key the bundle can use, which the
// drawer renders as a chip so the operator always sees which join they're
// trusting:
//   - a trace_id ⇒ exact "trace_id" join (any anchor kind);
//   - a METRIC anchor whose metricKind has a representative span
//     (latency/error) ⇒ "exemplar" — a real representative trace to pivot into;
//   - otherwise (throughput/count metric, or a non-metric anchor with no
//     trace_id) ⇒ the genuinely-fuzzy "service+window".
func joinKeyFor(traceID string, kind CorrelationKind, metricKind string) string {
	if traceID != "" {
		return joinTraceID
	}
	if kind == CorrelateMetric && metricHasExemplar(metricKind) {
		return joinExemplar
	}
	return joinServiceWindow
}

// correlateExemplarKind maps the drawer's metricKind enum (error|latency|
// throughput) to the exemplar trace flavour:
//   - latency  → slow  (the slowest representative trace)
//   - error    → error (the loudest erroring representative trace)
//   - throughput / anything else → any (no single representative span; the
//     "loudest in the window" stand-in, labelled service+window by joinKeyFor)
// Distinct from rootcause.go's exemplarKindForMetric, which classifies a
// problem's free-form metric NAME; here metricKind is already the clean enum.
func correlateExemplarKind(metricKind string) chstore.ExemplarKind {
	switch strings.ToLower(strings.TrimSpace(metricKind)) {
	case "error":
		return chstore.ExemplarError
	case "latency":
		return chstore.ExemplarSlow
	default:
		return chstore.ExemplarAny
	}
}

// metricHasExemplar reports whether a metricKind has a REAL representative span
// (latency/error do — the slow/error trace; throughput/count does not, since no
// single span "is" the value). Decides "exemplar" vs "service+window".
func metricHasExemplar(metricKind string) bool {
	switch correlateExemplarKind(metricKind) {
	case chstore.ExemplarSlow, chstore.ExemplarError:
		return true
	default:
		return false
	}
}

// redSeries fetches the anchor service's three RED series (rate / error_rate /
// p99) over [from,to]. Thin delegate: the composition itself lives in
// chstore.ServiceREDSeries (lifted v0.8.333 so the MCP get_metrics_for_span
// tool shares the exact aggregation choices — mcptools can't import api).
//
// v0.8.330 — takes ctx instead of *http.Request so it's shareable with the
// /api/spans/window-metrics pivot (pivot.go) AND correct under the v0.8.319
// serveCached ctx discipline: closures must query on the ctx serveCached hands
// them (the SWR background refresh runs after the request returns, when
// r.Context() is already cancelled).
func (s *Server) redSeries(ctx context.Context, service string, from, to time.Time) []chstore.SpanMetricSeries {
	return s.store.ServiceREDSeries(ctx, service, from, to)
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
