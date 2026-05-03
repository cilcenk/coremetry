// Tempo-compatible HTTP API. Lets Grafana add Qmetry as a Tempo datasource:
// only the endpoints Grafana actually calls during search and trace view are
// implemented (echo, search, search/tags, search/tag/{name}/values, traces/{id}).
//
// Both the v1 and v2 paths are wired so older / newer Grafana versions work.
//
// Spec reference: https://grafana.com/docs/tempo/latest/api_docs/
package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/cenk/qmetry/internal/chstore"
)

// RegisterTempoRoutes wires Tempo-compatible endpoints onto an existing mux.
// All routes live under /tempo/* so they don't collide with our own /api/*.
// Grafana's datasource config should point its URL at:  http://<qmetry>/tempo
func (s *Server) registerTempoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /tempo/api/echo",                    s.tempoEcho)
	mux.HandleFunc("GET /tempo/ready",                       s.tempoEcho)
	mux.HandleFunc("GET /tempo/api/search",                  s.tempoSearch)
	mux.HandleFunc("GET /tempo/api/search/tags",             s.tempoTags)
	mux.HandleFunc("GET /tempo/api/v2/search/tags",          s.tempoTagsV2)
	mux.HandleFunc("GET /tempo/api/search/tag/{name}/values", s.tempoTagValues)
	mux.HandleFunc("GET /tempo/api/v2/search/tag/{name}/values", s.tempoTagValuesV2)
	mux.HandleFunc("GET /tempo/api/traces/{id}",             s.tempoTrace)
	mux.HandleFunc("GET /tempo/api/v2/traces/{id}",          s.tempoTrace)
}

// ── /api/echo — Grafana datasource health check ──────────────────────────────
func (s *Server) tempoEcho(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("echo"))
}

// ── /api/search — Grafana query the trace list ───────────────────────────────
//
// Supported query parameters:
//   q              — TraceQL (we accept and ignore unsupported features)
//   tags           — logfmt-encoded `key=value` pairs (legacy)
//   minDuration    — e.g. "100ms"
//   maxDuration    — e.g. "1s"
//   limit          — int
//   start, end     — unix seconds (Tempo) or RFC3339
//
// Response: { "traces": [{ traceID, rootServiceName, rootTraceName, startTimeUnixNano, durationMs }] }
func (s *Server) tempoSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := parseInt(q.Get("limit"), 20)
	startSec, _ := strconv.ParseInt(q.Get("start"), 10, 64)
	endSec, _   := strconv.ParseInt(q.Get("end"),   10, 64)
	var from, to time.Time
	if startSec > 0 { from = time.Unix(startSec, 0) }
	if endSec   > 0 { to   = time.Unix(endSec,   0) }

	minMs := durationToMs(q.Get("minDuration"))
	maxMs := durationToMs(q.Get("maxDuration"))

	// Translate logfmt tag pairs into our FilterExpr list.
	filters := parseLogfmtTags(q.Get("tags"))

	// Lift TraceQL `{ resource.X = "Y" && span.Z = "W" }` into FilterExprs.
	// We don't try to parse the full grammar — just simple comparisons.
	tqlFilters, tqlService, tqlName := parseSimpleTraceQL(q.Get("q"))
	filters = append(filters, tqlFilters...)

	tracesResp, _, err := s.store.GetTraces(r.Context(), chstore.TraceFilter{
		From: from, To: to, MinMs: minMs, MaxMs: maxMs,
		Service: tqlService, Search: tqlName,
		Filters: filters,
		Limit: limit, Sort: "time", Order: "desc",
	})
	if err != nil { writeErr(w, err); return }

	type tempoTrace struct {
		TraceID           string `json:"traceID"`
		RootServiceName   string `json:"rootServiceName"`
		RootTraceName     string `json:"rootTraceName"`
		StartTimeUnixNano string `json:"startTimeUnixNano"` // Tempo wants strings
		DurationMs        int    `json:"durationMs"`
	}
	out := make([]tempoTrace, 0, len(tracesResp))
	for _, t := range tracesResp {
		out = append(out, tempoTrace{
			TraceID:           t.TraceID,
			RootServiceName:   t.ServiceName,
			RootTraceName:     t.RootName,
			StartTimeUnixNano: strconv.FormatInt(t.StartTime, 10),
			DurationMs:        int(t.DurationMs),
		})
	}
	writeJSON(w, map[string]interface{}{"traces": out})
}

