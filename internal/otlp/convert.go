package otlp

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logscollpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/templater"
)

// ── Trace conversion ──────────────────────────────────────────────────────────

// ConvertTraces returns the span rows plus the span-link rows extracted from
// them (v0.8.329 — previously sp.Links was silently dropped, pivot-audit §2;
// mirrors how ConvertMetrics returns exemplars since v0.8.328). Link rows
// carry the OWNING span's identity (trace/span id, start time, service) — the
// forward key of the span_links table. The invalid-link gate (empty linked
// trace id) is NOT applied here: pure conversion, unit-testable; the
// Ingester's addSpanLink applies it so HTTP + gRPC share one gate + one set
// of counters (the v0.8.328 exemplar split, same reason).
func ConvertTraces(req *tracecollpb.ExportTraceServiceRequest) ([]*chstore.Span, []*chstore.SpanLinkRow) {
	var out []*chstore.Span
	var links []*chstore.SpanLinkRow
	for _, rs := range req.ResourceSpans {
		svcName, hostName, deployEnv := "unknown", "", ""
		resK, resV := attrsToArrays(nil)
		if rs.Resource != nil {
			svcName = attrStr(rs.Resource.Attributes, "service.name", "unknown")
			hostName = attrStr(rs.Resource.Attributes, "host.name", "")
			// deployment.environment.name is the CURRENT semconv key
			// (≥1.27 renamed it); deployment.environment is the legacy
			// spelling older SDKs still emit. Multi-name fallback chain
			// per the v0.5.471 cluster precedent — operator-reported
			// (v0.8.379): the test env emits only the new key, so
			// deploy_env (and every env facet/filter on top of it)
			// stayed empty.
			deployEnv = attrStr(rs.Resource.Attributes, "deployment.environment.name",
				attrStr(rs.Resource.Attributes, "deployment.environment", ""))
			resK, resV = attrsToArrays(rs.Resource.Attributes)
		}
		for _, ss := range rs.ScopeSpans {
			scopeName := ""
			if ss.Scope != nil {
				scopeName = ss.Scope.Name
			}
			for _, sp := range ss.Spans {
				row := convertSpan(sp, svcName, hostName, deployEnv, scopeName, resK, resV)
				out = append(out, row)
				links = appendSpanLinks(links, sp.Links, row)
			}
		}
	}
	return out, links
}

// appendSpanLinks converts one span's OTLP links into SpanLinkRows carrying
// the already-converted span row's identity (owning trace/span id, start
// time, service) — identity by construction, no re-derivation to drift.
func appendSpanLinks(dst []*chstore.SpanLinkRow, links []*tracepb.Span_Link, sp *chstore.Span) []*chstore.SpanLinkRow {
	for _, ln := range links {
		attrK, attrV := attrsToArrays(ln.Attributes)
		dst = append(dst, &chstore.SpanLinkRow{
			TraceID: sp.TraceID,
			SpanID:  sp.SpanID,
			// parentID (not hexID): a link target of nil bytes and one of
			// all-zero bytes are the same "no context" on different SDKs —
			// both must collapse to "" so the Ingester's invalid gate sees
			// them identically (the exemplar/parent_id precedent).
			LinkedTraceID: parentID(ln.TraceId),
			LinkedSpanID:  parentID(ln.SpanId),
			Time:          sp.Time,
			ServiceName:   sp.ServiceName,
			AttrKeys:      attrK,
			AttrVals:      attrV,
		})
	}
	return dst
}

