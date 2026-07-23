package logstore

// Raw watcher search (v0.9.x — ES Watcher birebir-JSON import,
// Faz-1). RawSearch executes an operator-supplied ES search body
// VERBATIM against operator-supplied indices — the executable core of
// "birebir aynı Watcher JSON'ı Coremetry çalışabilsin". Only the cost
// guards are injected on top (injectRawSearchGuards below); the query
// semantics are untouched.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// injectRawSearchGuards parses the watch's search body and injects the
// cost-guard discipline every other ES path in this store already
// carries (CH-vs-ES divergence lesson):
//
//   - size: 0 — FORCED. The watcher path only compares hits.total;
//     shipping hit sources would be pure transfer cost.
//   - timeout — injected when absent (esTimeoutFromEnv default); an
//     operator-set value is kept (the evaluator's 30s ctx deadline is
//     the hard bound either way).
//   - track_total_hits — max(body value, cap, 10). The caller passes
//     threshold*2 so a "gte 15000" watch counts past the ES 10k
//     default saturation instead of flat-lining at 10000. An explicit
//     `true` (exact count) is kept; `false` would hide the count the
//     compare needs, so it's replaced by the cap. A NEGATIVE cap is
//     the exact-count sentinel (threshold near the ES int limit —
//     watcherTotalCap, review F11): track_total_hits becomes `true`.
//   - time window — when the query subtree carries NO range clause in
//     a POSITIVE (restricting) context, the query is wrapped in a
//     bool filter on tsField over the last 24h (the EQL
//     mandatory-window pattern): an unbounded count over a
//     billion-doc cluster is the most expensive query this store can
//     emit. The second return reports whether this window was
//     injected, so RawSearch can sanity-check tsField (review F2).
//
// Numbers decode via json.Number (review F4): a float64 round-trip
// silently corrupts int64-scale literals (epoch-nanos range bounds,
// 64-bit term IDs — 9007199254740993 re-marshals as …992), breaking
// the verbatim contract with zero indication.
//
// Pure function — table-tested in es_rawsearch_test.go.
func injectRawSearchGuards(body []byte, tsField, timeout string, trackTotalCap int) ([]byte, bool, error) {
	m := map[string]any{}
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			return nil, false, fmt.Errorf("watcher search body: %w", err)
		}
		if m == nil {
			m = map[string]any{}
		}
	}

	m["size"] = 0
	if _, ok := m["timeout"]; !ok {
		m["timeout"] = timeout
	}

	if trackTotalCap < 0 {
		// Exact-count sentinel: any numeric cap the ES int range can
		// hold would land BELOW the threshold, so ask for the real
		// (long) total instead.
		m["track_total_hits"] = true
	} else {
		if trackTotalCap < 10 {
			trackTotalCap = 10
		}
		switch v := m["track_total_hits"].(type) {
		case bool:
			if !v {
				m["track_total_hits"] = trackTotalCap
			}
			// true = exact count, already sufficient for any threshold.
		case json.Number:
			if n, err := v.Int64(); err != nil || n < int64(trackTotalCap) {
				m["track_total_hits"] = trackTotalCap
			}
		default:
			m["track_total_hits"] = trackTotalCap
		}
	}

	injectedWindow := false
	if !hasRangeClause(m["query"]) {
		injectedWindow = true
		window := map[string]any{
			"range": map[string]any{
				tsField: map[string]any{"gte": "now-24h", "lte": "now"},
			},
		}
		if q, ok := m["query"]; ok && q != nil {
			m["query"] = map[string]any{
				"bool": map[string]any{
					"must":   []any{q},
					"filter": []any{window},
				},
			}
		} else {
			m["query"] = map[string]any{
				"bool": map[string]any{"filter": []any{window}},
			}
		}
	}

	out, err := json.Marshal(m)
	return out, injectedWindow, err
}

