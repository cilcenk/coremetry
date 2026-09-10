package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// v0.10.550 — Kafka istemci metrikleri (VM seam) — Faz 1. Sözleşme:
//   • kapsam SPAN tarafından gelir (caller MV: servis + rol); üretici soruları
//     üretici servislerle, tüketici soruları tüketici servislerle daraltılır;
//     rolü bilinmeyen servis iki kapsama da girer (üst küme, sessiz daraltma yok);
//   • her soru bağımsız blok: biri hata verse diğerleri gelir, hata blokta yazılı;
//   • available = en az bir seri; yoksa not sebebi söyler, sayfa span'a düşer;
//   • env VM'de ifade edilemezse envAmbiguous; caller yoksa hiç sorgu yok.
func kafkaFixtureWindow() (time.Time, time.Time) {
	to := time.Date(2026, 9, 8, 6, 30, 0, 0, time.UTC)
	return to.Add(-30 * time.Minute), to
}

// isKafkaDiscoveryQuery — v0.10.609: KafkaDiscoverFilter'ın şekli (yalnız
// topic süzgeci, service.name kırılımı, tek nokta). Soru sorgularının kapsam
// iddiaları bu sorgulara uygulanmaz — tam tersini yapmaları tasarım.
func isKafkaDiscoveryQuery(f chstore.MetricQueryFilter) bool {
	return f.MaxDataPoints == 1 && len(f.GroupBy) == 1 && f.GroupBy[0] == "service.name" &&
		len(f.Filters) == 1 && f.Filters[0].Key == "topic"
}

func kafkaSeries(n int) []chstore.SpanMetricSeries {
	out := make([]chstore.SpanMetricSeries, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, chstore.SpanMetricSeries{GroupKey: []string{"svc"}, Points: []chstore.SpanMetricPoint{{Time: 1, Value: float64(i)}}})
	}
	return out
}

