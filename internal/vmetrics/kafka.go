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

	// --- v0.10.582: prod VM'de GÖRÜLEN ama katalogda olmayan aileler.
	// Operatör 2026-09-09 ekran görüntüleriyle gösterdi. Kural: gauge varsa
	// onu al, `_total` sayacını yalnız gauge karşılığı YOKSA ekle — ikisini
	// birden almak kataloğu iki katına çıkarır, teşhis gücü katmaz.

	// Poll aralığı — rebalance fırtınasının SEBEBİNİ söyleyen metrik.
	// Rebalance oranını zaten görüyorduk; poll aralığı max.poll.interval.ms
	// aşımına yaklaşıldığını, yani tüketicinin gruptan atılmak üzere
	// olduğunu önceden söyler.
	{Name: "kafka.consumer.time_between_poll_avg", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "avg", TR: "Poll aralığı ort.", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.time_between_poll_max", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "max", TR: "Poll aralığı maks.", Labels: []string{"client_id"}},

	// Lead — lag'in ikizi ve FARKLI bir alarm. Lag "geride kaldın" der;
	// lead sıfıra yaklaşınca "retention penceresinin dışına düşmek
	// üzeresin", yani VERİ KAYBI riski. Toplama MIN: en kötü partition
	// ortalamada kaybolur.
	{Name: "kafka.consumer.records_lead", Side: "consumer", Kind: KafkaGauge, Unit: "{record}", Agg: "min", TR: "Lead (retention'a uzaklık, en düşük)", Labels: []string{"client_id", "topic", "partition"}},
	{Name: "kafka.consumer.records_lead_avg", Side: "consumer", Kind: KafkaGauge, Unit: "{record}", Agg: "avg", TR: "Ortalama lead", Labels: []string{"client_id", "topic", "partition"}},

	// Kota kısıtı — yavaşlığın sebebi uygulama değil BROKER KOTASI
	// olduğunda bunu görmeden saatler harcanır.
	{Name: "kafka.consumer.fetch_throttle_time_avg", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "avg", TR: "Fetch kota kısıtı ort.", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.fetch_throttle_time_max", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "max", TR: "Fetch kota kısıtı maks.", Labels: []string{"client_id"}},

	// Rebalance ve sync SÜRESİ — oranı vardı, süresi yoktu.
	{Name: "kafka.consumer.rebalance_latency_avg", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "avg", TR: "Rebalance süresi ort.", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.rebalance_latency_max", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "max", TR: "Rebalance süresi maks.", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.sync_time_avg", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "avg", TR: "Grup sync süresi ort.", Labels: []string{"client_id"}},
	{Name: "kafka.consumer.sync_time_max", Side: "consumer", Kind: KafkaGauge, Unit: "ms", Agg: "max", TR: "Grup sync süresi maks.", Labels: []string{"client_id"}},

	// SASL yeniden kimlik doğrulama — bugüne dek yalnız İLK doğrulamayı
	// görüyorduk; oturum yenilemesi ayrı bir arıza sınıfı.
	{Name: "kafka.consumer.failed_reauthentication_rate", Side: "consumer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Yeniden kimlik doğrulama hatası (tüketici)", Labels: []string{"client_id"}},
	{Name: "kafka.producer.failed_reauthentication_rate", Side: "producer", Kind: KafkaGauge, Unit: "1/s", Agg: "sum", TR: "Yeniden kimlik doğrulama hatası (üretici)", Labels: []string{"client_id"}},

	// Üretici verimliliği — küçük batch + düşük sıkıştırma, ağ maliyetinin
	// sebebini söyler.
	{Name: "kafka.producer.batch_size_avg", Side: "producer", Kind: KafkaGauge, Unit: "By", Agg: "avg", TR: "Ortalama batch boyutu", Labels: []string{"client_id"}},
	{Name: "kafka.producer.compression_rate_avg", Side: "producer", Kind: KafkaGauge, Unit: "1", Agg: "avg", TR: "Ortalama sıkıştırma oranı", Labels: []string{"client_id"}},
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
		// v0.10.582 — lead lag'in İKİZİ değil, farklı bir alarm: sıfıra
		// yaklaşması "retention penceresinden düşmek üzeresin" demek.
		// MIN toplaması bilinçli — en kötü partition ortalamada kaybolur.
		{Key: "consumer_lead_min", Metric: "kafka.consumer.records_lead", TR: "En düşük lead (retention'a uzaklık) — servis · istemci", GroupBy: []string{"service.name", "client_id"}},
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
		// v0.10.582 — üçü de "neden" sorusunu cevaplıyor; sayı değil sebep.
		// Tümü eklenmedi: panel her açılışta soru başına bir VM range
		// sorgusu koşuyor, katalog zenginliği panel şişkinliği demek değil.
		{Key: "consumer_poll_gap_max", Metric: "kafka.consumer.time_between_poll_max", TR: "Poll aralığı maks. — istemci", GroupBy: []string{"client_id"}},
		{Key: "consumer_fetch_throttle_avg", Metric: "kafka.consumer.fetch_throttle_time_avg", TR: "Fetch kota kısıtı ort. — istemci", GroupBy: []string{"client_id"}},
		{Key: "consumer_rebalance_latency_avg", Metric: "kafka.consumer.rebalance_latency_avg", TR: "Rebalance süresi ort. — istemci", GroupBy: []string{"client_id"}},
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

// kafkaPickQuestions — v0.10.575: türetilmiş soru listesi kurucusu. TEK GÖVDE
// kuralı: türeyen liste soruyu yeniden YAZMAZ, kaynak listeden anahtarla SEÇER
// — ikiz gövde yazılırsa metrik/etiket zamanla kayar ("aynı kalacak" yorumu
// garanti değildir). prefixGroupBy seçilen sorunun kırılımına önek olarak
// eklenir (tekrar edeni yutar). Kaynakta olmayan anahtar atlanır; uzunluk
// iddiası testte pinli (kafka_test.go).
func kafkaPickQuestions(src []KafkaQuestion, prefixGroupBy []string, keys ...string) []KafkaQuestion {
	byKey := make(map[string]KafkaQuestion, len(src))
	for _, q := range src {
		byKey[q.Key] = q
	}
	out := make([]KafkaQuestion, 0, len(keys))
	for _, k := range keys {
		q, ok := byKey[k]
		if !ok {
			continue
		}
		gb := make([]string, 0, len(prefixGroupBy)+len(q.GroupBy))
		seen := make(map[string]bool, len(prefixGroupBy)+len(q.GroupBy))
		for _, g := range append(append([]string(nil), prefixGroupBy...), q.GroupBy...) {
			if g == "" || seen[g] {
				continue
			}
			seen[g] = true
			gb = append(gb, g)
		}
		q.GroupBy = gb
		out = append(out, q)
	}
	return out
}

// kafkaTopicChartKeys — topic sayfasının üst grafiği: giren/çıkan kayıt.
var kafkaTopicChartKeys = []string{"producer_send_rate", "consumer_consumed_rate"}

// KafkaTopicChartQuestions — v0.10.575: topic detay sayfası AÇILIŞINDA koşan
// dar set (yalnız üst grafik). Sayfanın tamamı 12 VM range sorgusu demek;
// VM/ES maliyet disiplini gereği açılışta yalnız bu iki soru gider, ağır
// bloklar sekme seçilince ?set=topic|clients ile istenir.
//
// Anahtarlar KafkaTopicQuestions ile AYNI — FE bloğu tek isimle okur, set
// değişince adlandırma kaymaz. Seçim türetmedir, kopya değil.
func KafkaTopicChartQuestions() []KafkaQuestion {
	return kafkaPickQuestions(KafkaTopicQuestions(), nil, kafkaTopicChartKeys...)
}

// kafkaClientHealthKeys — topic sayfasının "İstemci sağlığı" sekmesi.
// Hepsi YALNIZ client_id label'lı metrikler (topic label'ı YOK): bağlantı,
// broker istek gecikmesi, rebalance, son poll, fetch gecikmesi.
var kafkaClientHealthKeys = []string{
	"producer_connection_count",
	"consumer_connection_count",
	"producer_connection_creation_rate",
	"consumer_connection_creation_rate",
	"producer_request_latency_avg",
	"producer_request_latency_max",
	"consumer_rebalance_rate",
	"consumer_last_poll",
	"consumer_fetch_latency_avg",
	"consumer_poll_gap_max",       // v0.10.582
	"consumer_fetch_throttle_avg", // v0.10.582
}

// KafkaClientHealthQuestions — v0.10.575: topic detay sayfasının istemci
// sağlığı seti. Metrikler İSTEMCİYE aittir, topic'e değil — hiçbiri `topic`
// label'ı taşımaz, dolayısıyla topic'e göre SÜZÜLEMEZ (KafkaQuery zaten
// hata verir). Kapsam bu yüzden "topic'e dokunan servisler"dir; uç bunu
// yanıtta scope="services" ile ilan eder, sessiz daraltma yapmaz.
//
// Anahtarlar KafkaServiceQuestions ile aynı yazımdadır (servis Infra paneli
// v0.10.552 ile aynı blok adları) ve o listeden TÜRETİLİR — servis paneli
// değişmez. Tek fark kırılım: topic sayfası çok servislidir, önek olarak
// service.name eklenir; aksi hâlde iki serviste tekrarlanan client_id
// (Spring varsayılanı "consumer-<group>-1") tek seriye sessizce birleşirdi.
func KafkaClientHealthQuestions() []KafkaQuestion {
	return kafkaPickQuestions(KafkaServiceQuestions(), []string{"service.name"}, kafkaClientHealthKeys...)
}

// KafkaPartitionQuestions — v0.10.589: topic sayfası "Partition'lar" sekmesi.
// İkisi de topic etiketli (topic süzgeci uygulanır) ve partition kırılımlı.
// Kırılım SIRASI FE ile sözleşme (partitionLag.ts keyOf): service · client ·
// partition. Lag ve lead bir arada, çünkü FARKLI alarmlar: lag "geride
// kaldın", lead sıfıra yaklaşınca "retention'dan düşmek üzeresin".
func KafkaPartitionQuestions() []KafkaQuestion {
	gb := []string{"service.name", "client_id", "partition"}
	return []KafkaQuestion{
		{Key: "consumer_partition_lag", Metric: "kafka.consumer.records_lag", TR: "Lag — partition", GroupBy: gb},
		{Key: "consumer_partition_lead", Metric: "kafka.consumer.records_lead", TR: "Lead — partition", GroupBy: gb},
	}
}
