package vmetrics

import (
	"strings"
	"testing"
	"time"
)

// kafka_sets_test.go — v0.10.575: topic detay sayfasının iki türetilmiş soru
// seti (uç: /api/messaging/clients?set=chart|clients). Sözleşme:
//
//   • chart  — KafkaTopicQuestions'ın DAR alt kümesi. Anahtar VE etiket AYNI
//     (FE bloğu tek isimle okur, set değişince adlandırma kaymaz); metrikleri
//     `topic` label'lı, yani topic'e göre süzülebilir.
//   • clients — KafkaServiceQuestions'tan TÜRETİLİR (anahtar yazımı birebir
//     aynı, servis Infra paneli v0.10.552 ile ortak). HİÇBİR üyesi `topic`
//     label'ı TAŞIMAZ: kapsam iddiasının kanıtı budur — uç bu yüzden Topic'i
//     boş geçip scope="services" der. Burada YOKLUK ölçülür (varlık değil):
//     her üye için topic süzgecinin KafkaQuery'de hata verdiği gösterilir.
//   • KafkaServiceQuestions DEĞİŞMEZ — türetme kaynağı mutasyona uğratmaz.
//
// Türetme (kafkaPickQuestions) bilerek "seç, yeniden yazma"dır: ikiz gövde
// zamanla kayar. Uzunluk iddiaları sessiz atlamayı yakalar.

func kafkaQuestionIndex(qs []KafkaQuestion) map[string]KafkaQuestion {
	m := make(map[string]KafkaQuestion, len(qs))
	for _, q := range qs {
		m[q.Key] = q
	}
	return m
}

func TestKafkaTopicChartQuestions(t *testing.T) {
	chart := KafkaTopicChartQuestions()
	if len(chart) != 2 {
		t.Fatalf("chart seti 2 soru olmalı (üst grafik), geldi %d: %+v", len(chart), chart)
	}
	topic := kafkaQuestionIndex(KafkaTopicQuestions())
	seen := map[string]bool{}
	for _, q := range chart {
		if seen[q.Key] {
			t.Errorf("anahtar tekrar: %s", q.Key)
		}
		seen[q.Key] = true
		src, ok := topic[q.Key]
		if !ok {
			t.Errorf("%s: chart anahtarı KafkaTopicQuestions'ta yok — FE tek isimle okuyamaz", q.Key)
			continue
		}
		if q.Metric != src.Metric || q.TR != src.TR || strings.Join(q.GroupBy, ",") != strings.Join(src.GroupBy, ",") {
			t.Errorf("%s: chart sorusu topic sorusundan sapmış: %+v vs %+v", q.Key, q, src)
		}
		m, ok := KafkaMetricByName(q.Metric)
		if !ok {
			t.Errorf("%s: metrik katalogda yok: %s", q.Key, q.Metric)
			continue
		}
		// Chart seti topic KAPSAMLIDIR: metrik topic label'ı taşımalı, yoksa
		// uç topic süzgecini uygulayamaz (KafkaQuery hata verir).
		if !hasLabel(m, "topic") {
			t.Errorf("%s: chart metriği topic label'sız: %s", q.Key, q.Metric)
		}
	}
	if !seen["producer_send_rate"] || !seen["consumer_consumed_rate"] {
		t.Fatalf("üst grafik giren+çıkan kaydı ister: %v", seen)
	}
}