func convertSpan(sp *tracepb.Span, svcName, hostName, deployEnv, scopeName string, resK, resV []string) *chstore.Span {
	statusCode := "unset"
	statusMsg := ""
	if sp.Status != nil {
		switch sp.Status.Code {
		case tracepb.Status_STATUS_CODE_OK:
			statusCode = "ok"
		case tracepb.Status_STATUS_CODE_ERROR:
			statusCode = "error"
		}
		statusMsg = sp.Status.Message
	}

	attrK, attrV := attrsToArrays(sp.Attributes)

	// Extract well-known attributes into dedicated columns
	dbSystem := attrStr(sp.Attributes, "db.system", "")
	dbStmt := attrStr(sp.Attributes, "db.statement", "")
	httpMethod := attrStr(sp.Attributes, "http.method", "")
	httpRoute := attrStr(sp.Attributes, "http.route", attrStr(sp.Attributes, "http.target", ""))
	httpStatus := uint16(attrInt(sp.Attributes, "http.status_code", 0))
	rpcSystem := attrStr(sp.Attributes, "rpc.system", "")
	rpcMethod := attrStr(sp.Attributes, "rpc.method", "")
	peerService := attrStr(sp.Attributes, "peer.service", "")
	msgSystem := attrStr(sp.Attributes, "messaging.system", "")

	kind := kindStr(sp.Kind)

	// Phase-2 #3 — only build + marshal the events JSON when the span actually
	// carries events. The empty-events majority stored "[]" at the cost of a
	// convertEvents slice alloc + a reflect-based json.Marshal per span; skip it
	// and store "" (the read sites tolerate empty — chstore aggregate.go /
	// repo.go guard the Unmarshal on a non-empty string).
	var events []byte
	if len(sp.Events) > 0 {
		events, _ = json.Marshal(convertEvents(sp.Events))
	}

	dur := int64(sp.EndTimeUnixNano) - int64(sp.StartTimeUnixNano)
	if dur < 0 {
		dur = 0
	}

	// op_group — pure, cheap, lock-free per-span operation-shape
	// normalizer (group_id rel A). Source priority: http.route >
	// DB-statement-stripped > generic name normalization. NOT the
	// stateful Drain tree — that mutex-locks per call.
	opGroup := templater.NormalizeOperation(sp.Name, kind, httpMethod, httpRoute, dbSystem, dbStmt)

	return &chstore.Span{
		TraceID: hexID(sp.TraceId), SpanID: hexID(sp.SpanId), ParentID: parentID(sp.ParentSpanId),
		Name: sp.Name, OpGroup: opGroup, Kind: kind,
		ServiceName: svcName, HostName: hostName, DeployEnv: deployEnv,
		StatusCode: statusCode, StatusMsg: statusMsg,
		Time:     time.Unix(0, int64(sp.StartTimeUnixNano)).UTC(),
		Duration: dur,
		DBSystem: dbSystem, DBStatement: dbStmt,
		HTTPMethod: httpMethod, HTTPRoute: httpRoute, HTTPStatus: httpStatus,
		RPCSystem: rpcSystem, RPCMethod: rpcMethod,
		PeerService: peerService, MsgSystem: msgSystem,
		AttrKeys: attrK, AttrValues: attrV,
		ResKeys: resK, ResValues: resV,
		Events: string(events), ScopeName: scopeName,
	}
}

// ── Log conversion ────────────────────────────────────────────────────────────

func ConvertLogs(req *logscollpb.ExportLogsServiceRequest) []*chstore.Log {
	var out []*chstore.Log
	for _, rl := range req.ResourceLogs {
		svcName, hostName := "unknown", ""
		resK, resV := attrsToArrays(nil)
		if rl.Resource != nil {
			svcName = attrStr(rl.Resource.Attributes, "service.name", "unknown")
			hostName = attrStr(rl.Resource.Attributes, "host.name", "")
			resK, resV = attrsToArrays(rl.Resource.Attributes)
		}
		for _, sl := range rl.ScopeLogs {
			scopeName := ""
			if sl.Scope != nil {
				scopeName = sl.Scope.Name
			}
			for _, lr := range sl.LogRecords {
				ts := lr.TimeUnixNano
				if ts == 0 {
					ts = lr.ObservedTimeUnixNano
				}
				body := ""
				if lr.Body != nil {
					body = anyValStr(lr.Body)
				}
				attrK, attrV := attrsToArrays(lr.Attributes)
				out = append(out, &chstore.Log{
					TraceID: hexID(lr.TraceId), SpanID: hexID(lr.SpanId),
					Time:         time.Unix(0, int64(ts)).UTC(),
					SeverityNum:  uint8(lr.SeverityNumber),
					SeverityText: lr.SeverityText,
					Body:         body,
					ServiceName:  svcName, HostName: hostName,
					AttrKeys: attrK, AttrValues: attrV,
					ResKeys: resK, ResValues: resV,
					ScopeName: scopeName,
				})
			}
		}
	}
	return out
}

