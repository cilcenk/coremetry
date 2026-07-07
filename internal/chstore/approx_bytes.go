package chstore

// Approximate per-row in-memory byte sizers for the ingest byte budget
// (v0.8.355, HA audit 🟡#1). main.go passes these to
// consumer.NewSized(...) so each signal consumer can bound its buffered
// memory by BYTES, not just item count — a 500k-item buffer of 15-25KB
// Java stack-trace log bodies is multi-GB and an OOMKill destroys ALL
// buffered signals, worse than the counted drops the budget produces.
//
// These are cheap ESTIMATES — variable string/slice/map content plus a
// fixed constant for the struct itself — not exact heap accounting.
// They run on the OTLP Add hot path and once more in the batch loop,
// so they MUST stay allocation-free (len() sums only).

// approxStrings sums a string slice's content bytes plus the 16-byte
// string header per element (the slices are parallel attr arrays whose
// backing array is part of the buffered row's footprint).
func approxStrings(ss []string) int {
	n := len(ss) * 16
	for _, s := range ss {
		n += len(s)
	}
	return n
}

// Fixed struct overheads: struct fields (headers, times, numerics) +
// the row pointer's allocation slop, rounded up. Precision here buys
// nothing — the budget is a safety valve, not a metering system.
const (
	spanFixedBytes     = 256
	logFixedBytes      = 160
	metricFixedBytes   = 256
	exemplarFixedBytes = 128
	spanLinkFixedBytes = 128
)

// SpanApproxBytes estimates one buffered Span's in-memory footprint.
func SpanApproxBytes(s *Span) int {
	return spanFixedBytes +
		len(s.TraceID) + len(s.SpanID) + len(s.ParentID) +
		len(s.Name) + len(s.OpGroup) + len(s.Kind) +
		len(s.ServiceName) + len(s.HostName) + len(s.DeployEnv) +
		len(s.StatusCode) + len(s.StatusMsg) +
		len(s.DBSystem) + len(s.DBStatement) +
		len(s.HTTPMethod) + len(s.HTTPRoute) +
		len(s.RPCSystem) + len(s.RPCMethod) +
		len(s.PeerService) + len(s.MsgSystem) +
		len(s.Events) + len(s.ScopeName) +
		approxStrings(s.AttrKeys) + approxStrings(s.AttrValues) +
		approxStrings(s.ResKeys) + approxStrings(s.ResValues)
}

// LogApproxBytes estimates one buffered Log's in-memory footprint.
// Body dominates for the fat-log fleets this budget exists for.
func LogApproxBytes(l *Log) int {
	return logFixedBytes +
		len(l.TraceID) + len(l.SpanID) + len(l.SeverityText) +
		len(l.Body) + len(l.ServiceName) + len(l.HostName) + len(l.ScopeName) +
		approxStrings(l.AttrKeys) + approxStrings(l.AttrValues) +
		approxStrings(l.ResKeys) + approxStrings(l.ResValues)
}

// MetricPointApproxBytes estimates one buffered MetricPoint's in-memory
// footprint; histogram bucket slices count 8 bytes per element
// (float64 bounds + uint64 counts → 16/bucket combined).
func MetricPointApproxBytes(p *MetricPoint) int {
	return metricFixedBytes +
		len(p.Metric) + len(p.Instrument) + len(p.Description) + len(p.Unit) +
		len(p.ServiceName) + len(p.HostName) + len(p.Temporality) +
		approxStrings(p.AttrKeys) + approxStrings(p.AttrValues) +
		approxStrings(p.ResKeys) + approxStrings(p.ResValues) +
		len(p.BucketBounds)*8 + len(p.BucketCounts)*8
}

// ExemplarRowApproxBytes estimates one buffered ExemplarRow's in-memory
// footprint; 32 bytes per FilteredAttrs entry covers map bucket overhead.
func ExemplarRowApproxBytes(r *ExemplarRow) int {
	n := exemplarFixedBytes +
		len(r.Metric) + len(r.Service) + len(r.TraceID) + len(r.SpanID)
	for k, v := range r.FilteredAttrs {
		n += len(k) + len(v) + 32
	}
	return n
}

// SpanLinkRowApproxBytes estimates one buffered SpanLinkRow's in-memory
// footprint.
func SpanLinkRowApproxBytes(r *SpanLinkRow) int {
	return spanLinkFixedBytes +
		len(r.TraceID) + len(r.SpanID) +
		len(r.LinkedTraceID) + len(r.LinkedSpanID) +
		len(r.ServiceName) +
		approxStrings(r.AttrKeys) + approxStrings(r.AttrVals)
}
