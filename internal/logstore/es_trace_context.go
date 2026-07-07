package logstore

// Trace-context SELF-DISCOVERY against the configured Elasticsearch
// (v0.8.348, pivot Phase 1c — docs/pivot-audit.md §3 ⚠). The audit could not
// verify the PROD ES mappings; instead of a hand-supplied _mapping dump, the
// system inspects its OWN cluster the moment credentials land in Settings →
// Elasticsearch:
//
//  1. field_caps over the trace-id candidate shapes (the exact fan-out
//     traceTermsAny queries: configured field + trace.id / trace_id /
//     traceId / TraceId) → per-field mapping types + a pivotReady verdict.
//     A `text` mapping (or absence) is the silent killer: term clauses
//     match nothing and the trace→log pivot returns empty without error.
//  2. One size:0 aggregation over the last 24h → overall + top-50
//     per-service "% of logs with trace context" (exists on the effective
//     field).
//
// Cost guards (the buildFieldStatsBody / v0.8.3 kit): field_caps is
// metadata-only under a 3s ctx; the coverage search is a single _search,
// size:0, bounded 24h range on the configured timestamp field, ES-side
// timeout 10s + a 12s ctx, track_total_hits off (counts come from exact agg
// doc_counts), terms agg capped at 50 buckets, request_cache ON — the window
// is snapped to a 10-minute boundary so the body stays byte-identical long
// enough for the ES request cache to actually hit (a now-relative or
// per-second body is a new cache entry every call). The API layer adds a 5m
// serveCached on top, so steady-state load on ES is ≤1 aggregation per 5m
// per backend.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

const (
	// traceCoverageWindow is the coverage lookback. 24h matches the
	// "% of logs with trace context" snapshot the audit planned.
	traceCoverageWindow      = 24 * time.Hour
	traceCoverageWindowHours = 24
	// traceCoverageSnap rounds the window end down so the request body is
	// stable across calls within the snap — the request_cache contract.
	traceCoverageSnap = 10 * time.Minute
	// traceFieldCapsTimeout — field_caps touches mapping metadata only;
	// 3s is generous even on wide daily fan-outs.
	traceFieldCapsTimeout = 3 * time.Second
	// traceCoverageTimeout is the HARD ctx cap around the coverage search.
	// 12s: the body carries the 10s ES soft timeout; the extra 2s lets a
	// partial (soft-timed-out) response still arrive instead of the ctx
	// killing the transfer mid-read.
	traceCoverageTimeout = 12 * time.Second
	// traceCoverageTopServices bounds the per-service terms agg.
	traceCoverageTopServices = 50
)

// traceFieldCap is one field's capabilities collapsed across indices:
// every mapping type seen (sorted) + OR-ed searchable/aggregatable.
type traceFieldCap struct {
	Types        []string
	Searchable   bool
	Aggregatable bool
}

