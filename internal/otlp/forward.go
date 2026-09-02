package otlp

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/cilcenk/coremetry/internal/consumer"
)

// forward.go — v0.10.293 (docs/audit/vm-metrics-migration.md Dilim 1b):
// OTLP metrik gövdesinin HAM olarak VictoriaMetrics'e iletilmesi (çift
// yazım, Aşama 1).
//
// İlke: ileti CH modelinden GERİ ÜRETİLMEZ. OTLP/HTTP'de tel gövdesi
// olduğu gibi kuyruğa girer (gzip'liyse gzip'li — tekrar açılıp
// sıkıştırılmaz); zstd/deflate açılmış hâliyle; JSON gövde protobuf'a
// çevrilir (VM'in ucu protobuf); gRPC'de decode edilmiş istek yeniden
// marshal edilir (proto deterministik olmasa da anlamsal olarak özdeş;
// forward_test.go HTTP protobuf yolunda BAYT özdeşliğini çiviler).
//
// Ingest yolunda ikinci ağ çağrısı ASLA senkron değil (audit R5): gövde
// ayrı bir consumer kuyruğuna (bayt bütçeli) kopyalanır; kuyruk doluysa
// düşer ve sayılır — OTLP cevabı etkilenmez (exemplar'ların v0.8.345
// türev-sinyal kuralı). Yazım kapalıyken (ForwardEnabled false) kopya bile
// alınmaz: sıfır maliyet.

// MetricBody — kuyruk öğesi: tel gövdesi + gzip bayrağı.
type MetricBody struct {
	Body    []byte
	Gzipped bool
}

// MetricBodyApproxBytes — consumer bayt bütçesi için (v0.8.355 deseni).
func MetricBodyApproxBytes(b MetricBody) int { return len(b.Body) + 16 }

// SetMetricForward — main.go bağlar; nil-safe (bağlanmayan pod'da no-op).
func (ing *Ingester) SetMetricForward(c *consumer.Consumer[MetricBody]) {
	ing.MetricForward = c
	if c != nil {
		ing.metricFwd = c.Add
	} else {
		ing.metricFwd = nil
	}
}

// SetMetricForwardFunc — test dikişi: kuyruk yerine fonksiyon.
func (ing *Ingester) SetMetricForwardFunc(fn func(MetricBody) bool) { ing.metricFwd = fn }

// SetForwardEnabled — istek anında "yazım açık mı" kararı (vmetrics
// WriteReady): ayar PUT ile değişince restart gerekmez.
func (ing *Ingester) SetForwardEnabled(fn func() bool) { ing.metricFwdEnabled = fn }

// MetricForwardDropped — kuyruk doluyken düşen gövde sayısı (/admin/stats).
func (ing *Ingester) MetricForwardDropped() uint64 { return ing.metricFwdDropped.Load() }

// MetricForwardEnqueued — kuyruğa giren gövde sayısı.
func (ing *Ingester) MetricForwardEnqueued() uint64 { return ing.metricFwdEnqueued.Load() }

// forwardMetrics — nil-safe; kapalıyken kopya yok.
func (ing *Ingester) forwardMetrics(body []byte, gzipped bool) {
	if ing.metricFwd == nil || ing.metricFwdEnabled == nil || !ing.metricFwdEnabled() || len(body) == 0 {
		return
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	if ing.metricFwd(MetricBody{Body: cp, Gzipped: gzipped}) {
		ing.metricFwdEnqueued.Add(1)
	} else {
		ing.metricFwdDropped.Add(1)
	}
}

// readMetricsBody — OTLP/HTTP metrik isteğini decode eder VE ileti gövdesini
// üretir. readProto'nun metrik ikizi: sıkıştırma sözleşmesi aynı
// (decompressBody), 32 MiB tavanı aynı.
//
//	gzip     → req decode edilir; ileti = ham gzip baytları (Gzipped=true)
//	identity → ileti = ham baytlar
//	zstd/deflate → ileti = açılmış baytlar (WriteOTLP gzip'ler)
//	JSON     → ileti = proto.Marshal(req)
func readMetricsBody(r *http.Request) (*metricscollpb.ExportMetricsServiceRequest, MetricBody, error) {
	enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return nil, MetricBody{}, err
	}
	// decompressBody'yi ham baytlar üzerinden yeniden kur (gövde bir kez okundu).
	rr := r.Clone(r.Context())
	rr.Body = io.NopCloser(bytes.NewReader(raw))
	src, done, err := decompressBody(rr)
	if err != nil {
		return nil, MetricBody{}, err
	}
	defer done()
	plain, err := io.ReadAll(io.LimitReader(src, 32<<20))
	if err != nil {
		return nil, MetricBody{}, err
	}
	var req metricscollpb.ExportMetricsServiceRequest
	isJSON := strings.Contains(r.Header.Get("Content-Type"), "application/json")
	if isJSON {
		if err := protojson.Unmarshal(plain, &req); err != nil {
			return nil, MetricBody{}, err
		}
		pb, err := proto.Marshal(&req)
		if err != nil {
			return nil, MetricBody{}, fmt.Errorf("re-encode json metrics: %w", err)
		}
		return &req, MetricBody{Body: pb}, nil
	}
	if err := proto.Unmarshal(plain, &req); err != nil {
		return nil, MetricBody{}, err
	}
	if enc == "gzip" {
		return &req, MetricBody{Body: raw, Gzipped: true}, nil
	}
	return &req, MetricBody{Body: plain}, nil
}

// marshalForForward — gRPC yolu: decode edilmiş isteği yeniden kodlar.
func marshalForForward(req proto.Message) []byte {
	b, err := proto.Marshal(req)
	if err != nil {
		return nil
	}
	return b
}

var _ = atomic.Uint64{}
