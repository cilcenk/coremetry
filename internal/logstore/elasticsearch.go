package logstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// ESConfig is the operator-supplied connection + field-mapping spec for
// an external Elasticsearch cluster. Field paths default to OTel-spec
// names — most ECS / OTel-shipped indices already use these.
type ESConfig struct {
	Addresses []string
	Username  string
	Password  string
	// APIKey is the base64 "id:api_key" string — the `encoded` field
	// returned by POST /_security/api_key. Takes precedence over basic
	// auth: if both APIKey and Username are set, the underlying client
	// sends an Authorization: ApiKey ... header and ignores user/pass.
	APIKey             string
	InsecureSkipVerify bool

	Index string // e.g. "logs-*" — supports glob/data-stream patterns

	// Field paths inside each ES document. Override per-deployment via
	// config so any shipping pipeline (Filebeat, Logstash, OTel
	// Collector → ES exporter) can be queried without re-indexing.
	Fields ESFieldMap
}

type ESFieldMap struct {
	Timestamp  string // default "@timestamp"
	TraceID    string // default "trace.id"
	SpanID     string // default "span.id"
	Service    string // default "service.name"
	Body       string // default "message"
	SeverityNo string // numeric, default "" (skip if absent)
	SeverityTx string // text, default "log.level"
}

func (c *ESConfig) defaults() {
	if c.Index == "" {
		c.Index = "logs-*"
	}
	if c.Fields.Timestamp == "" {
		c.Fields.Timestamp = "@timestamp"
	}
	if c.Fields.TraceID == "" {
		c.Fields.TraceID = "trace.id"
	}
	if c.Fields.SpanID == "" {
		c.Fields.SpanID = "span.id"
	}
	if c.Fields.Service == "" {
		c.Fields.Service = "service.name"
	}
	if c.Fields.Body == "" {
		c.Fields.Body = "message"
	}
	if c.Fields.SeverityTx == "" {
		c.Fields.SeverityTx = "log.level"
	}
}

// ESStore implements the LogStore Search interface against an external
// Elasticsearch cluster. Read-only — Coremetry never writes to ES.
type ESStore struct {
	cli    *elasticsearch.Client
	cfg    ESConfig
	fields ESFieldMap
}

func NewES(cfg ESConfig) (*ESStore, error) {
	cfg.defaults()

	transport := &http.Transport{}
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	cli, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: cfg.Addresses,
		Username:  cfg.Username,
		Password:  cfg.Password,
		APIKey:    cfg.APIKey,
		Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("create ES client: %w", err)
	}
	// Ping early so a misconfigured ES surfaces at startup rather than
	// at the first user query. 5s budget — ES clusters under load can
	// be slow but not seconds-slow on a no-op endpoint.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Info(cli.Info.WithContext(ctx)); err != nil {
		return nil, fmt.Errorf("ES ping: %w", err)
	}
	return &ESStore{cli: cli, cfg: cfg, fields: cfg.Fields}, nil
}

func (s *ESStore) Backend() string { return "elasticsearch" }

