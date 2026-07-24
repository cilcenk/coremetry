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
	"io"
	"strconv"
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

	injectedWindow := injectWindowIfUnbounded(m, tsField)

	out, err := json.Marshal(m)
	return out, injectedWindow, err
}

// injectWindowIfUnbounded is the shared window core (Faz-2 refactor):
// when the decoded body's query subtree carries no range clause in a
// POSITIVE context, the query is wrapped in a bool filter on tsField
// over the last 24h. Used by both the count search
// (injectRawSearchGuards) and the sample fetch
// (injectRawSampleGuards) so the two paths can never drift on the
// unbounded-count guard. Returns whether the window was injected.
func injectWindowIfUnbounded(m map[string]any, tsField string) bool {
	if hasRangeClause(m["query"]) {
		return false
	}
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
	return true
}

// injectRawSampleGuards shapes the watch's own query for the tiny
// FIRE-time sample fetch (Faz-2 — hits[] örnekleri): the notification
// examples reuse the exact query semantics but must stay cheap, so
// the guards differ from the count path where the purpose differs:
//
//   - size — the caller's n clamped to [1,5]; an operator size is
//     overridden (this is OUR query shape, not the watch's).
//   - _source — restricted to the configured summary fields
//     (timestamp + message + service); shipping whole documents for a
//     3-line summary would be pure transfer cost.
//   - track_total_hits: false — the count query already ran; the
//     samples don't need a second count.
//   - sort — newest-first on tsField injected when the body has none
//     (unmapped_type keeps an unmapped timestamp field from erroring
//     the whole fetch); an operator sort is kept.
//   - timeout + 24h window — same discipline as the count path
//     (shared injectWindowIfUnbounded core).
//
// Numbers decode via json.Number — the verbatim contract (review F4)
// applies to this body too. Pure function — table-tested.
func injectRawSampleGuards(body []byte, tsField, timeout string, n int, sourceFields []string) ([]byte, error) {
	m := map[string]any{}
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("watcher sample body: %w", err)
		}
		if m == nil {
			m = map[string]any{}
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 5 {
		n = 5
	}
	m["size"] = n
	// v0.9.202 review-fix — örnek sorgusu YALNIZ doküman çeker: watch
	// body'sinden gelen aggs/aggregations fire anında ikinci kez
	// ÇALIŞTIRILMAZ (maliyet + anlamsız), sayfalama offset'i sıfırlanır.
	delete(m, "aggs")
	delete(m, "aggregations")
	delete(m, "from")
	if _, ok := m["timeout"]; !ok {
		m["timeout"] = timeout
	}
	m["track_total_hits"] = false
	flds := make([]any, 0, len(sourceFields))
	for _, f := range sourceFields {
		if f != "" {
			flds = append(flds, f)
		}
	}
	m["_source"] = flds
	if _, ok := m["sort"]; !ok {
		m["sort"] = []any{map[string]any{tsField: map[string]any{"order": "desc", "unmapped_type": "date"}}}
	}
	injectWindowIfUnbounded(m, tsField)
	return json.Marshal(m)
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
	total, _, err := s.rawCountSearch(ctx, indicesIn, body, trackTotalCap, false)
	return total, err
}

// RawSearchPayload runs the SAME guarded count search as RawSearch
// (one shared core — identical guards, identical silent-zero checks)
// but additionally returns the ctx.payload-shaped response subset the
// Faz-2 agg-path conditions walk:
//
//	{"hits":{"total":{"value":N}},"aggregations":{…}}
//
// Aggregations pass through VERBATIM (json.RawMessage — no float64
// round-trip). Watches without agg conditions stay on RawSearch; this
// path exists only for compare/array_compare over
// ctx.payload.aggregations.….
func (s *ESStore) RawSearchPayload(ctx context.Context, indicesIn []string, body json.RawMessage, trackTotalCap int) (json.RawMessage, int64, error) {
	total, aggs, err := s.rawCountSearch(ctx, indicesIn, body, trackTotalCap, true)
	if err != nil {
		return nil, 0, err
	}
	return watcherPayloadJSON(total, aggs), total, nil
}

