package otlp

// forward_filter.go — v0.10.373: the VictoriaMetrics forward honours the
// pipeline's metric DROP rules.
//
// v0.10.293 forwards the RAW export body (gzip bytes untouched) before
// the ClickHouse conversion; the pipeline (`AcceptMetric`) runs only on
// the converted points. So every drop rule — including the exclusion
// rules' "ingest'te düşür" checkbox, which api/metric_exclusions.go
// bridges into pipeline drop rules — held for ClickHouse and was silent
// for VictoriaMetrics: the operator unticked a probe route and VM kept
// storing it.
//
// Here the same predicate (Engine.DropsMetric, the deterministic half)
// is evaluated per OTLP data point on a MetricPoint VIEW built the way
// ConvertMetrics builds it (same name / service / host / attribute
// arrays, so a rule cannot match on one side and miss on the other).
// Dropped points are removed from the request in place; empty metrics,
// scopes and resources are pruned; the forward re-marshals. With no drop
// rule the raw pass-through is untouched (HasMetricDropRules fast path).

import (
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// dropForwardPoints removes every data point `drops` rejects. Returns
// the number of dropped points; the request is mutated in place.
func dropForwardPoints(req *metricscollpb.ExportMetricsServiceRequest, drops func(*chstore.MetricPoint) bool) int {
	if req == nil || drops == nil {
		return 0
	}
	dropped := 0
	rms := req.ResourceMetrics[:0]
	for _, rm := range req.ResourceMetrics {
		svcName, hostName := "unknown", ""
		resK, resV := attrsToArrays(nil)
		if rm.Resource != nil {
			svcName = attrStr(rm.Resource.Attributes, "service.name", "unknown")
			hostName = attrStr(rm.Resource.Attributes, "host.name", "")
			resK, resV = attrsToArrays(rm.Resource.Attributes)
		}
		view := func(m *metricspb.Metric, instrument string, attrs []*commonpb.KeyValue) *chstore.MetricPoint {
			attrK, attrV := attrsToArrays(attrs)
			return &chstore.MetricPoint{
				Metric: m.Name, Instrument: instrument, Unit: m.Unit,
				ServiceName: svcName, HostName: hostName,
				AttrKeys: attrK, AttrValues: attrV, ResKeys: resK, ResValues: resV,
			}
		}
		sms := rm.ScopeMetrics[:0]
		for _, sm := range rm.ScopeMetrics {
			ms := sm.Metrics[:0]
			for _, m := range sm.Metrics {
				dropped += dropMetricPoints(m, func(instrument string, attrs []*commonpb.KeyValue) bool {
					return drops(view(m, instrument, attrs))
				})
				if !metricEmpty(m) {
					ms = append(ms, m)
				}
			}
			sm.Metrics = ms
			if len(sm.Metrics) > 0 {
				sms = append(sms, sm)
			}
		}
		rm.ScopeMetrics = sms
		if len(rm.ScopeMetrics) > 0 {
			rms = append(rms, rm)
		}
	}
	req.ResourceMetrics = rms
	return dropped
}

// dropMetricPoints — one metric, all five data-point shapes.
func dropMetricPoints(m *metricspb.Metric, drop func(instrument string, attrs []*commonpb.KeyValue) bool) int {
	n := 0
	switch d := m.Data.(type) {
	case *metricspb.Metric_Gauge:
		if d.Gauge == nil {
			return 0
		}
		kept := d.Gauge.DataPoints[:0]
		for _, dp := range d.Gauge.DataPoints {
			if drop("gauge", dp.Attributes) {
				n++
				continue
			}
			kept = append(kept, dp)
		}
		d.Gauge.DataPoints = kept
	case *metricspb.Metric_Sum:
		if d.Sum == nil {
			return 0
		}
		kept := d.Sum.DataPoints[:0]
		for _, dp := range d.Sum.DataPoints {
			if drop("sum", dp.Attributes) {
				n++
				continue
			}
			kept = append(kept, dp)
		}
		d.Sum.DataPoints = kept
	case *metricspb.Metric_Histogram:
		if d.Histogram == nil {
			return 0
		}
		kept := d.Histogram.DataPoints[:0]
		for _, dp := range d.Histogram.DataPoints {
			if drop("histogram", dp.Attributes) {
				n++
				continue
			}
			kept = append(kept, dp)
		}
		d.Histogram.DataPoints = kept
	case *metricspb.Metric_ExponentialHistogram:
		if d.ExponentialHistogram == nil {
			return 0
		}
		kept := d.ExponentialHistogram.DataPoints[:0]
		for _, dp := range d.ExponentialHistogram.DataPoints {
			if drop("exponential_histogram", dp.Attributes) {
				n++
				continue
			}
			kept = append(kept, dp)
		}
		d.ExponentialHistogram.DataPoints = kept
	case *metricspb.Metric_Summary:
		if d.Summary == nil {
			return 0
		}
		kept := d.Summary.DataPoints[:0]
		for _, dp := range d.Summary.DataPoints {
			if drop("summary", dp.Attributes) {
				n++
				continue
			}
			kept = append(kept, dp)
		}
		d.Summary.DataPoints = kept
	}
	return n
}

func metricEmpty(m *metricspb.Metric) bool {
	switch d := m.Data.(type) {
	case *metricspb.Metric_Gauge:
		return d.Gauge == nil || len(d.Gauge.DataPoints) == 0
	case *metricspb.Metric_Sum:
		return d.Sum == nil || len(d.Sum.DataPoints) == 0
	case *metricspb.Metric_Histogram:
		return d.Histogram == nil || len(d.Histogram.DataPoints) == 0
	case *metricspb.Metric_ExponentialHistogram:
		return d.ExponentialHistogram == nil || len(d.ExponentialHistogram.DataPoints) == 0
	case *metricspb.Metric_Summary:
		return d.Summary == nil || len(d.Summary.DataPoints) == 0
	}
	return true
}

// forwardWithDrops — the forward entry both transports use: applies the
// pipeline's drop rules when any exist, otherwise forwards `raw` as-is.
// Returns the number of points dropped from the forward.
//
// `raw` is the wire body when the transport has one (HTTP: gzip bytes
// or protobuf, forwarded untouched when nothing is dropped); gRPC passes
// an empty MetricBody and the request is marshalled — exactly as before,
// and never while the forward is disabled.
func (ing *Ingester) forwardWithDrops(req *metricscollpb.ExportMetricsServiceRequest, raw MetricBody) int {
	if ing.metricFwd == nil || ing.metricFwdEnabled == nil || !ing.metricFwdEnabled() {
		return 0
	}
	sendRaw := func() {
		if len(raw.Body) > 0 {
			ing.forwardMetrics(raw.Body, raw.Gzipped)
			return
		}
		ing.forwardMetrics(marshalForForward(req), false)
	}
	if ing.pipeline == nil || !ing.pipeline.HasMetricDropRules() {
		sendRaw()
		return 0
	}
	n := dropForwardPoints(req, ing.pipeline.DropsMetric)
	if n == 0 {
		sendRaw()
		return 0
	}
	ing.metricFwdFiltered.Add(uint64(n))
	if len(req.ResourceMetrics) == 0 {
		return n // nothing left to forward
	}
	ing.forwardMetrics(marshalForForward(req), false)
	return n
}

// MetricForwardFiltered — data points the drop rules removed from the
// VictoriaMetrics forward (/admin/stats).
func (ing *Ingester) MetricForwardFiltered() uint64 { return ing.metricFwdFiltered.Load() }