func TestBuildMessagingClients(t *testing.T) {
	from, to := kafkaFixtureWindow()
	plan := messagingClientsPlan{System: "kafka", Cluster: "(default)", Destination: "orders", From: from, To: to, Mdp: 60}
	callers := []chstore.MsgCallerService{
		{Service: "producer-a", Role: "producer"}, {Service: "consumer-b", Role: "consumer"},
		{Service: "both-c", Role: "client"}, {Service: "producer-a", Role: "producer"},
	}
	src := &fakeEPSource{name: "vm", queryFn: func(f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		switch f.Name {
		case "kafka.consumer.records_lag_max":
			return kafkaSeries(2), nil
		case "kafka.producer.record_error_rate":
			return nil, errors.New("boom")
		}
		return nil, nil
	}}
	resp, err := buildMessagingClients(context.Background(), src, plan, callers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(resp.Producers, ",") != "both-c,producer-a" || strings.Join(resp.Consumers, ",") != "both-c,consumer-b" {
		t.Fatalf("kapsam: P=%v C=%v", resp.Producers, resp.Consumers)
	}
	if !resp.Available || resp.Source != "vm" {
		t.Fatalf("available/source: %+v", resp)
	}
	qs := vmetrics.KafkaTopicQuestions()
	// v0.10.609 — soru başına bir sorgu + 2 keşif sorgusu (üretici/tüketici
	// topic etiketli metrikten); blok sayısı soru sayısı.
	if len(src.calls) != len(qs)+2 || len(resp.Blocks) != len(qs) {
		t.Fatalf("soru başına bir sorgu + 2 keşif: calls=%d blocks=%d want=%d", len(src.calls), len(resp.Blocks), len(qs)+2)
	}
	lag := resp.Blocks["consumer_lag_max"]
	if len(lag.Series) != 2 || lag.Error != "" || lag.Unit == "" || lag.Label == "" {
		t.Fatalf("lag bloğu: %+v", lag)
	}
	if e := resp.Blocks["producer_error_rate"]; e.Error == "" || len(e.Series) != 0 {
		t.Fatalf("hata bloğu: %+v", e)
	}
	// Her sorgu topic süzgeci + servis kapsamı taşır; tüketici sorusu tüketici
	// servislerini, üretici sorusu üreticileri (rolü bilinmeyen ikisinde de).
	for i, f := range src.queries {
		if isKafkaDiscoveryQuery(f) {
			continue // v0.10.609 — keşif sorgusu: yalnız topic, servissiz
		}
		var topic, svcs []string
		for _, fe := range f.Filters {
			switch fe.Key {
			case "topic":
				topic = fe.Values
			case "service.name":
				svcs = fe.Values
			}
		}
		if len(topic) != 1 || topic[0] != "orders" {
			t.Errorf("sorgu %d topic süzgeci: %+v", i, f.Filters)
		}
		m, _ := vmetrics.KafkaMetricByName(f.Name)
		want := resp.Consumers
		if m.Side == "producer" {
			want = resp.Producers
		}
		if strings.Join(svcs, ",") != strings.Join(want, ",") {
			t.Errorf("sorgu %d (%s) servis kapsamı %v, bekl. %v", i, f.Name, svcs, want)
		}
	}
	if !strings.Contains(resp.Note, "vm") {
		t.Fatalf("not kaynağı söylemeli: %q", resp.Note)
	}
}

func TestBuildMessagingClients_NoSeriesAndNoCallers(t *testing.T) {
	from, to := kafkaFixtureWindow()
	plan := messagingClientsPlan{System: "kafka", Cluster: "(default)", Destination: "orders", Env: "prod", From: from, To: to}
	src := &fakeEPSource{name: "vm", queryFn: func(chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) { return nil, nil }}
	resp, err := buildMessagingClients(context.Background(), src, plan, []chstore.MsgCallerService{{Service: "x", Role: "consumer"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Available || !resp.EnvAmbiguous || !strings.Contains(resp.Note, "bulunamadı") || !strings.Contains(resp.Note, "env") {
		t.Fatalf("seri yok: %+v", resp)
	}
	if len(src.calls) == 0 {
		t.Fatal("caller varken sorgu atılmalı")
	}
	// v0.10.609 — caller yokken yalnız KEŞİF sorguları (topic etiketli send/
	// consumed rate) atılır; keşif de boşsa soru sorgusu yok.
	src2 := &fakeEPSource{name: "vm", queryFn: func(f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		if f.Name == "kafka.producer.record_send_rate" || f.Name == "kafka.consumer.records_consumed_rate" {
			return nil, nil
		}
		t.Fatalf("caller ve keşif yokken soru sorgusu atılmamalı: %s", f.Name)
		return nil, nil
	}}
	resp2, err := buildMessagingClients(context.Background(), src2, plan, nil)
	if err != nil || resp2.Available || !strings.Contains(resp2.Note, "üretici/tüketici") {
		t.Fatalf("caller yok: %+v %v", resp2, err)
	}
	if resp2.Producers == nil || resp2.Consumers == nil || resp2.Blocks == nil {
		t.Fatal("boş dilimler null değil [] olmalı (FE .map)")
	}
}

func TestBuildServiceKafkaClients(t *testing.T) {
	from, to := kafkaFixtureWindow()
	src := &fakeEPSource{name: "vm", envOK: true, queryFn: func(f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		if f.Name == "kafka.producer.connection_count" {
			return kafkaSeries(1), nil
		}
		return nil, nil
	}}
	resp, err := buildServiceKafkaClients(context.Background(), src, serviceKafkaClientsPlan{Service: "svc-a", Env: "prod", From: from, To: to, Mdp: 60})
	if err != nil {
		t.Fatal(err)
	}
	qs := vmetrics.KafkaServiceQuestions()
	if len(src.queries) != len(qs) || !resp.Available || resp.EnvAmbiguous {
		t.Fatalf("servis kapsamı: q=%d want=%d %+v", len(src.queries), len(qs), resp)
	}
	for _, f := range src.queries {
		var svc, env bool
		for _, fe := range f.Filters {
			if fe.Key == "service.name" && len(fe.Values) == 1 && fe.Values[0] == "svc-a" {
				svc = true
			}
			if fe.Key == "deployment.environment" {
				env = true
			}
		}
		if !svc || !env {
			t.Errorf("%s: servis=%v env=%v süzgeç: %+v", f.Name, svc, env, f.Filters)
		}
	}
}

// Cache anahtarı — SAF, tüm girdiler (v0.5.187 sınıfı).
func TestMessagingClientsKey(t *testing.T) {
	from, to := kafkaFixtureWindow()
	base := messagingClientsPlan{System: "kafka", Cluster: "c1", Destination: "orders", Env: "prod", From: from, To: to, Mdp: 60}
	k0 := messagingClientsKey(base, "vm", "mx0")
	if k0 != messagingClientsKey(base, "vm", "mx0") {
		t.Fatal("kararlı değil")
	}
	muts := []func(p *messagingClientsPlan){
		func(p *messagingClientsPlan) { p.System = "rabbitmq" },
		func(p *messagingClientsPlan) { p.Cluster = "c2" },
		func(p *messagingClientsPlan) { p.Destination = "payments" },
		func(p *messagingClientsPlan) { p.Env = "" },
		func(p *messagingClientsPlan) { p.Mdp = 30 },
		func(p *messagingClientsPlan) { p.To = p.To.Add(10 * time.Minute) },
	}
	seen := map[string]bool{k0: true}
	for i, m := range muts {
		p := base
		m(&p)
		k := messagingClientsKey(p, "vm", "mx0")
		if seen[k] {
			t.Fatalf("mutasyon %d anahtarı değiştirmedi: %s", i, k)
		}
		seen[k] = true
	}
	if messagingClientsKey(base, "ch", "mx0") == k0 || messagingClientsKey(base, "vm", "mx1") == k0 {
		t.Fatal("src / mx anahtarda olmalı")
	}
	sk := serviceKafkaClientsKey(serviceKafkaClientsPlan{Service: "a", Env: "prod", From: from, To: to, Mdp: 60}, "vm", "mx0")
	if sk == messagingClientsKey(base, "vm", "mx0") || !strings.HasPrefix(sk, "svc-kafka-clients:") {
		t.Fatalf("servis anahtarı: %s", sk)
	}
}

// v0.10.609 — operatör-bildirimi: kapsam yalnız span'dan geliyordu; log
// topic'inin tüketicisi span üretmiyor ama topic etiketli kafka-clients
// metriği üretiyor → tüm tüketici panelleri "kapsam boş". Sözleşme: kapsam =
// span ∪ topic etiketli metrikten keşif; keşfedilenler cevapta ayrı ve
// notta sayılı; keşif hatası cevabı düşürmez; tavan span'i önceler.
func TestBuildMessagingClients_DiscoversScopeFromTopicMetrics(t *testing.T) {
	from, to := kafkaFixtureWindow()
	plan := messagingClientsPlan{System: "kafka", Cluster: "(default)", Destination: "orders", From: from, To: to, Mdp: 60, Set: msgSetClients}
	callers := []chstore.MsgCallerService{{Service: "producer-a", Role: "producer"}}
	ser := func(names ...string) []chstore.SpanMetricSeries {
		out := []chstore.SpanMetricSeries{}
		for _, n := range names {
			out = append(out, chstore.SpanMetricSeries{GroupKey: []string{n}, Points: []chstore.SpanMetricPoint{{Time: 1, Value: 1}}})
		}
		return out
	}
	src := &fakeEPSource{name: "vm", queryFn: func(f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		switch f.Name {
		case "kafka.consumer.records_consumed_rate":
			if len(f.Filters) != 1 || f.Filters[0].Key != "topic" || f.Filters[0].Values[0] != "orders" ||
				strings.Join(f.GroupBy, ",") != "service.name" || f.MaxDataPoints != 1 || !f.PlainSeries {
				t.Fatalf("keşif süzgeci: %+v", f)
			}
			return ser("consumer-y", "consumer-x", "consumer-x", ""), nil
		case "kafka.producer.record_send_rate":
			return ser("producer-a", "producer-z"), nil
		}
		return ser("svc"), nil
	}}
	resp, err := buildMessagingClients(context.Background(), src, plan, callers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(resp.Consumers, ",") != "consumer-x,consumer-y" || strings.Join(resp.DiscoveredConsumers, ",") != "consumer-x,consumer-y" {
		t.Fatalf("tüketici kapsamı keşiften gelmeli: C=%v D=%v", resp.Consumers, resp.DiscoveredConsumers)
	}
	if strings.Join(resp.Producers, ",") != "producer-a,producer-z" || strings.Join(resp.DiscoveredProducers, ",") != "producer-z" {
		t.Fatalf("üretici kapsamı span ∪ keşif: P=%v D=%v", resp.Producers, resp.DiscoveredProducers)
	}
	if !strings.Contains(resp.Note, "keşfedilen 1 üretici / 2 tüketici") {
		t.Fatalf("not keşfi saymalı: %s", resp.Note)
	}
	// Tüketici soruları keşfedilen kapsamla SORULDU (kapsam boş değil).
	askedConsumer := false
	for _, q := range src.queries {
		if q.Name == "kafka.consumer.connection_count" {
			askedConsumer = true
			if len(q.Filters) == 0 || q.Filters[0].Key != "service.name" || strings.Join(q.Filters[0].Values, ",") != "consumer-x,consumer-y" {
				t.Fatalf("tüketici sorusu keşfedilen kapsamla gitmeli: %+v", q.Filters)
			}
		}
	}
	if !askedConsumer {
		t.Fatal("tüketici bağlantı sorusu sorulmalıydı")
	}
	if b := resp.Blocks["consumer_connection_count"]; b.Error != "" {
		t.Fatalf("keşfedilen kapsamla blok hatasız olmalı: %q", b.Error)
	}

	// Keşif hatası: kapsam span'da kalır, cevap düşmez, tüketici bloğu dürüst "kapsam boş".
	src2 := &fakeEPSource{name: "vm", queryFn: func(f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		if f.Name == "kafka.consumer.records_consumed_rate" || f.Name == "kafka.producer.record_send_rate" {
			return nil, errors.New("vm down")
		}
		return ser("svc"), nil
	}}
	resp2, err := buildMessagingClients(context.Background(), src2, plan, callers)
	if err != nil || strings.Join(resp2.Producers, ",") != "producer-a" || len(resp2.Consumers) != 0 || len(resp2.DiscoveredProducers) != 0 {
		t.Fatalf("keşif hatası kapsamı span'da bırakır: %+v %v", resp2, err)
	}
	if b := resp2.Blocks["consumer_connection_count"]; !strings.Contains(b.Error, "kapsam boş") || !strings.Contains(b.Error, "metrikte de") {
		t.Fatalf("tüketici bloğu dürüst kapsam-boş demeli: %q", b.Error)
	}
}

func TestMergeKafkaScope(t *testing.T) {
	all, added, trunc := mergeKafkaScope([]string{"b", "a", "a", ""}, []string{"c", "a", "d"}, 10)
	if strings.Join(all, ",") != "a,b,c,d" || strings.Join(added, ",") != "c,d" || trunc {
		t.Fatalf("birleşim: all=%v added=%v trunc=%v", all, added, trunc)
	}
	// Tavan: span servisleri ÖNCE, keşfedilenler kalan yere; kırpma ilan edilir.
	all, added, trunc = mergeKafkaScope([]string{"s1", "s2", "s3"}, []string{"d1", "d2", "d3"}, 4)
	if len(all) != 4 || strings.Join(added, ",") != "d1" || !trunc {
		t.Fatalf("tavan: all=%v added=%v trunc=%v", all, added, trunc)
	}
	for _, s := range []string{"s1", "s2", "s3"} {
		if !strings.Contains(strings.Join(all, ","), s) {
			t.Fatalf("span servisi tavanda düşmez: %v", all)
		}
	}
	if all, added, trunc = mergeKafkaScope(nil, nil, 5); len(all) != 0 || len(added) != 0 || trunc || all == nil {
		t.Fatalf("boş: %v %v %v", all, added, trunc)
	}
}
