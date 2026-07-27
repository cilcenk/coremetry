package api

// endpoints_detail.go — v0.8.360 (Stage-2 slice E2, docs/pages-enhancement-
// audit.md §1): the /endpoints route-scoped detail drill-down. Two read-only
// endpoints over the chstore readers in internal/chstore/endpoints_detail.go:
//
//   GET /api/endpoints/detail — ONE payload for the drawer: 1-D latency
//                               distribution (heatmap core collapsed),
//                               status breakdown (class + per-code), top
//                               exceptions on the route, top failing traces
//                               (direct /trace pivot), slow/error exemplars
//                               (spanmetrics_1m argMax states). Sections are
//                               null-tolerant: one failed section renders as
//                               null, the rest survive — the drawer never
//                               blanks on a partial backend hiccup.
//   GET /api/endpoints/split  — top-10 values of ONE whitelisted attribute
//                               with RED each (chstore.EndpointSplit; the
//                               whitelist is the only path to SQL identity).
//
// Both are bare (viewer-visible — read-only drill-downs, same posture as the
// pivot endpoints), serveCached 30s with hash-ALL-inputs keys: (service,
// path) fold into one FNV digest with a NUL separator so field boundaries
// can't be forged by a crafted path (the v0.5.187 ambiguity class), windows
// are minute-bucketed (pivotMinuteBucket) so concurrent drawer opens within
// the same minute share one upstream trip.
//
// registerEndpointsDetailRoutes follows the pivot.go register pattern —
// api.go's Start block grows by exactly ONE line for this family.

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// registerEndpointsDetailRoutes mounts the E2 drill-down endpoints. Called
// ONCE from api.go's Start block (its single new line for v0.8.360).
func (s *Server) registerEndpointsDetailRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/endpoints/detail", s.getEndpointDetail)
	mux.HandleFunc("GET /api/endpoints/split", s.getEndpointSplit)
}

// endpointKeyDigest folds (service, path) into one FNV-64a digest with a
// NUL separator so two different tuples can never alias in the cache key —
// paths are operator-controlled free text, so "a" + "b|c" must not collide
// with "a|b" + "c" (the v0.5.187 rule applied to strings instead of sets).
// Pinned by endpoints_detail_key_test.go.
func endpointKeyDigest(service, path string) string {
	h := fnv.New64a()
	h.Write([]byte(service))
	h.Write([]byte{0})
	h.Write([]byte(path))
	return fmt.Sprintf("%x", h.Sum64())
}

// endpointDetailKey builds the /api/endpoints/detail cache key from ALL
// inputs: (service, path) digest, signature flag, minute-bucketed window.
// v0.9.306 — env/cluster are IN the key. They change the answer, so
// sharing an entry across scopes would serve one operator's uat drawer
// to another looking at prod — the v0.5.187 cross-poisoning class, and
// the very inconsistency this slice exists to remove.
func endpointDetailKey(service, path string, sig bool, from, to time.Time, env, cluster string) string {
	return fmt.Sprintf("endpoints-detail:sp=%s:sig=%v:env=%s:clu=%s:from=%d:to=%d",
		endpointKeyDigest(service, path), sig, env, cluster,
		pivotMinuteBucket(from), pivotMinuteBucket(to))
}

// endpointSplitKey builds the /api/endpoints/split cache key — the detail
// inputs plus the split dimension.
// v0.9.311 (brief N4) — "Where the time goes". Snapped to the same
// minute bucket as its siblings so a drawer re-open inside the TTL
// doesn't re-run TWO raw-spans passes.
func endpointDownstreamKey(service, path string, sig bool, from, to time.Time, env, cluster string) string {
	return fmt.Sprintf("endpoints-downstream:sp=%s:sig=%v:env=%s:clu=%s:from=%d:to=%d",
		endpointKeyDigest(service, path), sig, env, cluster,
		pivotMinuteBucket(from), pivotMinuteBucket(to))
}

func endpointSplitKey(service, path string, sig bool, by string, from, to time.Time, env, cluster string) string {
	return fmt.Sprintf("endpoints-split:sp=%s:sig=%v:by=%s:env=%s:clu=%s:from=%d:to=%d",
		endpointKeyDigest(service, path), sig, by, env, cluster,
		pivotMinuteBucket(from), pivotMinuteBucket(to))
}

// endpointHistogram is the drawer's 1-D latency distribution: log-scale
// bin upper bounds in ms (the heatmap grid's Y axis) with the counts
// summed across the window. SamplingRate < 1 means the counts are
// extrapolated from a deterministic trace-ID sample (>1h windows — the
// v0.5.238 heatmap guardrail; the UI shows the "sampled" tag).
type endpointHistogram struct {
	Bins         []float64 `json:"bins"`
	Counts       []uint64  `json:"counts"`
	SamplingRate float64   `json:"samplingRate,omitempty"`
}

// endpointExemplars carries the two representative trace_ids for the
// endpoint window. Empty fields mean "no exemplar" (all-healthy window
// for the error one; pre-rollup-cutover for both).
type endpointExemplars struct {
	SlowTraceID  string `json:"slowTraceId,omitempty"`
	ErrorTraceID string `json:"errorTraceId,omitempty"`
}

// endpointDetailPayload is the one-response drawer contract. Every
// section pointer is nil when its read failed — per-section tolerance,
// never a 500 for a partial miss.
type endpointDetailPayload struct {
	Service         string                          `json:"service"`
	Path            string                          `json:"path"`
	FromNs          int64                           `json:"fromNs"`
	ToNs            int64                           `json:"toNs"`
	Histogram       *endpointHistogram              `json:"histogram"`
	StatusBreakdown *chstore.EndpointStatus         `json:"statusBreakdown"`
	TopExceptions   []chstore.EndpointException     `json:"topExceptions"`
	FailingTraces   []chstore.EndpointFailingTrace  `json:"failingTraces"`
	Exemplars       *endpointExemplars              `json:"exemplars"`
}