// ── /api/search/tags — distinct attribute keys ───────────────────────────────
func (s *Server) tempoTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.collectTagNames(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, map[string]interface{}{"tagNames": tags})
}

// /api/v2/search/tags — same data, scope-grouped (resource / span)
func (s *Server) tempoTagsV2(w http.ResponseWriter, r *http.Request) {
	span, res, err := s.collectTagNamesScoped(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, map[string]interface{}{
		"scopes": []map[string]interface{}{
			{"name": "resource", "tags": res},
			{"name": "span",     "tags": span},
		},
	})
}

func (s *Server) tempoTagValues(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	vals, err := s.collectTagValues(r.Context(), name)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, map[string]interface{}{"tagValues": vals})
}

func (s *Server) tempoTagValuesV2(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	vals, err := s.collectTagValues(r.Context(), name)
	if err != nil { writeErr(w, err); return }
	type v struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	wrapped := make([]v, len(vals))
	for i, val := range vals { wrapped[i] = v{"string", val} }
	writeJSON(w, map[string]interface{}{"tagValues": wrapped})
}

// ── /api/traces/{id} — OTLP TracesData (binary protobuf) ─────────────────────
//
// Grafana's Tempo plugin expects this endpoint to return an OTLP TracesData
// proto in binary form, NOT JSON. (Tempo natively serves protobuf and the
// plugin calls proto.Unmarshal on the body.) We honour Accept negotiation:
//   • application/json                 → JSON envelope (debugging / our UI)
//   • application/protobuf (default)   → real OTLP binary
func (s *Server) tempoTrace(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	spans, err := s.store.GetTrace(r.Context(), id)
	if err != nil { writeErr(w, err); return }
	if len(spans) == 0 {
		http.NotFound(w, r); return
	}

	td := buildTracesData(spans)

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, td)
		return
	}
	body, err := proto.Marshal(td)
	if err != nil { writeErr(w, err); return }
	w.Header().Set("Content-Type", "application/protobuf")
	w.Write(body)
}

// buildTracesData turns our flat span rows into an OTLP TracesData tree
// grouped by service. The hex IDs we store get decoded back to bytes —
// the proto wire format requires raw `bytes`, not strings.
func buildTracesData(spans []chstore.SpanRow) *tracepb.TracesData {
	bySvc := map[string][]chstore.SpanRow{}
	for _, sp := range spans {
		bySvc[sp.ServiceName] = append(bySvc[sp.ServiceName], sp)
	}

	makeAttrs := func(m map[string]string) []*commonpb.KeyValue {
		out := make([]*commonpb.KeyValue, 0, len(m))
		for k, v := range m {
			out = append(out, &commonpb.KeyValue{
				Key:   k,
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}},
			})
		}
		return out
	}

	td := &tracepb.TracesData{}
	for svcName, ss := range bySvc {
		resAttrs := map[string]string{"service.name": svcName}
		if ss[0].HostName != "" {
			resAttrs["host.name"] = ss[0].HostName
		}
		for k, v := range ss[0].ResourceAttributes {
			resAttrs[k] = v
		}

		spansPB := make([]*tracepb.Span, 0, len(ss))
		for _, sp := range ss {
			pb := &tracepb.Span{
				TraceId:           hexBytes(sp.TraceID),
				SpanId:            hexBytes(sp.SpanID),
				Name:              sp.Name,
				Kind:              tracepb.Span_SpanKind(spanKindNum(sp.Kind)),
				StartTimeUnixNano: uint64(sp.StartTime),
				EndTimeUnixNano:   uint64(sp.EndTime),
				Status: &tracepb.Status{
					Code:    tracepb.Status_StatusCode(statusCodeNum(sp.StatusCode)),
					Message: sp.StatusMessage,
				},
				Attributes: makeAttrs(sp.Attributes),
			}
			if sp.ParentSpanID != "" {
				pb.ParentSpanId = hexBytes(sp.ParentSpanID)
			}
			spansPB = append(spansPB, pb)
		}

		td.ResourceSpans = append(td.ResourceSpans, &tracepb.ResourceSpans{
			Resource:   &resourcepb.Resource{Attributes: makeAttrs(resAttrs)},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "qmetry"},
				Spans: spansPB,
			}},
		})
	}
	return td
}