// ── Metric conversion ─────────────────────────────────────────────────────────

// ConvertMetrics returns the metric points plus the OTLP exemplars extracted
// from their datapoints (v0.8.328 — previously dp.Exemplars was silently
// dropped, pivot-audit §2). Exemplar rows carry the SAME series fingerprint
// as their datapoint's metric row — the metric→trace pivot join key. The
// require-trace-context gate is NOT applied here (pure conversion, unit-
// testable); the Ingester's addExemplar applies it so HTTP + gRPC share one
// policy + one set of counters.
func ConvertMetrics(req *metricscollpb.ExportMetricsServiceRequest) ([]*chstore.MetricPoint, []*chstore.ExemplarRow) {
	var out []*chstore.MetricPoint
	var exs []*chstore.ExemplarRow
	for _, rm := range req.ResourceMetrics {
		svcName, svcInstance, hostName := "unknown", "", ""
		resK, resV := attrsToArrays(nil)
		if rm.Resource != nil {
			svcName = attrStr(rm.Resource.Attributes, "service.name", "unknown")
			// service.instance.id is half of the fingerprint's resource
			// identity. Read here (the only place the OTLP resource is in
			// hand) — it is NOT a metric_points column and doesn't need to
			// be: it's baked into series_fingerprint.
			svcInstance = attrStr(rm.Resource.Attributes, "service.instance.id", "")
			hostName = attrStr(rm.Resource.Attributes, "host.name", "")
			resK, resV = attrsToArrays(rm.Resource.Attributes)
		}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				pts, mexs := convertMetric(m, svcName, svcInstance, hostName, resK, resV)
				out = append(out, pts...)
				exs = append(exs, mexs...)
			}
		}
	}
	return out, exs
}

// temporalityStr maps the OTLP aggregation-temporality enum to the
// compact string stored on metric_points. "" for unspecified/unknown so
// the read path treats it as best-effort (delta-assume) rather than
// mis-delta-ing a series we can't classify.
func temporalityStr(t metricspb.AggregationTemporality) string {
	switch t {
	case metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA:
		return "delta"
	case metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE:
		return "cumulative"
	default:
		return ""
	}
}

