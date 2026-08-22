package logstore

// Histogram break-down axes that do NOT live on a single Elasticsearch
// field (v0.9.1250 — the last Kibana-parity gap on /logs: "hangi
// cluster / namespace gürültülü?").
//
// severity and service each aggregate on ONE field (or, for severity,
// one filters-agg over a candidate set — v0.8.377). cluster and
// namespace can't: the value lands on a different document path per
// shipping pipeline, which is why the FILTER side of both is already a
// multi-way bool.should (buildQuery's cluster leg, expandShorthand's
// `namespace:` alias). A terms agg takes exactly one field, so the
// break-down runs ONE terms agg PER CANDIDATE FIELD inside the same
// single _search and merges the outputs by GROUP NAME in Go.
//
// Why field_caps first, rather than just aggregating on every
// candidate:
//   - A terms agg on a `text`-mapped field is a 400
//     (IllegalArgumentException, "Fielddata is disabled") and would
//     kill the WHOLE histogram request — every band, not just that
//     field. Aggregating only on `<f>.keyword` would be safe but blind
//     on managed mappings (OpenShift cluster-logging types
//     `openshift.labels.cluster` as a plain keyword with no `.keyword`
//     subfield — the operator's real prod shape), so the axis would
//     answer "OTHER" for everything.
//   - Resolving first also SHRINKS the query: candidates absent from
//     the mapping never reach the body, so the typical cluster
//     break-down runs one or two terms aggs, i.e. the same cost shape
//     as today's service break-down.
//
// The verdict is cached per axis for groupFieldTTL (positive AND
// negative), mirroring the env-field discovery cache (es_env_field.go)
// — steady state is ≤1 field_caps per axis per 10 min per backend,
// never per request (the twice-stated ES-cost constraint).

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

const (
	// groupFieldTTL — same reasoning as envFieldTTL: mappings change
	// on index rollover / pipeline deploys, not per request.
	groupFieldTTL = 10 * time.Minute
	// groupFieldCapsTimeout — field_caps is mapping metadata only.
	groupFieldCapsTimeout = 3 * time.Second
	// groupTermsSize / groupTermsShardSize — per-CANDIDATE caps, and
	// deliberately TIGHTER than the single-field service path's
	// 50/500 (v0.5.396): the multi-field fan-out multiplies the terms
	// collections inside one request, and the chart draws top-5 +
	// "diğer" anyway. 20 also matches the CH path's `top_groups
	// LIMIT 20`. Everything beyond the cap is absorbed by the
	// total_buckets OTHER synthesis, so the stack never undercounts.
	groupTermsSize      = 20
	groupTermsShardSize = 200
)

// esClusterFields — the candidate document paths a k8s/OpenShift
// cluster name lands on. ONE list, used by both the cluster FILTER
// (buildQuery) and the cluster BREAK-DOWN: a cluster the histogram
// names must be a cluster the select beside it can filter to
// (v0.8.265 class). v0.9.452 order note: the operator's prod ES
// carries the value ONLY on `openshift.labels.cluster`
// (ClusterLogForwarder writes it top-level, no resource prefix).
var esClusterFields = []string{
	"resource_attributes.k8s.cluster.name",
	"resource_attributes.openshift.cluster.name",
	"resource_attributes.cluster",
	"openshift.labels.cluster",
}

// esNamespaceFields — the candidate document paths a k8s namespace
// lands on. Same single-contract rule as the cluster list, against the
// only namespace contract that exists on the ES side: the `namespace:`
// search shorthand (expandShorthand). A namespace the break-down names
// must therefore be a namespace `namespace:<value>` can find.
var esNamespaceFields = []string{
	"kubernetes.namespace.name",
	"kubernetes.namespace_name",
	"kubernetes.namespace",
	"k8s.namespace.name",
	"resource.k8s.namespace.name",
	"namespace",
}

// groupAxisCandidates returns the candidate field paths for a
// multi-field break-down axis, or nil for an axis that is NOT
// multi-field (severity/service/total go through their own builders).
func groupAxisCandidates(groupBy string) []string {
	switch groupBy {
	case "cluster":
		return esClusterFields
	case "namespace":
		return esNamespaceFields
	}
	return nil
}