// hexBytes decodes a hex string back into its raw byte form. Empty / invalid
// input returns nil so the proto field stays unset.
func hexBytes(hexStr string) []byte {
	if hexStr == "" { return nil }
	b, err := hex.DecodeString(hexStr)
	if err != nil { return nil }
	return b
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func (s *Server) collectTagNames(ctx context.Context) ([]string, error) {
	rows, err := s.store.Conn().Query(ctx, `
		SELECT DISTINCT k FROM (
		    SELECT arrayJoin(attr_keys) AS k FROM spans WHERE time > now() - INTERVAL 24 HOUR
		    UNION ALL
		    SELECT arrayJoin(res_keys)  AS k FROM spans WHERE time > now() - INTERVAL 24 HOUR
		) ORDER BY k LIMIT 1000`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil { return nil, err }
		out = append(out, k)
	}
	// Always include well-known dimensions
	wellKnownTags := []string{"service.name", "name", "kind", "status", "http.method", "http.route", "http.status_code"}
	have := map[string]bool{}
	for _, t := range out { have[t] = true }
	for _, t := range wellKnownTags {
		if !have[t] { out = append(out, t) }
	}
	return out, nil
}

func (s *Server) collectTagNamesScoped(ctx context.Context) (span []string, res []string, err error) {
	rs, err := s.store.Conn().Query(ctx, `
		SELECT DISTINCT arrayJoin(attr_keys) AS k FROM spans
		WHERE time > now() - INTERVAL 24 HOUR LIMIT 1000`)
	if err != nil { return nil, nil, err }
	for rs.Next() { var k string; rs.Scan(&k); span = append(span, k) }
	rs.Close()

	rs, err = s.store.Conn().Query(ctx, `
		SELECT DISTINCT arrayJoin(res_keys) AS k FROM spans
		WHERE time > now() - INTERVAL 24 HOUR LIMIT 1000`)
	if err != nil { return nil, nil, err }
	for rs.Next() { var k string; rs.Scan(&k); res = append(res, k) }
	rs.Close()
	return
}

func (s *Server) collectTagValues(ctx context.Context, key string) ([]string, error) {
	// Map a small set of well-known tags to their dedicated columns.
	var sql string
	var args []any
	switch key {
	case "service.name", "service":
		sql = `SELECT DISTINCT service_name FROM spans WHERE time > now() - INTERVAL 24 HOUR ORDER BY service_name LIMIT 500`
	case "name":
		sql = `SELECT DISTINCT name FROM spans WHERE time > now() - INTERVAL 24 HOUR ORDER BY name LIMIT 500`
	case "kind":
		sql = `SELECT DISTINCT kind FROM spans WHERE time > now() - INTERVAL 24 HOUR ORDER BY kind LIMIT 50`
	case "status":
		sql = `SELECT DISTINCT status_code FROM spans WHERE time > now() - INTERVAL 24 HOUR ORDER BY status_code`
	default:
		// Generic attribute lookup (try span attrs first, fall back to resource).
		key2 := strings.TrimPrefix(strings.TrimPrefix(key, "span."), "resource.")
		sql = `
			SELECT DISTINCT v FROM (
			    SELECT attr_values[indexOf(attr_keys, ?)] AS v FROM spans
			    WHERE time > now() - INTERVAL 24 HOUR
			    UNION ALL
			    SELECT res_values[indexOf(res_keys, ?)] AS v FROM spans
			    WHERE time > now() - INTERVAL 24 HOUR
			) WHERE v != '' ORDER BY v LIMIT 500`
		args = []any{key2, key2}
	}
	rows, err := s.store.Conn().Query(ctx, sql, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil { return nil, err }
		if v != "" { out = append(out, v) }
	}
	return out, nil
}

// parseLogfmtTags converts Tempo's legacy `tags` parameter ("key=value foo=bar")
// into FilterExpr equality clauses.
func parseLogfmtTags(s string) []chstore.FilterExpr {
	if s == "" { return nil }
	var out []chstore.FilterExpr
	for _, pair := range strings.Fields(s) {
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 { continue }
		k, v := pair[:eq], strings.Trim(pair[eq+1:], `"`)
		out = append(out, chstore.FilterExpr{Key: k, Op: "=", Values: []string{v}})
	}
	return out
}

// parseSimpleTraceQL pulls equality clauses out of a TraceQL string like
//   { resource.service.name = "api" && span.http.status_code = 500 }
// Anything more advanced is silently ignored.
func parseSimpleTraceQL(q string) ([]chstore.FilterExpr, string, string) {
	if q == "" { return nil, "", "" }
	q = strings.TrimSpace(q)
	q = strings.TrimPrefix(q, "{"); q = strings.TrimSuffix(q, "}")
	q = strings.TrimSpace(q)

	parts := splitTopLevel(q, "&&")
	var out []chstore.FilterExpr
	var svc, name string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		eq := strings.Index(p, "=")
		if eq < 0 { continue }
		key := strings.TrimSpace(p[:eq])
		val := strings.Trim(strings.TrimSpace(p[eq+1:]), `"`)
		switch {
		case key == "resource.service.name" || key == "service.name" || key == ".service.name":
			svc = val
		case key == "name" || key == ".name" || key == "span.name":
			name = val
		default:
			out = append(out, chstore.FilterExpr{Key: strings.TrimPrefix(key, "."), Op: "=", Values: []string{val}})
		}
	}
	return out, svc, name
}