func convertMetric(m *metricspb.Metric, svcName, svcInstance, hostName string, resK, resV []string) ([]*chstore.MetricPoint, []*chstore.ExemplarRow) {
	// base computes the series fingerprint ONCE per datapoint (v0.8.328) —
	// the same value lands on the metric row and on every exemplar row of
	// that datapoint (the consistency invariant convert_test.go pins).
	base := func(instrument string, startNs, timeNs uint64, val float64, cnt uint64, sum, mn, mx float64, attrs []*commonpb.KeyValue) *chstore.MetricPoint {
		attrK, attrV := attrsToArrays(attrs)
		return &chstore.MetricPoint{
			Metric: m.Name, Instrument: instrument,
			Description: m.Description, Unit: m.Unit,
			ServiceName: svcName, HostName: hostName,
			Time:      time.Unix(0, int64(timeNs)).UTC(),
			StartTime: time.Unix(0, int64(startNs)).UTC(),
			Value: val, Count: cnt, SumValue: sum, MinValue: mn, MaxValue: mx,
			AttrKeys: attrK, AttrValues: attrV, ResKeys: resK, ResValues: resV,
			SeriesFingerprint: SeriesFingerprint(m.Name, attrs, svcName, svcInstance),
		}
	}
	var out []*chstore.MetricPoint
	var exs []*chstore.ExemplarRow
	switch d := m.Data.(type) {
	case *metricspb.Metric_Gauge:
		for _, dp := range d.Gauge.DataPoints {
			p := base("gauge", dp.StartTimeUnixNano, dp.TimeUnixNano,
				numberVal(dp), 0, 0, 0, 0, dp.Attributes)
			out = append(out, p)
			exs = appendExemplars(exs, dp.Exemplars, p, dp.TimeUnixNano)
		}
	case *metricspb.Metric_Sum:
		for _, dp := range d.Sum.DataPoints {
			p := base("sum", dp.StartTimeUnixNano, dp.TimeUnixNano,
				numberVal(dp), 0, 0, 0, 0, dp.Attributes)
			p.Temporality = temporalityStr(d.Sum.AggregationTemporality)
			out = append(out, p)
			exs = appendExemplars(exs, dp.Exemplars, p, dp.TimeUnixNano)
		}
	case *metricspb.Metric_Histogram:
		for _, dp := range d.Histogram.DataPoints {
			sum := derefF64(dp.Sum)
			avg := 0.0
			if dp.Count > 0 {
				avg = sum / float64(dp.Count)
			}
			mn := derefF64(dp.Min)
			mx := derefF64(dp.Max)
			p := base("histogram", dp.StartTimeUnixNano, dp.TimeUnixNano,
				avg, dp.Count, sum, mn, mx, dp.Attributes)
			p.Temporality = temporalityStr(d.Histogram.AggregationTemporality)
			// v0.5.358 — preserve the explicit bucket layout so
			// the read path can estimate quantiles. dp.BucketCounts
			// has len(ExplicitBounds)+1 elements (one per bucket,
			// last is the +Inf bucket). When the producer didn't
			// emit explicit bounds (rare), both arrays remain
			// empty and the read path falls back to avg only.
			if len(dp.ExplicitBounds) > 0 && len(dp.BucketCounts) > 0 {
				p.BucketBounds = append([]float64(nil), dp.ExplicitBounds...)
				p.BucketCounts = append([]uint64(nil), dp.BucketCounts...)
			}
			out = append(out, p)
			exs = appendExemplars(exs, dp.Exemplars, p, dp.TimeUnixNano)
		}
	case *metricspb.Metric_ExponentialHistogram:
		for _, dp := range d.ExponentialHistogram.DataPoints {
			sum := derefF64(dp.Sum)
			avg := 0.0
			if dp.Count > 0 {
				avg = sum / float64(dp.Count)
			}
			// v0.8.435 (exemplar audit Faz D) — full fidelity: min/max
			// were literal 0,0 and temporality was never set (unlike the
			// explicit-histogram arm above); the native bucket structure
			// was dropped entirely, so quantile reads degraded to avg.
			p := base("exp_histogram", dp.StartTimeUnixNano, dp.TimeUnixNano,
				avg, dp.Count, sum, derefF64(dp.Min), derefF64(dp.Max), dp.Attributes)
			p.Temporality = temporalityStr(d.ExponentialHistogram.AggregationTemporality)
			// Materialize the exponential buckets as EXPLICIT bounds into
			// the existing bucket_bounds/bucket_counts columns — no new
			// schema, and the read path (percentileFromBuckets /
			// cumulativeToDelta) works on exp rows unchanged.
			if bounds, counts, ok := expBucketsToExplicit(dp); ok {
				p.BucketBounds = bounds
				p.BucketCounts = counts
			}
			out = append(out, p)
			exs = appendExemplars(exs, dp.Exemplars, p, dp.TimeUnixNano)
		}
	case *metricspb.Metric_Summary:
		// SummaryDataPoint carries NO exemplars field in the OTLP proto —
		// nothing to extract here (pivot-audit §2).
		for _, dp := range d.Summary.DataPoints {
			avg := 0.0
			if dp.Count > 0 {
				avg = dp.Sum / float64(dp.Count)
			}
			out = append(out, base("summary", dp.StartTimeUnixNano, dp.TimeUnixNano,
				avg, dp.Count, dp.Sum, 0, 0, dp.Attributes))
		}
	}
	return out, exs
}