// hasRangeClause walks the POSITIVE (restricting) contexts of a
// decoded query subtree looking for a `range` object — the signal the
// watch already bounds its own time (or numeric) window and the 24h
// fallback must not double-filter. Deliberately permissive on the
// FIELD: a numeric range in a positive context also counts, because
// wrapping such a query in an EXTRA time window could silently change
// what the operator's watch counts.
//
// v0.9.x (review F3): `must_not` and `should` subtrees are SKIPPED —
// a must_not range EXCLUDES a window (leaving the rest of retention
// in scope) and a should range is optional/scoring context — so a
// range found only there no longer suppresses the 24h fallback (the
// exact unbounded-count class this guard exists to prevent). Flat
// rule by design: even for a bool whose sole occupant is a should
// range the injected window only ANDs a ≤24h bound on top — the cost
// mandate wins over that exotic shape.
func hasRangeClause(v any) bool {
	switch node := v.(type) {
	case map[string]any:
		for k, child := range node {
			if k == "range" {
				if _, ok := child.(map[string]any); ok {
					return true
				}
			}
			if k == "must_not" || k == "should" {
				continue
			}
			if hasRangeClause(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if hasRangeClause(child) {
				return true
			}
		}
	}
	return false
}

// RawSearch runs the guarded body and returns hits.total.value. Empty
// indices fall back to the store's configured pattern. Cost posture
// mirrors Search: request cache off (live data), missing indices are
// 0 hits rather than 404s, failures ride recordQueryError so
// /admin/elastic sees them.
//
// Silent-zero guards (review F1 + F2 — proven against live ES 8.15):
//
//   - _shards.total == 0 → ERROR, never 0/nil. ES security resolves
//     index patterns against the credential's grants; an unauthorized
//     or nonexistent index answers HTTP 200 with 0 hits under
//     allow_no_indices/ignore_unavailable, which would leave the
//     watcher permanently green with nothing in /admin/elastic.
//   - injected 24h window + 0 hits → one field_caps probe on tsField;
//     if the field is unmapped in the watch's indices the range can
//     never match (a range on a nonexistent field is 0 hits, not an
//     error) → ERROR naming the field. Runs at most once per watch
//     interval and only while the count is zero, so no caching.
func (s *ESStore) RawSearch(ctx context.Context, indicesIn []string, body json.RawMessage, trackTotalCap int) (int64, error) {
	guarded, injectedWindow, err := injectRawSearchGuards(body, s.fields.Timestamp, esTimeoutFromEnv("10s"), trackTotalCap)
	if err != nil {
		return 0, err
	}
	idx := make([]string, 0, len(indicesIn))
	for _, i := range indicesIn {
		if t := strings.TrimSpace(i); t != "" {
			idx = append(idx, t)
		}
	}
	if len(idx) == 0 {
		idx = []string{s.cfg.Index}
	}

	tru := true
	reqCacheOff := false
	req := esapi.SearchRequest{
		Index:             idx,
		Body:              bytes.NewReader(guarded),
		AllowNoIndices:    &tru,
		IgnoreUnavailable: &tru,
		RequestCache:      &reqCacheOff,
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		return 0, s.recordQueryError("watcher raw search", idx, guarded, 0, fmt.Errorf("ES raw search: %w", err))
	}
	defer res.Body.Close()
	if res.IsError() {
		return 0, s.recordQueryError("watcher raw search", idx, guarded, res.StatusCode,
			parseESError("watcher raw search", res, strings.Join(idx, ",")))
	}
	var raw struct {
		Shards struct {
			Total int `json:"total"`
		} `json:"_shards"`
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return 0, fmt.Errorf("decode ES raw search response: %w", err)
	}
	if raw.Shards.Total == 0 {
		return 0, s.recordQueryError("watcher raw search", idx, guarded, res.StatusCode,
			fmt.Errorf("no authorized or existing index matched %v (0 shards searched) — check the ES credential grants read on the watch's indices", idx))
	}
	if injectedWindow && raw.Hits.Total.Value == 0 {
		// Best-effort probe: an unusable probe (transport error, 403 on
		// field_caps) must not turn a legitimate zero count into noise,
		// so only a SUCCESSFUL probe that shows the field unmapped
		// errors out. (fieldCaps' error label says trace-context — a
		// cosmetic mislabel on the rare probe-failure log line, not
		// worth a duplicate helper.)
		if caps, capErr := s.fieldCaps(ctx, idx, []string{s.fields.Timestamp}); capErr == nil {
			if _, mapped := caps[s.fields.Timestamp]; !mapped {
				return 0, s.recordQueryError("watcher raw search", idx, guarded, res.StatusCode,
					fmt.Errorf("injected 24h window filters on timestamp field %q, which is not mapped in %v — the watch can never match; fix the ES field map (Settings) or add an explicit range clause to the watch body", s.fields.Timestamp, idx))
			}
		}
	}
	return raw.Hits.Total.Value, nil
}