// ListFields returns the searchable field paths discovered from
// the configured index's mapping. Used by the v0.5.135 search-
// autocomplete affordance on /logs so the operator sees what
// they can filter by (level / service.name / k8s.namespace /
// trace.id / …) without guessing.
//
// Only "keyword" / "text" / "long" / "date" / "boolean" leaves
// are returned — nested objects expand to dotted paths
// (a.b.c) so the result matches what query_string expects.
//
// Results are alphabetised; an empty cache for ~60s tolerates
// the typical fleet of operators loading /logs together.
func (s *ESStore) ListFields(ctx context.Context) ([]string, error) {
	req := esapi.IndicesGetMappingRequest{
		Index: []string{s.cfg.Index},
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, fmt.Errorf("get mapping: %s", res.String())
	}
	var body map[string]struct {
		Mappings struct {
			Properties map[string]any `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, idx := range body {
		walkProperties("", idx.Mappings.Properties, seen)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sortStrings(out)
	return out, nil
}

// walkProperties recurses into ES mapping properties to flatten
// nested types (object / nested) into dot paths. Skips internal
// fields starting with "_". Only emits leaf paths whose type
// looks searchable.
func walkProperties(prefix string, props map[string]any, out map[string]struct{}) {
	for name, raw := range props {
		if name == "" || name[0] == '_' {
			continue
		}
		m, _ := raw.(map[string]any)
		if m == nil {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if nested, ok := m["properties"].(map[string]any); ok {
			walkProperties(path, nested, out)
			continue
		}
		t, _ := m["type"].(string)
		switch t {
		case "keyword", "text", "long", "integer", "short",
			"double", "float", "date", "boolean", "ip":
			out[path] = struct{}{}
		}
	}
}

// SimilarTrace is one trace bucket returned by SimilarTraces —
// the bucketing is done server-side via a terms aggregation on
// trace.id over a more_like_this query. Count is "how many
// log lines from this trace matched", a rough relevance proxy.
type SimilarTrace struct {
	TraceID string `json:"traceId"`
	Count   uint64 `json:"count"`
}

// SimilarTraces returns the trace IDs whose logs contain text
// pattern-similar to the supplied seed string. Uses Elastic's
// more_like_this query for the similarity signal + a terms
// aggregation to bucket per trace.id. The aggregation cap stays
// modest (50 traces) because the UI renders these as a clickable
// list — past 50 entries the operator will narrow rather than
// scroll. Read-only against Elastic.
func (s *ESStore) SimilarTraces(ctx context.Context, seed string, limit int) ([]SimilarTrace, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if seed == "" {
		return []SimilarTrace{}, nil
	}
	body := map[string]any{
		"size": 0,
		"query": map[string]any{
			"more_like_this": map[string]any{
				"fields":         []string{s.fields.Body},
				"like":           seed,
				"min_term_freq":  1,
				"min_doc_freq":   1,
				"max_query_terms": 25,
			},
		},
		"aggs": map[string]any{
			"traces": map[string]any{
				"terms": map[string]any{
					"field": s.fields.TraceID,
					"size":  limit,
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, err
	}
	req := esapi.SearchRequest{
		Index: []string{s.cfg.Index},
		Body:  &buf,
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, fmt.Errorf("similar traces: %s", res.String())
	}
	var resp struct {
		Aggregations struct {
			Traces struct {
				Buckets []struct {
					Key      string `json:"key"`
					DocCount uint64 `json:"doc_count"`
				} `json:"buckets"`
			} `json:"traces"`
		} `json:"aggregations"`
	}
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return nil, err
	}
	out := make([]SimilarTrace, 0, len(resp.Aggregations.Traces.Buckets))
	for _, b := range resp.Aggregations.Traces.Buckets {
		if b.Key == "" {
			continue
		}
		out = append(out, SimilarTrace{TraceID: b.Key, Count: b.DocCount})
	}
	return out, nil
}

// SQLResult is the wire-shape returned by ExecSQL. Mirrors the
// AdminSql /api/admin/sql/query shape (columns/rows/tookMs) so
// the frontend table renderer is single-codepath.
type SQLResult struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	TookMs  int64    `json:"tookMs"`
}

// ExecSQL forwards a query to Elasticsearch's /_sql endpoint and
// returns the result in the same shape as the CH SQL playground.
// Read-only by definition — the Elastic SQL plugin doesn't expose
// DML; we still keep the safety net (first-token check + 30s
// fetch budget) so a typo can't dump a billion rows.
func (s *ESStore) ExecSQL(ctx context.Context, query string, fetchSize int) (*SQLResult, error) {
	if fetchSize <= 0 || fetchSize > 10000 {
		fetchSize = 1000
	}
	body := map[string]any{
		"query":      query,
		"fetch_size": fetchSize,
		// 30s request timeout server-side — the SPA wraps the
		// fetch with its own 60s AbortController on top.
		"request_timeout":  "30s",
		"page_timeout":     "30s",
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "/_sql?format=json", &buf)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	res, err := s.cli.Transport.Perform(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		// Try to surface the ES error message — usually a JSON
		// {"error":{"type":..., "reason":...}} blob the operator
		// can act on directly (column name typo, parse failure,
		// unsupported function).
		var body map[string]any
		_ = json.NewDecoder(res.Body).Decode(&body)
		if ej, ok := body["error"].(map[string]any); ok {
			if reason, ok := ej["reason"].(string); ok {
				return nil, fmt.Errorf("es-sql: %s", reason)
			}
		}
		return nil, fmt.Errorf("es-sql: %s", res.Status)
	}
	var resp struct {
		Columns []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"columns"`
		Rows [][]any `json:"rows"`
	}
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return nil, err
	}
	cols := make([]string, len(resp.Columns))
	for i, c := range resp.Columns {
		cols[i] = c.Name
	}
	if resp.Rows == nil {
		resp.Rows = [][]any{}
	}
	return &SQLResult{
		Columns: cols,
		Rows:    resp.Rows,
		TookMs:  time.Since(start).Milliseconds(),
	}, nil
}