// appendExemplars converts one datapoint's OTLP exemplars into ExemplarRows
// carrying the datapoint's metric-row identity (fingerprint / metric /
// service). No dedup or sampling at ingest (v0.8.328 design call: exemplar
// volume is producer-bounded — SDKs keep ~1 per series per export).
func appendExemplars(dst []*chstore.ExemplarRow, exemplars []*metricspb.Exemplar, p *chstore.MetricPoint, dpTimeNs uint64) []*chstore.ExemplarRow {
	for _, ex := range exemplars {
		ts := ex.TimeUnixNano
		if ts == 0 {
			// Producer omitted the exemplar timestamp — anchor to the
			// datapoint. A 1970 row would sit outside every query window
			// and be unreachable forever.
			ts = dpTimeNs
		}
		var attrs map[string]string
		if len(ex.FilteredAttributes) > 0 {
			attrs = make(map[string]string, len(ex.FilteredAttributes))
			for _, kv := range ex.FilteredAttributes {
				attrs[kv.Key] = anyValStr(kv.Value)
			}
		}
		dst = append(dst, &chstore.ExemplarRow{
			Fingerprint: p.SeriesFingerprint,
			Metric:      p.Metric,
			Service:     p.ServiceName,
			Time:        time.Unix(0, int64(ts)).UTC(),
			Value:       numberVal(ex), // Exemplar satisfies numberDP (AsDouble/AsInt oneof)
			// parentID (not hexID): OTel "no trace context" arrives as nil
			// bytes OR all-zero bytes depending on the SDK — both must
			// collapse to "" so the require-trace-context gate sees them
			// identically (same disagreement parentID handles for spans).
			TraceID:       parentID(ex.TraceId),
			SpanID:        parentID(ex.SpanId),
			FilteredAttrs: attrs,
		})
	}
	return dst
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func hexID(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

// parentID is hexID specialised for parent_span_id: also treats an
// all-zero byte slice as empty. OTel SDKs disagree on the wire
// format for "no parent" — some send nil bytes (length 0), others
// send 8 zero bytes. Both must round-trip to "" so the downstream
// 'parent_id = ""' root check works regardless of sender.
func parentID(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	allZero := true
	for _, x := range b {
		if x != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return ""
	}
	return hex.EncodeToString(b)
}

func kindStr(k tracepb.Span_SpanKind) string {
	switch k {
	case tracepb.Span_SPAN_KIND_SERVER:
		return "server"
	case tracepb.Span_SPAN_KIND_CLIENT:
		return "client"
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return "producer"
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return "consumer"
	default:
		return "internal"
	}
}

func attrsToArrays(attrs []*commonpb.KeyValue) ([]string, []string) {
	if len(attrs) == 0 {
		return []string{}, []string{}
	}
	keys := make([]string, 0, len(attrs))
	vals := make([]string, 0, len(attrs))
	for _, kv := range attrs {
		keys = append(keys, kv.Key)
		vals = append(vals, anyValStr(kv.Value))
	}
	return keys, vals
}

func anyValStr(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'f', -1, 64)
	case *commonpb.AnyValue_BoolValue:
		if x.BoolValue {
			return "true"
		}
		return "false"
	case *commonpb.AnyValue_ArrayValue:
		b, _ := json.Marshal(x.ArrayValue)
		return string(b)
	case *commonpb.AnyValue_KvlistValue:
		b, _ := json.Marshal(x.KvlistValue)
		return string(b)
	}
	return ""
}

func attrStr(attrs []*commonpb.KeyValue, key, def string) string {
	for _, kv := range attrs {
		if kv.Key == key {
			return anyValStr(kv.Value)
		}
	}
	return def
}

func attrInt(attrs []*commonpb.KeyValue, key string, def int64) int64 {
	for _, kv := range attrs {
		if kv.Key == key {
			if kv.Value != nil {
				if iv, ok := kv.Value.Value.(*commonpb.AnyValue_IntValue); ok {
					return iv.IntValue
				}
			}
		}
	}
	return def
}

type spanEvent struct {
	Name       string            `json:"name"`
	TimeNano   uint64            `json:"timeNano"`
	Attributes map[string]string `json:"attributes"`
}

func convertEvents(evs []*tracepb.Span_Event) []spanEvent {
	out := make([]spanEvent, 0, len(evs))
	for _, e := range evs {
		attrK, attrV := attrsToArrays(e.Attributes)
		m := make(map[string]string, len(attrK))
		for i, k := range attrK {
			if i < len(attrV) {
				m[k] = attrV[i]
			}
		}
		out = append(out, spanEvent{Name: e.Name, TimeNano: e.TimeUnixNano, Attributes: m})
	}
	return out
}

type numberDP interface {
	GetAsDouble() float64
	GetAsInt() int64
}

func numberVal(dp numberDP) float64 {
	if v := dp.GetAsDouble(); v != 0 {
		return v
	}
	return float64(dp.GetAsInt())
}

func derefF64(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