func TestKafkaClientHealthQuestions(t *testing.T) {
	from := time.Date(2026, 9, 8, 6, 0, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)
	health := KafkaClientHealthQuestions()
	if len(health) != 11 {
		t.Fatalf("client-health seti 11 soru olmalı, geldi %d: %+v", len(health), health)
	}
	svc := kafkaQuestionIndex(KafkaServiceQuestions())
	seen := map[string]bool{}
	for _, q := range health {
		if seen[q.Key] {
			t.Errorf("anahtar tekrar: %s", q.Key)
		}
		seen[q.Key] = true
		src, ok := svc[q.Key]
		if !ok {
			t.Errorf("%s: anahtar KafkaServiceQuestions yazımında değil", q.Key)
			continue
		}
		if q.Metric != src.Metric || q.TR != src.TR {
			t.Errorf("%s: servis panelinden sapmış: %+v vs %+v", q.Key, q, src)
		}
		m, ok := KafkaMetricByName(q.Metric)
		if !ok {
			t.Errorf("%s: metrik katalogda yok: %s", q.Key, q.Metric)
			continue
		}
		// KAPSAM İDDİASININ KANITI — yokluk ölçülür: hiçbiri topic label'ı
		// taşımaz, dolayısıyla topic süzgeci uygulanamaz.
		if hasLabel(m, "topic") {
			t.Errorf("%s (%s): topic label'ı VAR — bu soru client-health setine ait değil, topic setine ait", q.Key, q.Metric)
		}
		if _, err := KafkaQuery(m, KafkaScope{Services: []string{"a"}, Topic: "orders", From: from, To: to}, q.GroupBy); err == nil {
			t.Errorf("%s: topic süzgeci hata vermedi — kapsam iddiası çürük", q.Key)
		}
		// Ucun gerçekte attığı çağrı: Topic BOŞ, kırılım servis + istemci.
		f, err := KafkaQuery(m, KafkaScope{Services: []string{"b", "a"}, From: from, To: to}, q.GroupBy)
		if err != nil {
			t.Errorf("%s: topic'siz sorgu kurulamadı: %v", q.Key, err)
			continue
		}
		for _, fe := range f.Filters {
			if fe.Key == "topic" {
				t.Errorf("%s: topic süzgeci sızmış: %+v", q.Key, f.Filters)
			}
		}
		if len(f.GroupBy) == 0 || f.GroupBy[0] != "service.name" {
			t.Errorf("%s: çok servisli sayfada kırılım service.name ile başlamalı (client_id servisler arası çakışır): %v", q.Key, f.GroupBy)
		}
	}
	for _, want := range []string{"producer_connection_count", "consumer_connection_count", "producer_request_latency_avg", "consumer_rebalance_rate", "consumer_last_poll", "consumer_fetch_latency_avg"} {
		if !seen[want] {
			t.Errorf("client-health seti %s sorusunu kaybetmiş", want)
		}
	}
}

// Türetme kaynağı mutasyona uğratmamalı: servis Infra paneli (v0.10.552)
// kırılımı client_id'dir, topic sayfası önek eklese bile değişmez.
func TestKafkaClientHealthDoesNotMutateServiceQuestions(t *testing.T) {
	before := map[string]string{}
	for _, q := range KafkaServiceQuestions() {
		before[q.Key] = strings.Join(q.GroupBy, ",")
	}
	_ = KafkaClientHealthQuestions()
	for _, q := range KafkaServiceQuestions() {
		if got := strings.Join(q.GroupBy, ","); got != before[q.Key] {
			t.Fatalf("%s: servis sorusu kırılımı değişti %q → %q", q.Key, before[q.Key], got)
		}
	}
	if got := before["producer_connection_count"]; got != "client_id" {
		t.Fatalf("servis paneli kırılımı client_id olmalı, geldi %q", got)
	}
}

// kafkaPickQuestions — eksik anahtar sessizce atlanır (uzunluk iddiaları bunu
// yakalar), önek tekrarı yutulur.
func TestKafkaPickQuestions(t *testing.T) {
	src := []KafkaQuestion{
		{Key: "a", Metric: "m1", TR: "A", GroupBy: []string{"client_id"}},
		{Key: "b", Metric: "m2", TR: "B", GroupBy: []string{"service.name", "client_id"}},
	}
	got := kafkaPickQuestions(src, []string{"service.name"}, "b", "yok", "a")
	if len(got) != 2 || got[0].Key != "b" || got[1].Key != "a" {
		t.Fatalf("seçim sırası anahtar listesini izlemeli: %+v", got)
	}
	if strings.Join(got[0].GroupBy, ",") != "service.name,client_id" {
		t.Fatalf("önek tekrarı yutulmalı: %v", got[0].GroupBy)
	}
	if strings.Join(got[1].GroupBy, ",") != "service.name,client_id" {
		t.Fatalf("önek başa eklenmeli: %v", got[1].GroupBy)
	}
	if strings.Join(src[1].GroupBy, ",") != "service.name,client_id" || strings.Join(src[0].GroupBy, ",") != "client_id" {
		t.Fatalf("kaynak liste mutasyona uğradı: %+v", src)
	}
}