// splitTopLevel splits a string on `sep` ignoring occurrences inside quotes.
func splitTopLevel(s, sep string) []string {
	var out []string
	depth := 0
	inStr := false
	last := 0
	for i := 0; i < len(s)-len(sep)+1; i++ {
		c := s[i]
		if c == '"' { inStr = !inStr }
		if !inStr {
			if c == '(' || c == '{' { depth++ }
			if c == ')' || c == '}' { depth-- }
			if depth == 0 && s[i:i+len(sep)] == sep {
				out = append(out, s[last:i])
				last = i + len(sep)
				i += len(sep) - 1
			}
		}
	}
	out = append(out, s[last:])
	return out
}

// durationToMs parses Go-style durations ("100ms", "1s") into milliseconds.
func durationToMs(s string) float64 {
	if s == "" { return 0 }
	d, err := time.ParseDuration(s)
	if err != nil { return 0 }
	return float64(d.Milliseconds())
}

func spanKindNum(k string) int {
	switch k {
	case "server":   return 2
	case "client":   return 3
	case "producer": return 4
	case "consumer": return 5
	case "internal": return 1
	}
	return 0
}

func statusCodeNum(s string) int {
	switch s {
	case "ok":    return 1
	case "error": return 2
	}
	return 0
}

// silence unused import warnings in some build configurations
var _ = json.RawMessage{}
