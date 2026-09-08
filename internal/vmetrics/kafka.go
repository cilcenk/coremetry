package vmetrics

// kafka.go — v0.10.550 (docs/audit/messaging-kafka-metrics-2026-09-08.md, Faz 1).
//
// OTel Java agent'ın `kafka-clients-metrics` modülü Kafka istemcisinin kendi JMX
// metriklerini OTel'e köprüler: ad `kafka.<producer|consumer>.<jmx-adı>`, VM'de
// nokta→alt çizgi. Bu dosya SAF: katalog (ad, taraf, tip, birim, toplama,
// label'lar) + soru → chstore.MetricQueryFilter üreticisi. Sorgu seam'e gider
// (metricSource.QueryMetric); Flux/PromQL burada YAZILMAZ.
//
// İki doğruluk kuralı katalogda kilitli (kafka_test.go):
//   • `_rate/_avg/_max/_count` GAUGE'dur — Kafka'nın kendi ~30 s pencere
//     ortalaması; rate()/increase() uygulanmaz. `_total` COUNTER'dır, yalnız rate.
//   • Sorgu daima SERVİS kapsamlı ve pencereli (client_id × topic × partition
//     kardinalitesi; promapi 1000 seri tavanı). Topic/client_id süzgeci yalnız o
//     label'ı taşıyan metrikte; groupBy yalnız bilinen label.
//
// UI adlandırması (audit §3): client_id ≠ consumer group; records_lag_max = "bu
// istemcinin gördüğü en yüksek lag (partition)". Broker lag'i kapsam dışı.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	KafkaGauge   = "gauge"
	KafkaCounter = "counter"
	// KafkaDefaultMaxDataPoints — çekmece/panel genişliği; Endpoints metrik ucu
	// ile aynı büyüklük (≤60 adım).
	KafkaDefaultMaxDataPoints = 60
)

// KafkaMetric — katalog satırı.
type KafkaMetric struct {
	Name   string   // OTel adı (kafka.consumer.records_lag_max)
	Side   string   // producer | consumer
	Kind   string   // KafkaGauge | KafkaCounter
	Unit   string   // "" | ms | s | By | By/s | 1/s | {record}/s | {record} | …
	Agg    string   // seam Aggregation: sum | avg | max (gauge) · rate (counter)
	TR     string   // UI etiketi
	Labels []string // client_id · topic · partition · node_id (resource label'ları hep var)
}

