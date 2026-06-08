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
)

// ── Trace conversion ──────────────────────────────────────────────────────────

func ConvertTraces(req *tracecollpb.ExportTraceServiceRequest) []*chstore.Span {
	var out []*chstore.Span
	for _, rs := range req.ResourceSpans {
		svcName, hostName, deployEnv := "unknown", "", ""
		resK, resV := attrsToArrays(nil)
		if rs.Resource != nil {
			svcName = attrStr(rs.Resource.Attributes, "service.name", "unknown")
			hostName = attrStr(rs.Resource.Attributes, "host.name", "")
			deployEnv = attrStr(rs.Resource.Attributes, "deployment.environment", "")
			resK, resV = attrsToArrays(rs.Resource.Attributes)
		}
		for _, ss := range rs.ScopeSpans {
			scopeName := ""
			if ss.Scope != nil {
				scopeName = ss.Scope.Name
			}
			for _, sp := range ss.Spans {
				out = append(out, convertSpan(sp, svcName, hostName, deployEnv, scopeName, resK, resV))
			}
		}
	}
	return out
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

	return &chstore.Span{
		TraceID: hexID(sp.TraceId), SpanID: hexID(sp.SpanId), ParentID: parentID(sp.ParentSpanId),
		Name: sp.Name, Kind: kind,
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

func ConvertMetrics(req *metricscollpb.ExportMetricsServiceRequest) []*chstore.MetricPoint {
	var out []*chstore.MetricPoint
	for _, rm := range req.ResourceMetrics {
		svcName, hostName := "unknown", ""
		resK, resV := attrsToArrays(nil)
		if rm.Resource != nil {
			svcName = attrStr(rm.Resource.Attributes, "service.name", "unknown")
			hostName = attrStr(rm.Resource.Attributes, "host.name", "")
			resK, resV = attrsToArrays(rm.Resource.Attributes)
		}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				out = append(out, convertMetric(m, svcName, hostName, resK, resV)...)
			}
		}
	}
	return out
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

func convertMetric(m *metricspb.Metric, svcName, hostName string, resK, resV []string) []*chstore.MetricPoint {
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
		}
	}
	var out []*chstore.MetricPoint
	switch d := m.Data.(type) {
	case *metricspb.Metric_Gauge:
		for _, dp := range d.Gauge.DataPoints {
			out = append(out, base("gauge", dp.StartTimeUnixNano, dp.TimeUnixNano,
				numberVal(dp), 0, 0, 0, 0, dp.Attributes))
		}
	case *metricspb.Metric_Sum:
		for _, dp := range d.Sum.DataPoints {
			p := base("sum", dp.StartTimeUnixNano, dp.TimeUnixNano,
				numberVal(dp), 0, 0, 0, 0, dp.Attributes)
			p.Temporality = temporalityStr(d.Sum.AggregationTemporality)
			out = append(out, p)
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
		}
	case *metricspb.Metric_ExponentialHistogram:
		for _, dp := range d.ExponentialHistogram.DataPoints {
			sum := derefF64(dp.Sum)
			avg := 0.0
			if dp.Count > 0 {
				avg = sum / float64(dp.Count)
			}
			out = append(out, base("exp_histogram", dp.StartTimeUnixNano, dp.TimeUnixNano,
				avg, dp.Count, sum, 0, 0, dp.Attributes))
		}
	case *metricspb.Metric_Summary:
		for _, dp := range d.Summary.DataPoints {
			avg := 0.0
			if dp.Count > 0 {
				avg = dp.Sum / float64(dp.Count)
			}
			out = append(out, base("summary", dp.StartTimeUnixNano, dp.TimeUnixNano,
				avg, dp.Count, dp.Sum, 0, 0, dp.Attributes))
		}
	}
	return out
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
