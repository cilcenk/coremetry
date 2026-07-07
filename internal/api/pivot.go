package api

// pivot.go — the cross-signal pivot QUERY layer (v0.8.330, Phase 2 of the
// pivot plan in docs/pivot-audit.md). Three read-only endpoints over the
// Phase 1 ingest surfaces:
//
//   GET /api/exemplars             — OTLP exemplars for a fingerprint SET
//                                    (ExemplarsForSeries, the metric→trace
//                                    pivot) with a metric+service fallback
//                                    (ExemplarsForMetric) for fingerprint-less
//                                    callers.
//   GET /api/traces/{id}/links     — OTel span links, BOTH directions in one
//                                    payload (LinksFromTrace + LinksToTrace —
//                                    each a PK scan on its own table, see
//                                    chstore/span_links.go).
//   GET /api/spans/window-metrics  — service RED series around a span's
//                                    timestamp (±window). Thin composition
//                                    over the SAME redSeries helper the
//                                    correlate bundle uses — service_summary_5m
//                                    MV fast-path, never a new spans aggregate.
//
// All three are bare (viewer-visible — read-only pivots, same posture as
// /api/correlate/context per audit §4), all serveCached 30s with
// hash-ALL-inputs keys, time inputs minute-bucketed so concurrent triage
// clicks within the same minute share one upstream trip (the correlate.go
// pattern). Every CH read is a Phase 1 reader that is already bounded
// (LIMIT + max_execution_time + PK-scan by construction) — no new SQL here.
//
// registerPivotRoutes is the register-pattern the audit calls for: new
// surfaces arrive as new files with their own registrar, and api.go's Start
// block grows by exactly ONE line per family instead of one per route.

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// registerPivotRoutes mounts the Phase 2 pivot endpoints. Called ONCE from
// api.go's Start block (its single new line for v0.8.330).
func (s *Server) registerPivotRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/exemplars", s.getPivotExemplars)
	mux.HandleFunc("GET /api/traces/{id}/links", s.getTraceLinks)
	mux.HandleFunc("GET /api/spans/window-metrics", s.getSpanWindowMetrics)
}

// pivotMaxFingerprints caps the ?fingerprints= set. A chart plots at most a
// few dozen series; 100 keeps the IN (…) clause and the cache key bounded,
// and a larger request is a client bug worth rejecting loudly (400).
const pivotMaxFingerprints = 100