func sortStrings(s []string) {
	// std sort.Strings imported lazily — avoid pulling sort in
	// the package if no other callsite needs it (small payoff
	// but the import list is already large here).
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// Ping checks ES cluster availability via the lightweight Info API
// (same endpoint NewES uses at startup). 2-second timeout enforced by
// the caller's context.
func (s *ESStore) Ping(ctx context.Context) error {
	res, err := s.cli.Info(s.cli.Info.WithContext(ctx))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("ES info: %s", res.Status())
	}
	return nil
}

func (s *ESStore) Search(ctx context.Context, f Filter) (*Page, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	from := f.Offset
	if from < 0 {
		from = 0
	}

	query := s.buildQuery(f)
	// `unmapped_type` makes the sort fail-safe: if the
	// configured timestamp field doesn't exist in the index
	// mapping (the shipping pipeline writes a differently-named
	// timestamp), ES would otherwise raise "No mapping found
	// for [@timestamp] in order to sort on" and return zero
	// hits even when the query body matched documents. Treating
	// the field as `date` makes docs without that field sort to
	// the end with null values rather than erroring the whole
	// search — operator sees the trace-scoped logs in best-
	// effort time order instead of a silent empty tab.
	body, err := json.Marshal(map[string]any{
		"query": query,
		"sort": []any{
			map[string]any{
				s.fields.Timestamp: map[string]any{
					"order":         "desc",
					"unmapped_type": "date",
				},
			},
		},
		"from":             from,
		"size":             limit,
		"track_total_hits": true,
	})
	if err != nil {
		return nil, err
	}

	tru := true
	req := esapi.SearchRequest{
		Index: []string{s.cfg.Index},
		Body:  bytes.NewReader(body),
		// Treat "no matching index" / "one shard unavailable" as 0
		// hits instead of 404. Without these, an operator pointing
		// the read backend at a freshly-provisioned ES cluster
		// (no logs shipped yet) sees a 404 in the UI and assumes
		// Coremetry is broken — when really ES just has nothing
		// to search. Kibana applies the same defaults for the
		// Discover view.
		AllowNoIndices:    &tru,
		IgnoreUnavailable: &tru,
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		return nil, fmt.Errorf("ES search: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, parseESError("search", res, s.cfg.Index)
	}

	var raw struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
			Hits []struct {
				ID     string         `json:"_id"`
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode ES response: %w", err)
	}

	out := make([]*LogRecord, 0, len(raw.Hits.Hits))
	for _, h := range raw.Hits.Hits {
		out = append(out, s.mapHit(h.ID, h.Source))
	}
	// Diagnostic: when a trace/span ID search returns zero
	// hits, log the exact request body + index hit. The
	// operator can grep server logs for "[es-debug]" to see
	// what Coremetry actually asked for vs what direct curl
	// returns — pinpointing field-shape / format mismatches
	// without us blindly shipping more candidate field names.
	// Only logs the empty-result case so steady-state traffic
	// stays quiet.
	if raw.Hits.Total.Value == 0 && (f.TraceID != "" || f.SpanID != "") {
		log.Printf("[es-debug] zero hits for trace_id=%q span_id=%q index=%q query=%s",
			f.TraceID, f.SpanID, s.cfg.Index, string(body))
	}
	return &Page{Total: raw.Hits.Total.Value, Logs: out}, nil
}

// Histogram runs a date_histogram aggregation against ES,
// optionally split by a terms aggregation on `groupBy`. The
// fields used for splitting map to the configured ESFieldMap so
// custom shipping pipelines (Vector, Filebeat, OTel ECS mode)
// don't need the operator to re-index. Single round-trip; ES
// handles bucketing server-side and we just stitch the response.
func (s *ESStore) Histogram(ctx context.Context, f Filter, bucketSec int, groupBy string) ([]LogSeries, error) {
	if bucketSec <= 0 {
		bucketSec = 30
	}
	groupField := ""
	switch groupBy {
	case "service":
		groupField = s.fields.Service + ".keyword"
	case "severity":
		groupField = s.fields.SeverityTx + ".keyword"
	}

	dateAgg := map[string]any{
		"date_histogram": map[string]any{
			"field":          s.fields.Timestamp,
			"fixed_interval": fmt.Sprintf("%ds", bucketSec),
			"min_doc_count":  0,
		},
	}
	var aggs map[string]any
	if groupField != "" {
		aggs = map[string]any{
			"groups": map[string]any{
				"terms": map[string]any{
					"field": groupField,
					"size":  20,
				},
				"aggs": map[string]any{"buckets": dateAgg},
			},
		}
	} else {
		aggs = map[string]any{"buckets": dateAgg}
	}

	body, err := json.Marshal(map[string]any{
		"size":  0,
		"query": s.buildQuery(f),
		"aggs":  aggs,
	})
	if err != nil {
		return nil, err
	}
	tru := true
	req := esapi.SearchRequest{
		Index: []string{s.cfg.Index},
		Body:  bytes.NewReader(body),
		// Same forgiveness as Search — no matching index → 0
		// buckets rather than a 404. Keeps the panel rendering
		// "no data" instead of a scary error toast.
		AllowNoIndices:    &tru,
		IgnoreUnavailable: &tru,
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		return nil, fmt.Errorf("ES histogram: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, parseESError("histogram", res, s.cfg.Index)
	}

	var raw struct {
		Aggregations map[string]any `json:"aggregations"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode ES histogram: %w", err)
	}

	parseBuckets := func(a any) []LogPoint {
		root, ok := a.(map[string]any)
		if !ok {
			return nil
		}
		bs, ok := root["buckets"].([]any)
		if !ok {
			return nil
		}
		pts := make([]LogPoint, 0, len(bs))
		for _, b := range bs {
			bm, _ := b.(map[string]any)
			tMs, _ := bm["key"].(float64)
			cnt, _ := bm["doc_count"].(float64)
			pts = append(pts, LogPoint{T: int64(tMs) * int64(time.Millisecond), V: int64(cnt)})
		}
		return pts
	}

	if groupField == "" {
		return []LogSeries{{Name: "_total", Points: parseBuckets(raw.Aggregations["buckets"])}}, nil
	}
	groups, _ := raw.Aggregations["groups"].(map[string]any)
	bs, _ := groups["buckets"].([]any)
	out := make([]LogSeries, 0, len(bs))
	for _, b := range bs {
		bm, _ := b.(map[string]any)
		name, _ := bm["key"].(string)
		out = append(out, LogSeries{Name: name, Points: parseBuckets(bm["buckets"])})
	}
	return out, nil
}

// buildQuery constructs the ES bool/must query corresponding to a Filter.
func (s *ESStore) buildQuery(f Filter) map[string]any {
	must := []any{}

	// Time range
	if !f.From.IsZero() || !f.To.IsZero() {
		rng := map[string]any{}
		if !f.From.IsZero() {
			rng["gte"] = f.From.UTC().Format(time.RFC3339Nano)
		}
		if !f.To.IsZero() {
			rng["lte"] = f.To.UTC().Format(time.RFC3339Nano)
		}
		must = append(must, map[string]any{
			"range": map[string]any{s.fields.Timestamp: rng},
		})
	}
	// trace_id / span_id correlation — single-document keyword
	// lookup, the hottest predicate. Different shipping
	// pipelines settle on different field names:
	//
	//   OTel ECS mode      → "trace.id"    (nested {trace:{id:…}})
	//   OTel default       → "TraceId"     (PascalCase from attribs)
	//   Filebeat / vector  → "trace_id"    (snake_case)
	//   Custom JSON parsers → "traceId"    (camelCase)
	//
	// We don't know which one applies until we see a doc, and
	// asking every operator to configure the field name is a
	// papercut. Cheap solution: bool/should over all four
	// common shapes. ES short-circuits term lookups against
	// non-existent fields (no terms in the inverted index for
	// that path → match nothing for that branch), so the
	// overhead vs a single-field term is negligible. The
	// `s.fields.TraceID` configured override is included too so
	// a fully custom field still works.
	if f.TraceID != "" {
		must = append(must, traceTermsAny(s.fields.TraceID, s.fields.Body,
			strings.ToLower(f.TraceID), "trace"))
	}
	if f.SpanID != "" {
		must = append(must, traceTermsAny(s.fields.SpanID, s.fields.Body,
			strings.ToLower(f.SpanID), "span"))
	}
	if f.Service != "" {
		must = append(must, map[string]any{
			"term": map[string]any{s.fields.Service: f.Service},
		})
	}
	if f.Search != "" {
		// Free-text search box → Lucene query_string. Plain words still
		// work as a body match (default_field), but the user can also
		// write structured queries like:
		//
		//     level:error
		//     service.name:java-demo AND NOT message:health
		//     trace.id:c9ea*
		//
		// AllowLeadingWildcard is OFF for performance — leading * means
		// scanning every term in the inverted index, which is multi-second
		// at our scale. AllowLeadingWildcard can be re-enabled via the
		// field map if an operator really needs it.
		//
		// case_insensitive lets `level:error` match docs stored as
		// `ERROR` on a keyword field. Without this flag, keyword
		// fields are exact-match — the historical "level:error
		// returns nothing" bug on indices that store severities
		// uppercase.
		must = append(must, map[string]any{
			"query_string": map[string]any{
				"query":                  s.expandShorthand(f.Search),
				"default_field":          s.fields.Body,
				"default_operator":       "AND",
				"allow_leading_wildcard": false,
				"case_insensitive":       true,
				"lenient":                true, // tolerate type mismatches (e.g. searching numeric column with a word)
			},
		})
	}
	if f.SeverityMin > 0 && s.fields.SeverityNo != "" {
		must = append(must, map[string]any{
			"range": map[string]any{s.fields.SeverityNo: map[string]any{"gte": f.SeverityMin}},
		})
	}

	if len(must) == 0 {
		return map[string]any{"match_all": map[string]any{}}
	}
	return map[string]any{"bool": map[string]any{"must": must}}
}

// shorthandRe matches `<token>:<value>` (quoted or unquoted) in a Lucene
// query string, anchored to start-of-string, whitespace, or an opening
// paren so we don't rewrite a colon that's part of a value.
var shorthandRe = regexp.MustCompile(
	`(?i)(^|[\s(])(level|severity|service|trace|trace_id|traceid|span|span_id|spanid|message|body|pod|namespace|cluster|host):("[^"]+"|\S+)`)

// expandShorthand rewrites common short field names to multi-shape
// OR groups so the same query works against any shipping pipeline.
// `level:error` becomes
// `(log.level:error OR level:error OR severity:error OR severity_text:error OR SeverityText:error)`
// — the same fallback chain mapHit already uses when reading values
// back. Without this, an operator typing the obvious shorthand sees
// zero hits even though the index does contain matching docs under a
// differently-shaped field name.
func (s *ESStore) expandShorthand(q string) string {
	aliases := map[string][]string{
		"level":     {s.fields.SeverityTx, "level", "severity", "severity_text", "SeverityText"},
		"severity":  {s.fields.SeverityTx, "level", "severity", "severity_text", "SeverityText"},
		"service":   {s.fields.Service, "service.name", "service_name", "serviceName", "ServiceName"},
		"trace":     {s.fields.TraceID, "trace.id", "trace_id", "traceId", "TraceId"},
		"trace_id":  {s.fields.TraceID, "trace.id", "trace_id", "traceId", "TraceId"},
		"traceid":   {s.fields.TraceID, "trace.id", "trace_id", "traceId", "TraceId"},
		"span":      {s.fields.SpanID, "span.id", "span_id", "spanId", "SpanId"},
		"span_id":   {s.fields.SpanID, "span.id", "span_id", "spanId", "SpanId"},
		"spanid":    {s.fields.SpanID, "span.id", "span_id", "spanId", "SpanId"},
		"message":   {s.fields.Body, "message", "Body", "body", "log.message"},
		"body":      {s.fields.Body, "message", "Body", "body", "log.message"},
		"pod":       {"kubernetes.pod_name", "kubernetes.pod.name", "k8s.pod.name", "resource.k8s.pod.name", "pod_name", "pod"},
		"namespace": {"kubernetes.namespace_name", "kubernetes.namespace", "k8s.namespace.name", "resource.k8s.namespace.name", "namespace"},
		"cluster":   {"openshift.labels.cluster", "openshift.cluster.name", "kubernetes.cluster.name", "k8s.cluster.name", "resource.k8s.cluster.name", "kubernetes.cluster_name", "cluster"},
		"host":      {"host.name", "host.hostname", "resource.host.name", "hostname", "host"},
	}
	return shorthandRe.ReplaceAllStringFunc(q, func(m string) string {
		sub := shorthandRe.FindStringSubmatch(m)
		if len(sub) != 4 {
			return m
		}
		prefix, key, val := sub[1], strings.ToLower(sub[2]), sub[3]
		fields, ok := aliases[key]
		if !ok {
			return m
		}
		// Dedupe — the configured override and the hard-coded
		// fallback often collide (e.g. "log.level" appears twice).
		seen := map[string]struct{}{}
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			if f == "" {
				continue
			}
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			parts = append(parts, f+":"+val)
		}
		if len(parts) == 0 {
			return m
		}
		return prefix + "(" + strings.Join(parts, " OR ") + ")"
	})
}

// mapHit walks the source document using the configured field paths
// (which can be dotted, e.g. "trace.id") and pulls each value into the
// canonical LogRecord shape. Anything not under a known path falls into
// the Attributes map so it's still inspectable in the UI.
func (s *ESStore) mapHit(id string, src map[string]any) *LogRecord {
	r := &LogRecord{
		Attributes:         map[string]string{},
		ResourceAttributes: map[string]string{},
	}

	if v := readPath(src, s.fields.Timestamp); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			r.Timestamp = t.UnixNano()
		}
	}
	r.TraceID = readPathAny(src, s.fields.TraceID,
		"trace.id", "trace_id", "traceId", "TraceId")
	r.SpanID = readPathAny(src, s.fields.SpanID,
		"span.id", "span_id", "spanId", "SpanId")
	// Service column on the Logs page kept coming up blank for
	// operators whose pipelines ship a flat `service_name`
	// instead of the OTel ECS `service.name`. Read the
	// configured path first; if empty, walk the four common
	// shapes any shipper might emit.
	r.ServiceName = readPathAny(src, s.fields.Service,
		"service.name", "service_name", "serviceName", "ServiceName")
	r.Body = readPathAny(src, s.fields.Body,
		"message", "Body", "body", "log.message", "log")
	r.SeverityText = readPathAny(src, s.fields.SeverityTx,
		"log.level", "level", "severity", "severity_text", "SeverityText")

	if s.fields.SeverityNo != "" {
		if v := readPath(src, s.fields.SeverityNo); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 24 {
				r.Severity = uint8(n)
			}
		}
	}
	// ES doc IDs are strings; convert to a stable hash-ish int64 so the
	// frontend's existing numeric ID assumption still holds. Lossy but
	// the ID is only used for React keys, never as a lookup.
	r.ID = stringToInt64ID(id)

	// Stash everything else into Attributes so the user can still see
	// host name, custom fields, etc. Skip the canonical fields we
	// already extracted to avoid duplication.
	skip := map[string]bool{
		s.fields.Timestamp: true, s.fields.TraceID: true, s.fields.SpanID: true,
		s.fields.Service: true, s.fields.Body: true,
		s.fields.SeverityTx: true, s.fields.SeverityNo: true,
	}
	flatten("", src, r.Attributes, skip)
	return r
}

// readPathAny tries the configured `primary` path first, then
// walks `fallbacks` in order until one yields a non-empty
// value. Lets the LogRecord mapper survive shipping pipelines
// that emit a different field shape than the OTel ECS default
// (snake_case service_name, PascalCase TraceId, etc.) without
// asking every operator to override the Fields map by hand.
//
// Empty primary still works — operator can leave it unset
// and we'll hit the fallback list, picking whichever shape
// their docs actually carry.
func readPathAny(src map[string]any, primary string, fallbacks ...string) string {
	if v := readPath(src, primary); v != "" {
		return v
	}
	for _, p := range fallbacks {
		if p == primary {
			continue
		}
		if v := readPath(src, p); v != "" {
			return v
		}
	}
	return ""
}

// readPath fetches a value from a map by dotted path, returning the
// stringified value or "" if missing. Handles nested maps for ECS-style
// docs ({trace: {id: "..."}}).
func readPath(src map[string]any, path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(path, ".")
	var cur any = src
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = m[p]
		if !ok {
			return ""
		}
	}
	return toString(cur)
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		// JSON numbers parse as float64; strip the .0 for integers.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// flatten walks a nested map and writes "a.b.c=value" pairs into dst.
// Skip-set values are pre-extracted into the canonical LogRecord fields.
func flatten(prefix string, src map[string]any, dst map[string]string, skip map[string]bool) {
	for k, v := range src {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		if skip[full] {
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			flatten(full, nested, dst, skip)
			continue
		}
		dst[full] = toString(v)
	}
}

// traceTermsAny builds a bool/should clause that matches the
// given hex id against every common field name shape used by
// log shippers — plus a match against the message body for
// pipelines that don't extract the ID into a structured field
// (the ID is just embedded in the line text like
// "[trace_id=abc] processing..."). kind is "trace" or "span";
// we derive the four common spellings from it.
//
// Returns a shape ready to plug into a `must` array. One non-
// empty branch wins; non-existent fields contribute nothing
// to score and short-circuit cheaply against the inverted
// index. The body match uses `match` (analyzed) so the
// hex token gets picked out of brackets / punctuation by
// the standard analyzer.
func traceTermsAny(configured, body, value, kind string) map[string]any {
	// Build the candidate set with the configured field first
	// (operator's explicit override wins on conflict) and the
	// four common shapes after. Dedupe via a tiny set so we
	// don't emit duplicate clauses when the configured value
	// already equals one of the defaults.
	title := strings.ToUpper(kind[:1]) + kind[1:] // "trace" → "Trace"
	candidates := []string{
		configured,
		kind + ".id", // trace.id / span.id  (ECS)
		kind + "_id", // trace_id / span_id  (snake)
		kind + "Id",  // traceId / spanId    (camel)
		title + "Id", // TraceId / SpanId    (OTel default)
	}
	seen := map[string]bool{}
	should := make([]any, 0, len(candidates)+1)
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		should = append(should, map[string]any{
			"term": map[string]any{c: value},
		})
	}
	if body != "" {
		// Body match — catches "[trace_id=abc123] msg…" lines
		// where the shipping pipeline never split the ID into
		// its own field. Analyzed match so the standard analyzer
		// tokenises around brackets / equals / spaces and the
		// hex id lands as its own term.
		should = append(should, map[string]any{
			"match": map[string]any{body: value},
		})
	}
	return map[string]any{
		"bool": map[string]any{
			"should":               should,
			"minimum_should_match": 1,
		},
	}
}

