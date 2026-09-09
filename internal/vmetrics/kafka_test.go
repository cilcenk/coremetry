package vmetrics

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// v0.10.550 — Kafka client metrik kataloğu + soru→MetricQueryFilter üreticisi
// (docs/audit/messaging-kafka-metrics-2026-09-08.md Faz 1). Sözleşme:
//   - katalog adları OTel Java kafka-clients-metrics modülünün adları
//     (kafka.<producer|consumer>.<jmx>), tekil; `_total` = counter, gerisi gauge;
//   - gauge'a ASLA rate/increase uygulanmaz (Kafka'nın kendi pencere ortalaması);
//     counter'a yalnız rate;
//   - sorgu daima servis kapsamlı (kardinalite) ve pencereli; topic/client_id
//     süzgeci yalnız o label'ı taşıyan metrikte; groupBy yalnız bilinen label.
func TestKafkaCatalogIntegrity(t *testing.T) {
	seen := map[string]bool{}
	known := map[string]bool{"client_id": true, "topic": true, "partition": true, "node_id": true}
	for _, m := range KafkaCatalog {
		if seen[m.Name] {
			t.Errorf("%s: tekrar", m.Name)
		}
		seen[m.Name] = true
		if !strings.HasPrefix(m.Name, "kafka."+m.Side+".") {
			t.Errorf("%s: ad taraf (%s) ile uyuşmuyor", m.Name, m.Side)
		}
		isTotal := strings.HasSuffix(m.Name, "_total")
		if isTotal != (m.Kind == KafkaCounter) {
			t.Errorf("%s: kind=%s ama _total=%v", m.Name, m.Kind, isTotal)
		}
		switch m.Kind {
		case KafkaCounter:
			if m.Agg != "rate" {
				t.Errorf("%s: counter agg %q, rate olmalı", m.Name, m.Agg)
			}
		case KafkaGauge:
			if m.Agg == "rate" || m.Agg == "increase" {
				t.Errorf("%s: gauge'a %s uygulanamaz", m.Name, m.Agg)
			}
			// v0.10.582 — "min" eklendi. Kapının ASIL işi yukarıdaki
			// rate/increase yasağı; bu liste yalnız o gün kullanımda olan
			// toplamalardı. min hem promql.go hem metricquery.go tarafında
			// destekleniyor ve records_lead için TEK doğru toplama: lead'in
			// tehlikeli yönü aşağı, en kötü partition ortalamada kaybolur.
			if m.Agg != "sum" && m.Agg != "avg" && m.Agg != "max" && m.Agg != "min" {
				t.Errorf("%s: gauge agg %q", m.Name, m.Agg)
			}
		default:
			t.Errorf("%s: kind %q", m.Name, m.Kind)
		}
		for _, l := range m.Labels {
			if !known[l] {
				t.Errorf("%s: bilinmeyen label %q", m.Name, l)
			}
		}
		if m.TR == "" {
			t.Errorf("%s: UI etiketi boş", m.Name)
		}
		// VM'deki yazım (nokta→alt çizgi) ad adaylarında olmalı — seam bunu
		// nameCandidates ile dener; kafka.* için kanıt burada.
		under := strings.ReplaceAll(m.Name, ".", "_")
		found := false
		for _, c := range plainNameCandidates(m.Name) {
			if c == under {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: aday listesi %q taşımıyor: %v", m.Name, under, plainNameCandidates(m.Name))
		}
	}
	if _, ok := KafkaMetricByName("kafka.consumer.records_lag_max"); !ok {
		t.Fatal("records_lag_max katalogda olmalı")
	}
	if _, ok := KafkaMetricByName("kafka.consumer.io_ratio"); ok {
		t.Fatal("deprecated io_ratio katalogda olmamalı")
	}
}

func TestKafkaQuestionsResolve(t *testing.T) {
	// v0.10.575 — türetilmiş setler de katalogda çözülmeli ve anahtarları
	// kendi içinde tekil olmalı (ayrıntılı sözleşme: kafka_sets_test.go).
	for _, set := range [][]KafkaQuestion{KafkaTopicQuestions(), KafkaServiceQuestions(), KafkaTopicChartQuestions(), KafkaClientHealthQuestions()} {
		keys := map[string]bool{}
		for _, q := range set {
			if keys[q.Key] {
				t.Errorf("soru anahtarı tekrar: %s", q.Key)
			}
			keys[q.Key] = true
			m, ok := KafkaMetricByName(q.Metric)
			if !ok {
				t.Errorf("%s: metrik katalogda yok: %s", q.Key, q.Metric)
				continue
			}
			for _, g := range q.GroupBy {
				if g != "service.name" && g != "host.name" && !hasLabel(m, g) {
					t.Errorf("%s: groupBy %q metrik label'ı değil", q.Key, g)
				}
			}
		}
	}
	for _, q := range KafkaTopicQuestions() {
		m, _ := KafkaMetricByName(q.Metric)
		if !hasLabel(m, "topic") {
			t.Errorf("topic sorusu %s topic label'sız metrik kullanıyor: %s", q.Key, q.Metric)
		}
	}
}

func TestKafkaQuery(t *testing.T) {
	from := time.Date(2026, 9, 8, 6, 0, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)
	lag, _ := KafkaMetricByName("kafka.consumer.records_lag_max")
	conn, _ := KafkaMetricByName("kafka.consumer.connection_count")
	errTotal, _ := KafkaMetricByName("kafka.producer.record_error_total")

	f, err := KafkaQuery(lag, KafkaScope{Services: []string{"b", "a", "a"}, Topic: "orders", From: from, To: to}, []string{"service.name", "client_id"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != lag.Name || f.Aggregation != "max" || f.MaxDataPoints != KafkaDefaultMaxDataPoints {
		t.Fatalf("temel alanlar: %+v", f)
	}
	if len(f.Filters) != 2 || f.Filters[0].Key != "service.name" || f.Filters[0].Op != "IN" ||
		strings.Join(f.Filters[0].Values, ",") != "a,b" || f.Filters[1].Key != "topic" || f.Filters[1].Values[0] != "orders" {
		t.Fatalf("süzgeçler: %+v", f.Filters)
	}
	if strings.Join(f.GroupBy, ",") != "service.name,client_id" {
		t.Fatalf("groupBy: %v", f.GroupBy)
	}
	if f.Service != "" {
		t.Fatal("Service alanı tek servis içindir; kapsam Filters'ta taşınır")
	}
	if f2, err := KafkaQuery(errTotal, KafkaScope{Services: []string{"a"}, Topic: "orders", From: from, To: to}, nil); err != nil || f2.Aggregation != "rate" {
		t.Fatalf("counter → rate: %+v %v", f2, err)
	}
	bad := []struct {
		name string
		m    KafkaMetric
		sc   KafkaScope
		gb   []string
		want string
	}{
		{"servis yok", lag, KafkaScope{From: from, To: to}, nil, "servis"},
		{"pencere yok", lag, KafkaScope{Services: []string{"a"}}, nil, "pencere"},
		{"ters pencere", lag, KafkaScope{Services: []string{"a"}, From: to, To: from}, nil, "pencere"},
		{"topic label'sız metrikte topic", conn, KafkaScope{Services: []string{"a"}, Topic: "x", From: from, To: to}, nil, "topic"},
		{"bilinmeyen groupBy", lag, KafkaScope{Services: []string{"a"}, From: from, To: to}, []string{"node_id"}, "groupBy"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := KafkaQuery(c.m, c.sc, c.gb); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("hata bekleniyordu (%q), geldi: %v", c.want, err)
			}
		})
	}
	// Ölçek 1 servis: IN tek değer — promMatcher `=` üretir; seam sözleşmesi.
	f3, _ := KafkaQuery(conn, KafkaScope{Services: []string{"a"}, From: from, To: to, MaxDataPoints: 120}, []string{"client_id"})
	if f3.MaxDataPoints != 120 || len(f3.Filters) != 1 || len(f3.Filters[0].Values) != 1 {
		t.Fatalf("tek servis: %+v", f3)
	}
	names := make([]string, 0, len(KafkaCatalog))
	for _, m := range KafkaCatalog {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	if len(names) < 15 {
		t.Fatalf("katalog çok kısa: %d", len(names))
	}
}