var KafkaCatalog = []KafkaMetric{
	// ── Üretici ──
	{Name: "kafka.producer.connection_count", Side: "producer", Kind: KafkaGauge, Unit: "{connection}", Agg: "sum", TR: "Açık bağlantı (üretici)", Labels: []string{"client_id"}},
	{Name: "kafka.producer.connection_creation_rate", Side: "producer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Bağlantı açma (üretici)", Labels: []string{"client_id"}},
	{Name: "kafka.producer.connection_close_rate", Side: "producer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Bağlantı kapama (üretici)", Labels: []string{"client_id"}},
	{Name: "kafka.producer.record_send_rate", Side: "producer", Kind: KafkaGauge, Unit: "{record}/s", Agg: "sum", TR: "Gönderilen kayıt", Labels: []string{"client_id", "topic"}},
	{Name: "kafka.producer.record_send_total", Side: "producer", Kind: KafkaCounter, Unit: "{record}", Agg: "rate", TR: "Gönderilen kayıt (sayaç)", Labels: []string{"client_id", "topic"}},
	{Name: "kafka.producer.record_error_rate", Side: "producer", Kind: KafkaGauge, Unit: "{record}/s", Agg: "sum", TR: "Gönderim hatası", Labels: []string{"client_id", "topic"}},
	{Name: "kafka.producer.record_error_total", Side: "producer", Kind: KafkaCounter, Unit: "{record}", Agg: "rate", TR: "Gönderim hatası (sayaç)", Labels: []string{"client_id", "topic"}},
	{Name: "kafka.producer.record_retry_rate", Side: "producer", Kind: KafkaGauge, Unit: "{record}/s", Agg: "sum", TR: "Yeniden deneme", Labels: []string{"client_id", "topic"}},
	{Name: "kafka.producer.byte_rate", Side: "producer", Kind: KafkaGauge, Unit: "By/s", Agg: "sum", TR: "Gönderilen bayt", Labels: []string{"client_id", "topic"}},
	{Name: "kafka.producer.request_latency_avg", Side: "producer", Kind: KafkaGauge, Unit: "ms", Agg: "avg", TR: "Broker istek gecikmesi (ort.)", Labels: []string{"client_id", "node_id"}},
	{Name: "kafka.producer.request_latency_max", Side: "producer", Kind: KafkaGauge, Unit: "ms", Agg: "max", TR: "Broker istek gecikmesi (maks.)", Labels: []string{"client_id", "node_id"}},
	{Name: "kafka.producer.record_queue_time_avg", Side: "producer", Kind: KafkaGauge, Unit: "ms", Agg: "avg", TR: "Kayıt kuyruk süresi (ort.)", Labels: []string{"client_id"}},
	{Name: "kafka.producer.buffer_available_bytes", Side: "producer", Kind: KafkaGauge, Unit: "By", Agg: "sum", TR: "Boş tampon", Labels: []string{"client_id"}},
	{Name: "kafka.producer.buffer_exhausted_rate", Side: "producer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Tampon tükenmesi", Labels: []string{"client_id"}},
	{Name: "kafka.producer.requests_in_flight", Side: "producer", Kind: KafkaGauge, Unit: "{request}", Agg: "sum", TR: "Uçuştaki istek", Labels: []string{"client_id"}},
	{Name: "kafka.producer.waiting_threads", Side: "producer", Kind: KafkaGauge, Unit: "{thread}", Agg: "sum", TR: "Tampon bekleyen iş parçacığı", Labels: []string{"client_id"}},
	{Name: "kafka.producer.failed_authentication_rate", Side: "producer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Kimlik doğrulama hatası (üretici)", Labels: []string{"client_id"}},
	// ── Tüketici ──
	{Name: "kafka.consumer.connection_count", Side: "consumer", Kind: KafkaGauge, Unit: "{connection}", Agg: "sum", TR: "Açık bağlantı (tüketici)", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.connection_creation_rate", Side: "consumer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Bağlantı açma (tüketici)", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.connection_close_rate", Side: "consumer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Bağlantı kapama (tüketici)", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.records_consumed_rate", Side: "consumer", Kind: KafkaGauge, Unit: "{record}/s", Agg: "sum", TR: "Tüketilen kayıt", Labels: []string{"client_id", "topic"}},
	{Name: "kafka.consumer.records_consumed_total", Side: "consumer", Kind: KafkaCounter, Unit: "{record}", Agg: "rate", TR: "Tüketilen kayıt (sayaç)", Labels: []string{"client_id", "topic"}},
	{Name: "kafka.consumer.bytes_consumed_rate", Side: "consumer", Kind: KafkaGauge, Unit: "By/s", Agg: "sum", TR: "Tüketilen bayt", Labels: []string{"client_id", "topic"}},
	{Name: "kafka.consumer.records_lag", Side: "consumer", Kind: KafkaGauge, Unit: "{record}", Agg: "max", TR: "Bu istemcinin gördüğü lag (partition)", Labels: []string{"client_id", "topic", "partition"}},
	{Name: "kafka.consumer.records_lag_max", Side: "consumer", Kind: KafkaGauge, Unit: "{record}", Agg: "max", TR: "Bu istemcinin gördüğü en yüksek lag (partition)", Labels: []string{"client_id", "topic", "partition"}},
	{Name: "kafka.consumer.records_lag_avg", Side: "consumer", Kind: KafkaGauge, Unit: "{record}", Agg: "avg", TR: "Bu istemcinin gördüğü ortalama lag (partition)", Labels: []string{"client_id", "topic", "partition"}},
	{Name: "kafka.consumer.fetch_latency_avg", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "avg", TR: "Fetch gecikmesi (ort.)", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.fetch_latency_max", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "max", TR: "Fetch gecikmesi (maks.)", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.fetch_rate", Side: "consumer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Fetch isteği", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.commit_latency_avg", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "avg", TR: "Commit gecikmesi (ort.)", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.commit_rate", Side: "consumer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Commit", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.rebalance_rate_per_hour", Side: "consumer", Kind: KafkaGauge, Unit: "1/h", Agg: "sum", TR: "Rebalance (saatlik)", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.failed_rebalance_total", Side: "consumer", Kind: KafkaCounter, Unit: "{rebalance}", Agg: "rate", TR: "Başarısız rebalance (sayaç)", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.last_poll_seconds_ago", Side: "consumer", Kind: KafkaGauge, Unit: "s", Agg: "max", TR: "Son poll'dan beri", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.assigned_partitions", Side: "consumer", Kind: KafkaGauge, Unit: "{partition}", Agg: "sum", TR: "Atanmış partition", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.heartbeat_rate", Side: "consumer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Heartbeat", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.failed_authentication_rate", Side: "consumer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Kimlik doğrulama hatası (tüketici)", Labels: []string{"client_id"}},
}

var kafkaByName = func() map[string]KafkaMetric {
	m := make(map[string]KafkaMetric, len(KafkaCatalog))
	for _, k := range KafkaCatalog {
		m[k.Name] = k
	}
	return m
}()

// KafkaMetricByName — katalog araması (OTel adı).
func KafkaMetricByName(name string) (KafkaMetric, bool) {
	m, ok := kafkaByName[strings.TrimSpace(name)]
	return m, ok
}

func hasLabel(m KafkaMetric, l string) bool {
	for _, x := range m.Labels {
		if x == l {
			return true
		}
	}
	return false
}

// KafkaQuestion — bir yüzeyin sorduğu soru: sabit metrik + sabit kırılım.
// Key yanıt zarfında blok anahtarı; FE anahtarla çizer, adla değil.
type KafkaQuestion struct {
	Key     string
	Metric  string
	TR      string
	GroupBy []string
}

// KafkaTopicQuestions — topic detayının (çekmece/sayfa) soruları. Hepsi
// `topic` label'lı; kapsam span tarafındaki üretici/tüketici servisleri.
func KafkaTopicQuestions() []KafkaQuestion {
	return []KafkaQuestion{
		{Key: "producer_send_rate", Metric: "kafka.producer.record_send_rate", TR: "Gönderilen kayıt/sn — servis", GroupBy: []string{"service.name"}},
		{Key: "producer_error_rate", Metric: "kafka.producer.record_error_rate", TR: "Gönderim hatası/sn — servis", GroupBy: []string{"service.name"}},
		{Key: "producer_retry_rate", Metric: "kafka.producer.record_retry_rate", TR: "Yeniden deneme/sn — servis", GroupBy: []string{"service.name"}},
		{Key: "consumer_consumed_rate", Metric: "kafka.consumer.records_consumed_rate", TR: "Tüketilen kayıt/sn — servis", GroupBy: []string{"service.name"}},
		{Key: "consumer_lag_max", Metric: "kafka.consumer.records_lag_max", TR: "İstemcinin gördüğü en yüksek lag — servis · istemci", GroupBy: []string{"service.name", "client_id"}},
	}
}

// KafkaServiceQuestions — servis sayfasının "Kafka client" paneli. Metrikler
// istemciye ait (topic'e değil); kapsam tek servis.
func KafkaServiceQuestions() []KafkaQuestion {
	return []KafkaQuestion{
		{Key: "producer_connection_count", Metric: "kafka.producer.connection_count", TR: "Açık bağlantı (üretici) — istemci", GroupBy: []string{"client_id"}},
		{Key: "consumer_connection_count", Metric: "kafka.consumer.connection_count", TR: "Açık bağlantı (tüketici) — istemci", GroupBy: []string{"client_id"}},
		{Key: "producer_connection_creation_rate", Metric: "kafka.producer.connection_creation_rate", TR: "Bağlantı açma/sn (üretici)", GroupBy: []string{"client_id"}},
		{Key: "consumer_connection_creation_rate", Metric: "kafka.consumer.connection_creation_rate", TR: "Bağlantı açma/sn (tüketici)", GroupBy: []string{"client_id"}},
		{Key: "producer_request_latency_avg", Metric: "kafka.producer.request_latency_avg", TR: "Broker istek gecikmesi ort. — istemci", GroupBy: []string{"client_id"}},
		{Key: "producer_request_latency_max", Metric: "kafka.producer.request_latency_max", TR: "Broker istek gecikmesi maks. — istemci", GroupBy: []string{"client_id"}},
		{Key: "producer_error_rate", Metric: "kafka.producer.record_error_rate", TR: "Gönderim hatası/sn — topic", GroupBy: []string{"topic"}},
		{Key: "consumer_lag_max", Metric: "kafka.consumer.records_lag_max", TR: "İstemcinin gördüğü en yüksek lag — topic · istemci", GroupBy: []string{"topic", "client_id"}},
		{Key: "consumer_rebalance_rate", Metric: "kafka.consumer.rebalance_rate_per_hour", TR: "Rebalance/saat — istemci", GroupBy: []string{"client_id"}},
		{Key: "consumer_last_poll", Metric: "kafka.consumer.last_poll_seconds_ago", TR: "Son poll'dan beri (s) — istemci", GroupBy: []string{"client_id"}},
		{Key: "consumer_fetch_latency_avg", Metric: "kafka.consumer.fetch_latency_avg", TR: "Fetch gecikmesi ort. — istemci", GroupBy: []string{"client_id"}},
	}
}

// KafkaScope — sorgunun kapsamı. Services ZORUNLU (kardinalite + doğruluk:
// kapsamsız `kafka_*` tüm filoyu toplar).
type KafkaScope struct {
	Services      []string
	Topic         string
	ClientID      string
	From, To      time.Time
	MaxDataPoints int
}

// KafkaQuery — katalog satırı + kapsam + kırılım → seam süzgeci. SAF.
func KafkaQuery(m KafkaMetric, sc KafkaScope, groupBy []string) (chstore.MetricQueryFilter, error) {
	svcs := uniqSorted(sc.Services)
	if len(svcs) == 0 {
		return chstore.MetricQueryFilter{}, fmt.Errorf("%s: servis kapsamı zorunlu", m.Name)
	}
	if sc.From.IsZero() || sc.To.IsZero() || !sc.To.After(sc.From) {
		return chstore.MetricQueryFilter{}, fmt.Errorf("%s: pencere geçersiz (from<to şart)", m.Name)
	}
	filters := []chstore.FilterExpr{{Key: "service.name", Op: "IN", Values: svcs}}
	if t := strings.TrimSpace(sc.Topic); t != "" {
		if !hasLabel(m, "topic") {
			return chstore.MetricQueryFilter{}, fmt.Errorf("%s: topic label'ı yok, topic süzgeci uygulanamaz", m.Name)
		}
		filters = append(filters, chstore.FilterExpr{Key: "topic", Op: "=", Values: []string{t}})
	}
	if c := strings.TrimSpace(sc.ClientID); c != "" {
		if !hasLabel(m, "client_id") {
			return chstore.MetricQueryFilter{}, fmt.Errorf("%s: client_id label'ı yok", m.Name)
		}
		filters = append(filters, chstore.FilterExpr{Key: "client_id", Op: "=", Values: []string{c}})
	}
	gb := make([]string, 0, len(groupBy))
	for _, g := range groupBy {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if g != "service.name" && g != "host.name" && !hasLabel(m, g) {
			return chstore.MetricQueryFilter{}, fmt.Errorf("%s: groupBy %q bu metriğin label'ı değil", m.Name, g)
		}
		gb = append(gb, g)
	}
	mdp := sc.MaxDataPoints
	if mdp <= 0 {
		mdp = KafkaDefaultMaxDataPoints
	}
	return chstore.MetricQueryFilter{
		Name: m.Name, Filters: filters, GroupBy: gb, Aggregation: m.Agg,
		From: sc.From, To: sc.To, MaxDataPoints: mdp,
	}, nil
}

func uniqSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