// parseESError pulls a human-readable reason from a non-2xx ES
// response. ES wraps real failures in an envelope like
//
//	{"error":{"type":"index_not_found_exception",
//	          "reason":"no such index [logs-otel-default]"},
//	 "status":404}
//
// res.String() dumps the lot — the type is buried mid-line — so
// we surface the reason cleanly. For 404 specifically we append
// a "check the index pattern in your config" hint because that's
// the canonical operator footgun (typo in the configured index
// name, or pointing at a brand-new ES that hasn't seen logs yet).
//
// Falls back to res.Status() + raw body when the envelope shape
// isn't what we expect (older ES, transport-level errors).
func parseESError(op string, res *esapi.Response, configuredIndex string) error {
	var parsed struct {
		Error struct {
			Type      string `json:"type"`
			Reason    string `json:"reason"`
			IndexUUID string `json:"index_uuid"`
			Index     string `json:"index"`
		} `json:"error"`
		Status int `json:"status"`
	}
	body, _ := io.ReadAll(res.Body)
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Reason != "" {
		switch {
		case res.StatusCode == 404 && parsed.Error.Type == "index_not_found_exception":
			return fmt.Errorf(
				"ES %s: index %q not found — check `logs.elasticsearch.index` in your config (current value %q). Run `curl <es>/_cat/indices?v=true` against your cluster to see what's available",
				op, parsed.Error.Index, configuredIndex)
		case res.StatusCode == 401 || res.StatusCode == 403:
			return fmt.Errorf(
				"ES %s: %s (status %d) — check API key / username + password and that the credential has read access to %q",
				op, parsed.Error.Reason, res.StatusCode, configuredIndex)
		default:
			return fmt.Errorf("ES %s %d: %s (%s)",
				op, res.StatusCode, parsed.Error.Reason, parsed.Error.Type)
		}
	}
	return fmt.Errorf("ES %s %s: %s", op, res.Status(), string(body))
}

// stringToInt64ID is a 64-bit FNV-1a so React keys stay numeric. Not a
// crypto hash; collisions are fine because the ID is never used for
// lookups, only for stable list-rendering.
func stringToInt64ID(s string) int64 {
	const (
		offset64 uint64 = 14695981039346656037
		prime64  uint64 = 1099511628211
	)
	h := offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return int64(h >> 1)
}
