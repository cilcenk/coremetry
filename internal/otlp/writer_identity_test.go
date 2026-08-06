package otlp

import (
	"testing"

	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// v0.9.724 — yazar kimliği zinciri: service.instance.id →
// k8s.pod.name → host.name (ConvertMetrics içinde çözülür,
// SeriesFingerprint'e girer).
//
// Operatör vakası (prod ekranı): instance.id yaymayan çok-pod'lu
// serviste aynı attr-set'li kümülatif sayaçlar TEK fingerprint'e
// çöküyordu; okuma yolu her pod-değişimini reset sanıp delta=cur
// (ham sayaç, binlik ölçek) basıyordu — testere + "-0.000".
// Okuma tarafındaki simetrik zincir metricrate_sql_test.go'da pinli.

func writerReq(resAttrs map[string]string) *metricscollpb.ExportMetricsServiceRequest {
	var kvs []*commonpb.KeyValue
	for k, v := range resAttrs {
		kvs = append(kvs, &commonpb.KeyValue{
			Key:   k,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}},
		})
	}
	return &metricscollpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: kvs},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "http.server.request.duration",
					Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
						IsMonotonic:            true,
						AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
						DataPoints: []*metricspb.NumberDataPoint{{
							TimeUnixNano: 1e18,
							Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 42},
						}},
					}},
				}},
			}},
		}},
	}
}

func fpOf(t *testing.T, resAttrs map[string]string) uint64 {
	t.Helper()
	pts, _ := ConvertMetrics(writerReq(resAttrs))
	if len(pts) != 1 {
		t.Fatalf("%d nokta, 1 bekleniyordu", len(pts))
	}
	return pts[0].SeriesFingerprint
}

func TestWriterIdentityChain(t *testing.T) {
	svc := map[string]string{"service.name": "cm-get-service"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range svc {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	t.Run("instance.id yokken pod adı ayrıştırır (prod vakası)", func(t *testing.T) {
		a := fpOf(t, with(map[string]string{"k8s.pod.name": "cm-get-7d4-aaa"}))
		b := fpOf(t, with(map[string]string{"k8s.pod.name": "cm-get-7d4-bbb"}))
		if a == b {
			t.Fatal("iki pod tek fingerprint'e çöktü — interleave sınıfı geri geldi")
		}
	})

	t.Run("pod adı da yokken host.name ayrıştırır", func(t *testing.T) {
		a := fpOf(t, with(map[string]string{"host.name": "node-a"}))
		b := fpOf(t, with(map[string]string{"host.name": "node-b"}))
		if a == b {
			t.Fatal("iki host tek fingerprint'e çöktü")
		}
	})

	t.Run("instance.id VARSA zincir kısa devre — pod adı kimliğe girmez", func(t *testing.T) {
		a := fpOf(t, with(map[string]string{"service.instance.id": "i-1", "k8s.pod.name": "p-a"}))
		b := fpOf(t, with(map[string]string{"service.instance.id": "i-1", "k8s.pod.name": "p-b"}))
		if a != b {
			t.Fatal("instance.id varken pod adı kimliği DEĞİŞTİRMEMELİ (mevcut serilerin sürekliliği)")
		}
	})

	t.Run("hiç kimlik yoksa deterministik tek seri (belgeli kalıntı)", func(t *testing.T) {
		if fpOf(t, with(nil)) != fpOf(t, with(nil)) {
			t.Fatal("kimliksiz vaka deterministik değil")
		}
	})
}
