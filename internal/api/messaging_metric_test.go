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
	if len(src.calls) != len(qs) || len(resp.Blocks) != len(qs) {
		t.Fatalf("soru başına bir sorgu: calls=%d blocks=%d want=%d", len(src.calls), len(resp.Blocks), len(qs))
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
	src2 := &fakeEPSource{name: "vm", queryFn: func(chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) { t.Fatal("caller yokken sorgu atılmamalı"); return nil, nil }}
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