// traceFieldCandidates returns the trace-id field shapes to probe, in
// verdict priority order: the operator-configured field FIRST (explicit
// override wins), then the four common shipper spellings — the same set
// traceTermsAny fans its term clauses over, deduped the same way.
func traceFieldCandidates(configured string) []string {
	candidates := []string{configured, "trace.id", "trace_id", "traceId", "TraceId"}
	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// parseFieldCaps decodes a field_caps response body into per-field
// capabilities. Pure — unit-tested against keyword / text / absent
// fixtures without a live ES. Fields absent from the mapping simply don't
// appear in the response (IncludeUnmapped is off), so absence == no entry.
func parseFieldCaps(raw []byte) (map[string]traceFieldCap, error) {
	var decoded struct {
		Fields map[string]map[string]struct {
			Type          string `json:"type"`
			Searchable    bool   `json:"searchable"`
			Aggregatable  bool   `json:"aggregatable"`
			MetadataField bool   `json:"metadata_field"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode field_caps: %w", err)
	}
	out := make(map[string]traceFieldCap, len(decoded.Fields))
	for name, byType := range decoded.Fields {
		fc := traceFieldCap{}
		for typ, c := range byType {
			fc.Types = append(fc.Types, typ)
			fc.Searchable = fc.Searchable || c.Searchable
			fc.Aggregatable = fc.Aggregatable || c.Aggregatable
		}
		sort.Strings(fc.Types)
		out[name] = fc
	}
	return out, nil
}

// traceContextFields projects the field_caps result onto the candidate
// list, preserving candidate order (configured first) so the verdict walk
// below is a plain first-present scan.
func traceContextFields(candidates []string, configured string, caps map[string]traceFieldCap) []TraceContextField {
	out := make([]TraceContextField, 0, len(candidates))
	for _, name := range candidates {
		c := caps[name] // zero value = absent
		types := c.Types
		if types == nil {
			types = []string{}
		}
		out = append(out, TraceContextField{
			Name: name, Types: types,
			Searchable: c.Searchable, Aggregatable: c.Aggregatable,
			Configured: name == configured,
		})
	}
	return out
}

// resolveEffectiveTraceField is the VERDICT rule (pure, table-tested):
// the effective field is the configured one when present in the mapping
// (any type), else the first present fan-out shape in traceTermsAny order;
// pivotReady ⇔ that field is keyword-mapped. Mixed keyword+text across
// dailies counts as keyword (the term clause matches on the keyword-mapped
// indices). All-absent → ("<configured>", "absent", false).
func resolveEffectiveTraceField(fields []TraceContextField) (name, typ string, pivotReady bool) {
	for _, f := range fields {
		if len(f.Types) == 0 {
			continue
		}
		typ = f.Types[0]
		for _, t := range f.Types {
			if t == "keyword" {
				typ = "keyword"
				break
			}
		}
		return f.Name, typ, typ == "keyword"
	}
	if len(fields) > 0 {
		return fields[0].Name, "absent", false
	}
	return "", "absent", false
}

// pickServiceAggField picks the terms-aggregatable variant of the
// configured service field from the SAME field_caps response (no second
// round-trip, unlike FieldStats' empty-result retry): the bare field when
// aggregatable (pure keyword mapping), else its .keyword subfield (text +
// keyword multi-field), else the bare field as a harmless fallback — terms
// on an unmapped field returns empty buckets while the overall counts
// still come back.
func pickServiceAggField(svc string, caps map[string]traceFieldCap) string {
	if strings.HasSuffix(svc, ".keyword") {
		return svc
	}
	if caps[svc].Aggregatable {
		return svc
	}
	if kw := svc + ".keyword"; caps[kw].Aggregatable {
		return kw
	}
	return svc
}

// buildTraceCoverageBody constructs the single coverage _search body.
// PURE — unit-tested for the cost guards. size:0, bounded range on the
// timestamp field, exact counts via filter-agg doc_counts (total =
// match_all filter; withTrace = exists on the effective field) so
// track_total_hits stays OFF, terms agg capped at 50 services with the
// same exists sub-filter, ES soft timeout inline.
func buildTraceCoverageBody(tsField, effField, svcAggField string, from, to time.Time, esTimeout string) map[string]any {
	withTrace := func() map[string]any {
		return map[string]any{
			"filter": map[string]any{"exists": map[string]any{"field": effField}},
		}
	}
	return map[string]any{
		"size": 0,
		"query": map[string]any{
			"range": map[string]any{tsField: map[string]any{
				"gte": from.UTC().Format(time.RFC3339),
				"lte": to.UTC().Format(time.RFC3339),
			}},
		},
		"aggs": map[string]any{
			"total":      map[string]any{"filter": map[string]any{"match_all": map[string]any{}}},
			"with_trace": withTrace(),
			"services": map[string]any{
				"terms": map[string]any{
					"field": svcAggField,
					"size":  traceCoverageTopServices,
					// Shard-level headroom for accurate top-50 on
					// multi-shard indices (the buildFieldStatsBody ratio).
					"shard_size": traceCoverageTopServices * 10,
				},
				"aggs": map[string]any{"with_trace": withTrace()},
			},
		},
		"track_total_hits": false,
		"timeout":          esTimeout,
	}
}

// parseTraceCoverageResponse decodes the coverage aggregation. Pure —
// unit-tested against a fixture. Bucket keys stringify via %v like
// FieldStats (a numeric-mapped service field must not fail the decode).
func parseTraceCoverageResponse(raw []byte) (total, withTrace int64, services []TraceContextServiceCoverage, err error) {
	var decoded struct {
		Aggregations struct {
			Total struct {
				DocCount int64 `json:"doc_count"`
			} `json:"total"`
			WithTrace struct {
				DocCount int64 `json:"doc_count"`
			} `json:"with_trace"`
			Services struct {
				Buckets []struct {
					Key       any   `json:"key"`
					DocCount  int64 `json:"doc_count"`
					WithTrace struct {
						DocCount int64 `json:"doc_count"`
					} `json:"with_trace"`
				} `json:"buckets"`
			} `json:"services"`
		} `json:"aggregations"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return 0, 0, nil, fmt.Errorf("decode coverage response: %w", err)
	}
	services = make([]TraceContextServiceCoverage, 0, len(decoded.Aggregations.Services.Buckets))
	for _, b := range decoded.Aggregations.Services.Buckets {
		services = append(services, TraceContextServiceCoverage{
			Service:   fmt.Sprintf("%v", b.Key),
			Total:     b.DocCount,
			WithTrace: b.WithTrace.DocCount,
		})
	}
	return decoded.Aggregations.Total.DocCount, decoded.Aggregations.WithTrace.DocCount, services, nil
}

// fieldCaps runs one field_caps request against the given indices and
// returns the collapsed per-field capabilities. Failures funnel through
// recordQueryError so they land on /admin/elastic's error panel.
func (s *ESStore) fieldCaps(ctx context.Context, indices, fields []string) (map[string]traceFieldCap, error) {
	tru := true
	probe := []byte(strings.Join(fields, ","))
	req := esapi.FieldCapsRequest{
		Index:             indices,
		Fields:            fields,
		AllowNoIndices:    &tru,
		IgnoreUnavailable: &tru,
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		return nil, s.recordQueryError("trace-context field_caps", indices, probe, 0,
			fmt.Errorf("ES field_caps: %w", err))
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, s.recordQueryError("trace-context field_caps", indices, probe, res.StatusCode,
			parseESError("trace-context field_caps", res, s.cfg.Index))
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("read field_caps response: %w", err)
	}
	return parseFieldCaps(raw)
}

// TraceContextDiagnostics implements TraceContextDiagnoser (pivot Phase 1c
// self-discovery). Errors come back as a typed report — field_caps failure
// → {available:false, reason}; a coverage failure keeps the field verdict
// (the operationally critical half) and sets Reason. The error return is
// reserved for programming errors (marshal), so the handler never turns a
// slow/misconfigured cluster into a raw 5xx.
func (s *ESStore) TraceContextDiagnostics(ctx context.Context) (*TraceContextReport, error) {
	rep := &TraceContextReport{
		WindowHours: traceCoverageWindowHours,
		Fields:      []TraceContextField{},
		Services:    []TraceContextServiceCoverage{},
	}

	to := time.Now().UTC().Truncate(traceCoverageSnap)
	from := to.Add(-traceCoverageWindow)
	// Same window-narrowed concrete-index resolution every query path uses
	// (es_indices.go) — the verdict must describe the indices the pivot
	// actually queries, and the coverage agg must not fan out past 24h of
	// dailies.
	idx := s.queryIndices(ctx, Filter{From: from, To: to})

	candidates := traceFieldCandidates(s.fields.TraceID)
	capsFields := append(append([]string{}, candidates...),
		s.fields.Service, s.fields.Service+".keyword")

	fcCtx, cancelFC := context.WithTimeout(ctx, traceFieldCapsTimeout)
	defer cancelFC()
	caps, err := s.fieldCaps(fcCtx, idx, capsFields)
	if err != nil {
		rep.Available = false
		rep.Reason = "field_caps failed: " + err.Error()
		rep.EffectiveType = "absent"
		return rep, nil
	}
	rep.Available = true
	rep.Fields = traceContextFields(candidates, s.fields.TraceID, caps)
	rep.EffectiveField, rep.EffectiveType, rep.PivotReady = resolveEffectiveTraceField(rep.Fields)

	body, err := json.Marshal(buildTraceCoverageBody(
		s.fields.Timestamp, rep.EffectiveField, pickServiceAggField(s.fields.Service, caps),
		from, to, "10s"))
	if err != nil {
		return nil, err
	}
	covCtx, cancelCov := context.WithTimeout(ctx, traceCoverageTimeout)
	defer cancelCov()
	tru := true
	req := esapi.SearchRequest{
		Index:             idx,
		Body:              bytes.NewReader(body),
		AllowNoIndices:    &tru,
		IgnoreUnavailable: &tru,
		RequestCache:      &tru,
	}
	res, err := req.Do(covCtx, s.cli)
	if err != nil {
		s.recordQueryError("trace-context coverage", idx, body, 0, fmt.Errorf("ES coverage: %w", err))
		rep.Reason = "coverage query failed: " + err.Error()
		return rep, nil
	}
	defer res.Body.Close()
	if res.IsError() {
		perr := parseESError("trace-context coverage", res, s.cfg.Index)
		s.recordQueryError("trace-context coverage", idx, body, res.StatusCode, perr)
		rep.Reason = "coverage query failed: " + perr.Error()
		return rep, nil
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		rep.Reason = "coverage read failed: " + err.Error()
		return rep, nil
	}
	total, withTrace, services, err := parseTraceCoverageResponse(raw)
	if err != nil {
		rep.Reason = err.Error()
		return rep, nil
	}
	rep.Total, rep.WithTrace = total, withTrace
	rep.Services = services
	return rep, nil
}