// resolveGroupAggFields is the VERDICT rule (pure, table-tested —
// es_group_fields_test.go): for each candidate, pick the spelling that
// a terms agg can actually run on — the bare field when it is
// keyword-mapped + aggregatable, else its `.keyword` subfield when
// that is. A text-only candidate (no keyword path) is SKIPPED rather
// than aggregated: that is the request-killing 400 described above.
// Order follows the candidate order; duplicates are dropped so two
// candidates resolving to the same physical field can't double-count.
func resolveGroupAggFields(candidates []string, caps map[string]traceFieldCap) []string {
	keywordCapable := func(c traceFieldCap) bool {
		for _, t := range c.Types {
			if t == "keyword" {
				return c.Aggregatable
			}
		}
		return false
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, name := range candidates {
		agg := ""
		switch {
		case keywordCapable(caps[name]):
			agg = name
		case keywordCapable(caps[name+".keyword"]):
			agg = name + ".keyword"
		default:
			continue
		}
		if _, dup := seen[agg]; dup {
			continue
		}
		seen[agg] = struct{}{}
		out = append(out, agg)
	}
	return out
}

// esGroupFieldsCache memoises the per-axis discovery verdict. Positive
// AND negative are cached for the same TTL (es_env_field.go rationale).
type esGroupFieldsCache struct {
	mu     sync.Mutex
	byAxis map[string]esGroupFieldsVerdict
}

type esGroupFieldsVerdict struct {
	fields  []string
	expires time.Time
}

// histogramGroupAggFields resolves (and caches) the aggregatable field
// spellings for a multi-field axis. Empty result ⇒ the axis cannot be
// answered on this backend; the caller falls back to the ungrouped
// _total body, which renders as a single visibly-unbroken series
// rather than a plausible-looking wrong break-down.
func (s *ESStore) histogramGroupAggFields(ctx context.Context, groupBy string) []string {
	candidates := groupAxisCandidates(groupBy)
	if len(candidates) == 0 {
		return nil
	}
	now := time.Now()
	s.groupFields.mu.Lock()
	if v, ok := s.groupFields.byAxis[groupBy]; ok && now.Before(v.expires) {
		f := v.fields
		s.groupFields.mu.Unlock()
		return f
	}
	s.groupFields.mu.Unlock()

	fcCtx, cancel := context.WithTimeout(ctx, groupFieldCapsTimeout)
	defer cancel()
	// Window-narrowed concrete indices, same as every read path — the
	// verdict must describe the indices queries actually hit.
	idx := s.queryIndices(fcCtx, Filter{From: now.Add(-24 * time.Hour), To: now})
	caps, err := s.fieldCaps(fcCtx, idx, envFieldCapsFields(candidates))
	var fields []string
	if err != nil {
		// Already recorded on /admin/elastic by fieldCaps. Cache the
		// negative for one TTL so a flapping cluster isn't re-probed
		// per request.
		log.Printf("[logstore-es] %s break-down field discovery failed (axis reported as single _total for %s): %v",
			groupBy, groupFieldTTL, err)
	} else if fields = resolveGroupAggFields(candidates, caps); len(fields) > 0 {
		log.Printf("[logstore-es] %s break-down aggregating on %v (of candidates %v)", groupBy, fields, candidates)
	} else {
		log.Printf("[logstore-es] no aggregatable %s field among %v — break-down returns a single _total series",
			groupBy, candidates)
	}
	s.groupFields.mu.Lock()
	if s.groupFields.byAxis == nil {
		s.groupFields.byAxis = map[string]esGroupFieldsVerdict{}
	}
	s.groupFields.byAxis[groupBy] = esGroupFieldsVerdict{fields: fields, expires: now.Add(groupFieldTTL)}
	s.groupFields.mu.Unlock()
	return fields
}

// multiFieldAggKey names the per-candidate terms agg inside the body.
func multiFieldAggKey(i int) string { return fmt.Sprintf("groups_%d", i) }

// buildMultiFieldHistogramBody — one terms agg per resolved candidate
// field, each with the shared date_histogram sub-agg, plus the
// ungrouped total_buckets the caller uses to synthesise OTHER
// (v0.5.396). PURE — unit-tested in es_group_fields_test.go.
//
// Every v0.8.3 cost guard of the single-field builder is carried over
// verbatim: size:0, min_doc_count:1 (sparse like CH), soft timeout,
// track_total_hits:false; request_cache is set on the SearchRequest by
// the caller. Per-field size/shard_size are the tighter caps above.
func buildMultiFieldHistogramBody(query any, timestampField string, aggFields []string, bucketSec int, esTimeout string) map[string]any {
	dateAgg := histogramDateAgg(timestampField, bucketSec)
	aggs := map[string]any{"total_buckets": dateAgg}
	for i, field := range aggFields {
		aggs[multiFieldAggKey(i)] = map[string]any{
			"terms": map[string]any{
				"field":      field,
				"size":       groupTermsSize,
				"shard_size": groupTermsShardSize,
			},
			"aggs": map[string]any{"buckets": dateAgg},
		}
	}
	return map[string]any{
		"size":             0,
		"query":            query,
		"aggs":             aggs,
		"track_total_hits": false,
		"timeout":          esTimeout,
	}
}

// mergeFieldSeries merges the per-candidate-field terms outputs into
// ONE series list keyed by group NAME, summing per timestamp. Sorted
// by total descending (then name) so the frontend's top-5 + "diğer"
// fold sees a stable, meaningful order.
//
// HONESTY — the known double-count: a document that carries the same
// logical value on TWO different candidate paths (e.g. both
// `resource_attributes.k8s.cluster.name` AND `openshift.labels.cluster`)
// is counted once per path, so its group reads high. This is rare —
// the paths belong to different shipping pipelines and a document
// normally travels exactly one — and it is bounded: the inflation
// stays inside the named band, and the OTHER synthesis (total minus
// sum-of-groups, clamped at 0 by the caller) absorbs the arithmetic
// rather than going negative. The alternative — a single cardinality
// pass over a scripted field union — is a per-document script at
// billion-doc scale, which the ES-cost discipline rules out. PURE,
// table-tested including the double-count case.
func mergeFieldSeries(perField [][]LogSeries) []LogSeries {
	byName := map[string]map[int64]int64{}
	order := []string{}
	for _, list := range perField {
		for _, s := range list {
			pts, ok := byName[s.Name]
			if !ok {
				pts = map[int64]int64{}
				byName[s.Name] = pts
				order = append(order, s.Name)
			}
			for _, p := range s.Points {
				pts[p.T] += p.V
			}
		}
	}
	out := make([]LogSeries, 0, len(order))
	totals := make(map[string]int64, len(order))
	for _, name := range order {
		pts := byName[name]
		ts := make([]int64, 0, len(pts))
		for t := range pts {
			ts = append(ts, t)
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		series := LogSeries{Name: name, Points: make([]LogPoint, 0, len(ts))}
		var total int64
		for _, t := range ts {
			series.Points = append(series.Points, LogPoint{T: t, V: pts[t]})
			total += pts[t]
		}
		totals[name] = total
		out = append(out, series)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if totals[out[i].Name] != totals[out[j].Name] {
			return totals[out[i].Name] > totals[out[j].Name]
		}
		return out[i].Name < out[j].Name
	})
	return out
}
