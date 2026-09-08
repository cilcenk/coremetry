package evaluator

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.554 — Kafka hedefli kural: pencere değeri = serilerin en kötü noktası
// (lag için maks; gauge'lar zaten kova başına toplanmış), örnek sayısı = nokta
// sayısı (MinSamples bununla karşılaştırılır); açıklama servis · topic · eşik.
func TestKafkaTargetValueAndDescribe(t *testing.T) {
	series := []chstore.SpanMetricSeries{
		{GroupKey: nil, Points: []chstore.SpanMetricPoint{{Time: 1, Value: 120}, {Time: 2, Value: 980}, {Time: 3, Value: 300}}},
		{GroupKey: nil, Points: []chstore.SpanMetricPoint{{Time: 1, Value: 50}}},
	}
	v, n := kafkaTargetValue(series)
	if v != 980 || n != 4 {
		t.Fatalf("değer/örnek: %v %d", v, n)
	}
	if v, n := kafkaTargetValue(nil); v != 0 || n != 0 {
		t.Fatal("boş seri → 0, 0")
	}
	r := chstore.AlertRule{Name: "Lag · orders", Metric: "kafka_lag_max", Comparator: ">", Threshold: 500, WindowSec: 600,
		Target: &chstore.RuleTarget{Kind: chstore.RuleTargetKafkaClient, Service: "loan-svc", Topic: "orders", ClientID: "c1"}}
	d := describeKafkaTargetProblem(r, 980, 4)
	for _, want := range []string{"loan-svc", "orders", "c1", "980", "> 500", "600s", "lag"} {
		if !strings.Contains(d, want) {
			t.Errorf("açıklama %q taşımalı: %s", want, d)
		}
	}
	if !strings.Contains(d, "istemcinin gördüğü") {
		t.Errorf("lag açıklaması doğruluk notu taşımalı (group lag değil): %s", d)
	}
	if mdp := kafkaTargetMaxDataPoints(600); mdp != 10 {
		t.Fatalf("600 s → 10 kova, geldi %d", mdp)
	}
	if mdp := kafkaTargetMaxDataPoints(36000); mdp != 60 {
		t.Fatalf("tavan 60, geldi %d", mdp)
	}
	if mdp := kafkaTargetMaxDataPoints(30); mdp != 1 {
		t.Fatalf("taban 1, geldi %d", mdp)
	}
}
