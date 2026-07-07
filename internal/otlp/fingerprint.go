package otlp

// Series fingerprint — v0.8.328, cross-signal pivot Phase 1a (pivot-audit §1).
//
// metric_points had NO persisted series identity: group keys were rebuilt
// per-query from the attr arrays, so there was nothing an exemplar row could
// join back to. SeriesFingerprint INTRODUCES that identity at ingest — one
// uint64 computed once per datapoint, stored on metric_points.series_fingerprint
// AND on exemplars.series_fingerprint, making the metric→trace pivot a pure
// primary-key scan (exemplars ORDER BY (series_fingerprint, timestamp)).
//
// Hash: cespare/xxhash/v2 — already in the module graph (CH driver dep),
// pure-Go, the de-facto Prometheus series-fingerprint hash. In-SQL hashing
// (cityHash64) was rejected: the fingerprint must be computable in Go at
// ingest and stable across ClickHouse versions.
//
// Canonical bytes (PINNED by fingerprint_test.go — changing this layout
// orphans every stored exemplar):
//
//	metricName · 0x00
//	· "k=v" ‹0x1F› "k=v" …   (datapoint attrs, sorted by key)      · 0x00
//	· "service.instance.id=Y" ‹0x1F› "service.name=X"
//	  (resource identity: ONLY these two keys, sorted, empty-valued
//	   pairs skipped — deterministic because the keys are fixed)
//
// 0x00 separates the three sections and 0x1F separates pairs — control bytes
// that never appear in real OTLP attribute keys/values, so split-point
// forgeries need attacker-controlled control characters AND a matching '='
// layout (the pinned injection tests). Attr values are stringified with
// anyValStr — the exact stringification metric_points stores in attr_values —
// so the fingerprint agrees with what the read path already treats as one
// series (int 5 ≡ "5" by design).

import (
	"sort"

	"github.com/cespare/xxhash/v2"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

var (
	fpSectionSep = []byte{0x00} // metricName / dp attrs / resource identity
	fpPairSep    = []byte{0x1f} // between "k=v" pairs within a section
)

// SeriesFingerprint returns the stable identity of one metric series:
// (metric name, datapoint attribute set, service.name + service.instance.id).
// Resource identity is deliberately limited to those two keys (pivot-audit
// open question #3, approved): per-instance series pivot per-instance;
// service-level rollups use the metric+service fallback read path.
func SeriesFingerprint(metricName string, dpAttrs []*commonpb.KeyValue, serviceName, serviceInstanceID string) uint64 {
	d := xxhash.New()
	_, _ = d.WriteString(metricName)
	_, _ = d.Write(fpSectionSep)

	if len(dpAttrs) > 0 {
		// Sort by key so wire order (which OTLP does not guarantee) never
		// changes the identity. Ties broken by stringified value: duplicate
		// keys are illegal per OTLP but must still hash deterministically.
		sorted := make([]*commonpb.KeyValue, len(dpAttrs))
		copy(sorted, dpAttrs)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Key != sorted[j].Key {
				return sorted[i].Key < sorted[j].Key
			}
			return anyValStr(sorted[i].Value) < anyValStr(sorted[j].Value)
		})
		for i, kv := range sorted {
			if i > 0 {
				_, _ = d.Write(fpPairSep)
			}
			_, _ = d.WriteString(kv.Key)
			_, _ = d.WriteString("=")
			_, _ = d.WriteString(anyValStr(kv.Value))
		}
	}
	_, _ = d.Write(fpSectionSep)

	// Resource identity, sorted by key ("service.instance.id" < "service.name").
	// Empty values are skipped (the common non-k8s "no instance id" case) —
	// still deterministic: the key set is fixed, so presence is a pure
	// function of the inputs.
	wrote := false
	if serviceInstanceID != "" {
		_, _ = d.WriteString("service.instance.id=")
		_, _ = d.WriteString(serviceInstanceID)
		wrote = true
	}
	if serviceName != "" {
		if wrote {
			_, _ = d.Write(fpPairSep)
		}
		_, _ = d.WriteString("service.name=")
		_, _ = d.WriteString(serviceName)
	}
	return d.Sum64()
}