// pivotExemplar is the response item shape ({ts, value, traceId, spanId,
// attrs} per the Phase 2 contract) — chstore.OTLPExemplar minus the
// fingerprint (an input echo the client already holds) and with the
// timestamp under the chart-friendly `ts` name.
type pivotExemplar struct {
	Ts      int64             `json:"ts"` // unix ns
	Value   float64           `json:"value"`
	TraceID string            `json:"traceId"`
	SpanID  string            `json:"spanId"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// pivotFPDigest is the sorted-FNV digest of a fingerprint SET for cache keys.
// The v0.5.187 rule: NEVER a length-based stand-in — two distinct sets of the
// same size must never share a key, or they cross-poison each other's cached
// exemplars. Numeric sort makes the digest order-invariant; fixed-width 8-byte
// encoding needs no separator (no injection ambiguity between elements).
// Pinned by pivot_key_test.go.
func pivotFPDigest(fps []uint64) string {
	cp := append([]uint64(nil), fps...)
	slices.Sort(cp)
	h := fnv.New64a()
	var b [8]byte
	for _, fp := range cp {
		binary.BigEndian.PutUint64(b[:], fp)
		h.Write(b[:])
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// pivotMinuteBucket collapses a time to its minute for cache keys — the
// correlate.go / servicegraph.go convention: concurrent pivots within the
// same minute share one upstream trip, and the raw time still drives the
// actual query window. Pinned by pivot_key_test.go.
func pivotMinuteBucket(t time.Time) int64 {
	return t.Truncate(time.Minute).Unix()
}

// pivotExemplarKey builds the /api/exemplars cache key from ALL inputs:
// fingerprint set (digested), metric, service, limit, minute-bucketed window.
// Extracted pure so pivot_key_test.go pins the order-invariance /
// distinct-set / bucketing invariants against the REAL key construction.
func pivotExemplarKey(fps []uint64, metric, service string, limit int, from, to time.Time) string {
	return fmt.Sprintf("pivot-exemplars:fp=%s:m=%s:svc=%s:lim=%d:from=%d:to=%d",
		pivotFPDigest(fps), metric, service, limit,
		pivotMinuteBucket(from), pivotMinuteBucket(to))
}

// pivotWindowMetricsKey builds the /api/spans/window-metrics cache key —
// service + minute-bucketed anchor + clamped window seconds (all inputs).
func pivotWindowMetricsKey(service string, at time.Time, windowS int) string {
	return fmt.Sprintf("pivot-winmetrics:svc=%s:at=%d:w=%d",
		service, pivotMinuteBucket(at), windowS)
}

// parseFingerprints parses the comma-separated ?fingerprints= list as
// uint64s. Empty input → nil (the metric fallback path); any unparsable
// element or a set larger than pivotMaxFingerprints is a caller bug → error
// (the handler 400s rather than silently querying a truncated set).
func parseFingerprints(raw string) ([]uint64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]uint64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid fingerprint %q (want uint64)", p)
		}
		out = append(out, v)
	}
	if len(out) > pivotMaxFingerprints {
		return nil, fmt.Errorf("too many fingerprints (%d, max %d)", len(out), pivotMaxFingerprints)
	}
	return out, nil
}

// isHex32 reports whether s is exactly 32 lowercase-hex chars — a full trace
// id. The strictest check the existing trace path applies is repo.go's
// len==32 exact-match branch (shorter input degrades to a prefix search
// there); the span_links tables store hex.EncodeToString output (lowercase
// 32-hex), so this endpoint validates strictly and 400s garbage instead of
// running two PK lookups that can only miss. Callers lowercase first, so
// uppercase input is accepted at the edge.
func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// pivotWindowClamp resolves ?window_s= for /api/spans/window-metrics: the
// HALF-window in seconds around the anchor. Default 900 (±15m — the same
// bracket correlate.go widens a trace window by); clamped to [60, 3600] so a
// crafted URL can neither shrink the read below one summary bucket nor drag
// a multi-hour scan. Absent/garbage/zero/negative all mean the default.
// Pure + table-tested (pivot_key_test.go).
func pivotWindowClamp(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 900
	}
	if n < 60 {
		return 60
	}
	if n > 3600 {
		return 3600
	}
	return n
}

// getPivotExemplars serves GET /api/exemplars — the metric→trace pivot read.
//
//	?fingerprints=fp1,fp2&from=&to=&limit=   → ExemplarsForSeries (PK scan)
//	?metric=&service=&from=&to=&limit=       → ExemplarsForMetric (bounded
//	                                           granule scan) when fingerprints
//	                                           are absent
//
// Response: {items: [{ts, value, traceId, spanId, attrs}, …]}. Both readers
// clamp the limit server-side (default 100, max 1000 — chstore) and carry
// LIMIT + max_execution_time; window defaults to the last hour.
func (s *Server) getPivotExemplars(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fps, err := parseFingerprints(q.Get("fingerprints"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	metric := strings.TrimSpace(q.Get("metric"))
	service := strings.TrimSpace(q.Get("service"))
	if len(fps) == 0 && metric == "" {
		http.Error(w, "fingerprints or metric required", http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	limit := parseInt(q.Get("limit"), 0) // 0 = chstore's default (100)

	key := pivotExemplarKey(fps, metric, service, limit, from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		var (
			rows []chstore.OTLPExemplar
			err  error
		)
		if len(fps) > 0 {
			rows, err = s.store.ExemplarsForSeries(ctx, fps, from, to, limit)
		} else {
			rows, err = s.store.ExemplarsForMetric(ctx, metric, service, from, to, limit)
		}
		if err != nil {
			return nil, err
		}
		items := make([]pivotExemplar, 0, len(rows))
		for _, e := range rows {
			items = append(items, pivotExemplar{
				Ts: e.TimeUnixNs, Value: e.Value,
				TraceID: e.TraceID, SpanID: e.SpanID, Attrs: e.Attrs,
			})
		}
		return map[string]any{"items": items}, nil
	})
}

// getTraceLinks serves GET /api/traces/{id}/links — OTel span-link traversal
// for one trace, BOTH directions in one payload:
//
//	{outgoing: […], incoming: […]}
//
// outgoing = links this trace's spans declare (LinksFromTrace, span_links PK
// scan); incoming = backlinks pointing at it (LinksToTrace, span_links_reverse
// PK scan). Sequential on purpose: both are sub-ms primary-key point-lookups,
// so a parallel fan-out (the correlate.go WaitGroup shape) would buy nothing
// for two goroutines of overhead. Empty directions serialize as [] not null.
func (s *Server) getTraceLinks(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if !isHex32(id) {
		http.Error(w, "trace id must be 32 hex chars", http.StatusBadRequest)
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), 0) // 0 = chstore's default (100)

	key := fmt.Sprintf("pivot-tracelinks:id=%s:lim=%d", id, limit)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		outgoing, err := s.store.LinksFromTrace(ctx, id, limit)
		if err != nil {
			return nil, err
		}
		incoming, err := s.store.LinksToTrace(ctx, id, limit)
		if err != nil {
			return nil, err
		}
		if outgoing == nil {
			outgoing = []chstore.SpanLink{}
		}
		if incoming == nil {
			incoming = []chstore.SpanLink{}
		}
		return map[string]any{"outgoing": outgoing, "incoming": incoming}, nil
	})
}

// getSpanWindowMetrics serves GET /api/spans/window-metrics — the span→metric
// pivot: the service's RED series (rate / error_rate / p99) bracketing a
// span's timestamp.
//
//	?service=<name>&at=<unix_ns>&window_s=<half-window seconds>
//
// window_s clamps to [60, 3600], default 900 (±15m). Composition ONLY: the
// series come from the SAME redSeries helper the correlate bundle uses
// (QuerySpanMetric → service_summary_5m MV fast-path) — no new spans
// aggregate (MV-first rule). `at` defaults to now for a live pivot. The key
// minute-buckets `at` while the query uses the raw instant — same tradeoff
// as correlate.go's tsBucket (clicks within a minute share the trip; the
// window drift is « the 5m summary granularity the series are read at).
func (s *Server) getSpanWindowMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	if service == "" {
		http.Error(w, "service required", http.StatusBadRequest)
		return
	}
	at := time.Now()
	if atNs, _ := strconv.ParseInt(q.Get("at"), 10, 64); atNs > 0 {
		at = time.Unix(0, atNs)
	}
	windowS := pivotWindowClamp(q.Get("window_s"))
	from := at.Add(-time.Duration(windowS) * time.Second)
	to := at.Add(time.Duration(windowS) * time.Second)

	key := pivotWindowMetricsKey(service, at, windowS)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		series := s.redSeries(ctx, service, from, to)
		if series == nil {
			series = []chstore.SpanMetricSeries{}
		}
		return map[string]any{
			"service": service,
			"fromNs":  from.UnixNano(),
			"toNs":    to.UnixNano(),
			"metrics": series,
		}, nil
	})
}