// watcherPayloadJSON assembles the payload subset from the decoded
// response pieces. Pure — table-tested. The aggregations key is
// omitted when the response carried none, so a condition path into a
// missing agg fails loudly at extraction instead of comparing nil.
func watcherPayloadJSON(total int64, aggs json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	fmt.Fprintf(&b, `{"hits":{"total":{"value":%d}}`, total)
	if len(bytes.TrimSpace(aggs)) > 0 {
		b.WriteString(`,"aggregations":`)
		b.Write(aggs)
	}
	b.WriteString("}")
	return b.Bytes()
}

// watcherIndices trims the watch's index list, falling back to the
// store's configured pattern when empty.
func (s *ESStore) watcherIndices(indicesIn []string) []string {
	idx := make([]string, 0, len(indicesIn))
	for _, i := range indicesIn {
		if t := strings.TrimSpace(i); t != "" {
			idx = append(idx, t)
		}
	}
	if len(idx) == 0 {
		idx = []string{s.cfg.Index}
	}
	return idx
}

// rawCountSearch is the shared executable core of RawSearch and
// RawSearchPayload: guard injection, the search itself, the
// silent-zero guards (0-shards, unmapped-timestamp probe), and the
// response decode — total plus the raw aggregations subtree.
func (s *ESStore) rawCountSearch(ctx context.Context, indicesIn []string, body json.RawMessage, trackTotalCap int, wantAggs bool) (int64, json.RawMessage, error) {
	guarded, injectedWindow, err := injectRawSearchGuards(body, s.fields.Timestamp, esTimeoutFromEnv("10s"), trackTotalCap)
	if err != nil {
		return 0, nil, err
	}
	idx := s.watcherIndices(indicesIn)

	tru := true
	reqCacheOff := false
	// v0.9.202 review-fix — FilterPath: hits-tipi watch (wantAggs=false)
	// aggregations subtree'sini HİÇ taşımaz (import edilen body agg içerse
	// bile ES yanıttan kırpar — transfer+buffer maliyeti sıfırlanır);
	// agg-tipi yalnız ihtiyacını ister.
	filter := []string{"hits.total", "_shards"}
	if wantAggs {
		filter = append(filter, "aggregations")
	}
	req := esapi.SearchRequest{
		Index:             idx,
		Body:              bytes.NewReader(guarded),
		AllowNoIndices:    &tru,
		IgnoreUnavailable: &tru,
		RequestCache:      &reqCacheOff,
		FilterPath:        filter,
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		return 0, nil, s.recordQueryError("watcher raw search", idx, guarded, 0, fmt.Errorf("ES raw search: %w", err))
	}
	defer res.Body.Close()
	if res.IsError() {
		return 0, nil, s.recordQueryError("watcher raw search", idx, guarded, res.StatusCode,
			parseESError("watcher raw search", res, strings.Join(idx, ",")))
	}
	// v0.9.202 review-fix — yanıt tavanı: operatör body'sindeki dev
	// terms/composite agg'ler multi-MB aggregations üretebilir; sınırsız
	// buffer + her değerlendirmede yeniden decode bellek yoludur. 8MB
	// tavan; aşan watch YÜKSEK SESLE hata alır (agg'sini küçültsün).
	const rawSearchMaxResp = 8 << 20
	respBody, rerr := io.ReadAll(io.LimitReader(res.Body, rawSearchMaxResp+1))
	if rerr != nil {
		return 0, nil, fmt.Errorf("read ES raw search response: %w", rerr)
	}
	if len(respBody) > rawSearchMaxResp {
		return 0, nil, s.recordQueryError("watcher raw search", idx, guarded, res.StatusCode,
			fmt.Errorf("ES response exceeds the 8MB watcher cap (aggregations too large) — shrink the watch's aggregation (e.g. terms size)"))
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
		Aggregations json.RawMessage `json:"aggregations"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return 0, nil, fmt.Errorf("decode ES raw search response: %w", err)
	}
	if raw.Shards.Total == 0 {
		return 0, nil, s.recordQueryError("watcher raw search", idx, guarded, res.StatusCode,
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
				return 0, nil, s.recordQueryError("watcher raw search", idx, guarded, res.StatusCode,
					fmt.Errorf("injected 24h window filters on timestamp field %q, which is not mapped in %v — the watch can never match; fix the ES field map (Settings) or add an explicit range clause to the watch body", s.fields.Timestamp, idx))
			}
		}
	}
	return raw.Hits.Total.Value, raw.Aggregations, nil
}

// RawSearchSamples fetches up to n (clamped ≤5) matching documents
// for the watch's query and returns one single-line ≤200-char summary
// per hit ("<ts> [<service>] <message>", newest first) — the
// notification examples embedded at FIRE time (Faz-2, ES Watcher
// parity: the watcher's own actions interpolate ctx.payload.hits).
// Failures are recorded for /admin/elastic but the CALLER treats them
// soft: a broken sample fetch must never block the fire itself.
func (s *ESStore) RawSearchSamples(ctx context.Context, indicesIn []string, body json.RawMessage, n int) ([]string, error) {
	guarded, err := injectRawSampleGuards(body, s.fields.Timestamp, esTimeoutFromEnv("10s"), n,
		[]string{s.fields.Timestamp, s.fields.Body, s.fields.Service})
	if err != nil {
		return nil, err
	}
	idx := s.watcherIndices(indicesIn)

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
		return nil, s.recordQueryError("watcher samples", idx, guarded, 0, fmt.Errorf("ES sample search: %w", err))
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, s.recordQueryError("watcher samples", idx, guarded, res.StatusCode,
			parseESError("watcher samples", res, strings.Join(idx, ",")))
	}
	var raw struct {
		Hits struct {
			Hits []struct {
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	// json.Number keeps numeric timestamps readable (epoch millis
	// would otherwise stringify as scientific notation).
	dec := json.NewDecoder(res.Body)
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode ES sample response: %w", err)
	}
	lines := make([]string, 0, len(raw.Hits.Hits))
	for _, h := range raw.Hits.Hits {
		if line := watcherSampleLine(h.Source, s.fields.Timestamp, s.fields.Service, s.fields.Body); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// watcherSampleLine flattens one hit's _source into the summary line:
// "<ts> [<service>] <message>", empty parts omitted, all whitespace
// runs collapsed to single spaces, truncated to 200 runes with an
// ellipsis. Pure — table-tested.
func watcherSampleLine(src map[string]any, tsField, svcField, msgField string) string {
	parts := make([]string, 0, 3)
	if ts := sourceScalar(src, tsField); ts != "" {
		parts = append(parts, ts)
	}
	if svc := sourceScalar(src, svcField); svc != "" {
		parts = append(parts, "["+svc+"]")
	}
	if msg := sourceScalar(src, msgField); msg != "" {
		parts = append(parts, msg)
	}
	line := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	const maxRunes = 200
	if r := []rune(line); len(r) > maxRunes {
		line = string(r[:maxRunes-1]) + "…"
	}
	return line
}

// sourceScalar resolves a possibly-dotted field path inside a decoded
// _source document: the flat dotted key wins (Filebeat-style
// pipelines index "service.name" literally), then the nested object
// walk. Only scalars stringify — composites return "" (a summary
// line must not dump structures).
func sourceScalar(src map[string]any, path string) string {
	if len(src) == 0 || path == "" {
		return ""
	}
	if v, ok := src[path]; ok {
		return scalarString(v)
	}
	head, rest, found := strings.Cut(path, ".")
	if !found {
		return ""
	}
	if sub, ok := src[head].(map[string]any); ok {
		return sourceScalar(sub, rest)
	}
	return ""
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}