// getEndpointDetail serves GET /api/endpoints/detail —
//
//	?service=<name>&path=<route>&from=&to=&sig=1
//
// sig=1 marks path as an ID-collapsed signature (/orders/:id — the
// table's "group by shape" mode); every section then matches the same
// opSig rewrite the table grouped by. Window defaults to the last hour.
func (s *Server) getEndpointDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	path := q.Get("path")
	if service == "" || path == "" {
		http.Error(w, "service and path required", http.StatusBadRequest)
		return
	}
	sig := q.Get("sig") == "1"
	from, to := parseFromTo(r, time.Hour)
	// v0.9.306 — the scope the table row was computed under.
	env := strings.TrimSpace(q.Get("env"))
	cluster := strings.TrimSpace(q.Get("cluster"))

	key := endpointDetailKey(service, path, sig, from, to, env, cluster)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		eq := chstore.EndpointDetailQuery{
			Service: service, Path: path, BySignature: sig,
			From: from, To: to,
			Env: env, Cluster: cluster,
		}
		out := endpointDetailPayload{
			Service: service, Path: path,
			FromNs: from.UnixNano(), ToNs: to.UnixNano(),
		}
		// Sections run sequentially (each is a PK-pruned single-endpoint
		// read; the payload is cached 30s) with per-section error
		// tolerance — a failed section stays nil, the rest ship. The
		// ctx guard stops the chain when the client is gone / the
		// request deadline hit, so five reads can't outlive one budget.
		if hm, err := s.store.EndpointLatencyHistogram(ctx, eq); err == nil && hm != nil {
			bins, counts := chstore.CollapseLatencyHistogram(hm)
			if len(bins) > 0 {
				out.Histogram = &endpointHistogram{
					Bins: bins, Counts: counts, SamplingRate: hm.SamplingRate,
				}
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if st, err := s.store.EndpointStatusBreakdown(ctx, eq); err == nil {
			out.StatusBreakdown = st
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if ex, err := s.store.EndpointTopExceptions(ctx, eq, 5); err == nil {
			out.TopExceptions = ex
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if ft, err := s.store.EndpointFailingTraces(ctx, eq, 10); err == nil {
			out.FailingTraces = ft
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if slow, errTid, err := s.store.EndpointExemplars(ctx, eq); err == nil && (slow != "" || errTid != "") {
			out.Exemplars = &endpointExemplars{SlowTraceID: slow, ErrorTraceID: errTid}
		}
		return out, nil
	})
}

// getEndpointSplit serves GET /api/endpoints/split —
//
//	?service=<name>&path=<route>&by=<dimension>&from=&to=&sig=1
//
// by is whitelisted (chstore.EndpointSplitDims); anything else 400s with
// the allowed list so a stale frontend build fails loudly, not silently.
func (s *Server) getEndpointSplit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	path := q.Get("path")
	by := strings.TrimSpace(q.Get("by"))
	if service == "" || path == "" {
		http.Error(w, "service and path required", http.StatusBadRequest)
		return
	}
	dims := chstore.EndpointSplitDims()
	valid := false
	for _, d := range dims {
		if d == by {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, fmt.Sprintf("unknown split dimension %q (allowed: %s)",
			by, strings.Join(dims, ", ")), http.StatusBadRequest)
		return
	}
	sig := q.Get("sig") == "1"
	from, to := parseFromTo(r, time.Hour)
	env := strings.TrimSpace(q.Get("env"))
	cluster := strings.TrimSpace(q.Get("cluster"))

	key := endpointSplitKey(service, path, sig, by, from, to, env, cluster)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		eq := chstore.EndpointDetailQuery{
			Service: service, Path: path, BySignature: sig,
			From: from, To: to,
			Env: env, Cluster: cluster,
		}
		rows, err := s.store.EndpointSplit(ctx, eq, by, 10)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []chstore.EndpointSplitRow{}
		}
		return map[string]any{"by": by, "values": rows}, nil
	})
}

// getEndpointDownstream serves GET /api/endpoints/downstream —
//
//	?service=<name>&path=<route>&from=&to=&sig=1&env=&cluster=
//
// The drawer's "Where the time goes" section (v0.9.311, brief N4).
// Sampled by construction: no MV carries route→downstream, so this
// walks a bounded sample of the route's SLOWEST traces and splits
// their time. The payload states the sample size; the UI must render
// it, because a share computed from 200 traces is not a window-wide
// measurement and must not read as one.
//
// 60s cache: the read is two raw-spans passes, which is exactly the
// shape that must not fire on every drawer re-open.
func (s *Server) getEndpointDownstream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	path := q.Get("path")
	if service == "" || path == "" {
		http.Error(w, "service and path required", http.StatusBadRequest)
		return
	}
	sig := q.Get("sig") == "1"
	from, to := parseFromTo(r, time.Hour)
	env := strings.TrimSpace(q.Get("env"))
	cluster := strings.TrimSpace(q.Get("cluster"))

	key := endpointDownstreamKey(service, path, sig, from, to, env, cluster)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		return s.store.EndpointWhereTheTimeGoes(ctx, chstore.EndpointDetailQuery{
			Service: service, Path: path, BySignature: sig,
			From: from, To: to,
			Env: env, Cluster: cluster,
		})
	})
}
