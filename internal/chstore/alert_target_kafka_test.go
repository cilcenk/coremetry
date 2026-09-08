package chstore

import (
	"strings"
	"testing"
)

// v0.10.554 — Kafka istemci hedefli kural (docs/audit/messaging-kafka-metrics
// -2026-09-08.md Faz 5). Sözleşme: kind=kafka_client; service ZORUNLU (kapsamsız
// kafka_* sorgusu tüm filoyu toplar), topic/clientId isteğe bağlı; metrik yalnız
// kafka_* ailesi ve kafka_* metriği hedefsiz olamaz; eşik > 0.
func TestValidateRuleTargetKafka(t *testing.T) {
	ok := AlertRule{Name: "lag", Metric: "kafka_lag_max", Threshold: 1000, Target: &RuleTarget{Kind: RuleTargetKafkaClient, Service: "loan-svc", Topic: "orders"}}
	if err := ValidateRuleTarget(ok); err != nil {
		t.Fatalf("geçerli hedef reddedildi: %v", err)
	}
	cases := []struct {
		name string
		mut  func(r *AlertRule)
		want string
	}{
		{"servis yok", func(r *AlertRule) { r.Target.Service = " " }, "service"},
		{"db metriği kafka hedefinde", func(r *AlertRule) { r.Metric = "db_stmt_p95_ms" }, "kafka_"},
		{"kafka metriği hedefsiz", func(r *AlertRule) { r.Target = nil }, "kafka_client"},
		{"eşik sıfır", func(r *AlertRule) { r.Threshold = 0 }, "threshold"},
		{"bilinmeyen metrik", func(r *AlertRule) { r.Metric = "kafka_bytes" }, "kafka_"},
		{"topic çok uzun", func(r *AlertRule) { r.Target.Topic = strings.Repeat("t", 300) }, "topic"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ok
			tg := *ok.Target
			r.Target = &tg
			c.mut(&r)
			err := ValidateRuleTarget(r)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("hata bekleniyordu (%q), geldi: %v", c.want, err)
			}
		})
	}
	// DB hedefi eskisi gibi: stmtHash zorunlu, kafka alanları görmezden gelinir.
	db := AlertRule{Metric: "db_stmt_p95_ms", Threshold: 100, Target: &RuleTarget{Kind: RuleTargetDBStatement, StmtHash: "42"}}
	if err := ValidateRuleTarget(db); err != nil {
		t.Fatalf("db hedefi: %v", err)
	}
	// Metrik eşlemesi: kural adı → OTel katalog adı.
	if KafkaTargetMetricName("kafka_lag_max") != "kafka.consumer.records_lag_max" ||
		KafkaTargetMetricName("kafka_producer_error_rate") != "kafka.producer.record_error_rate" ||
		KafkaTargetMetricName("db_stmt_p95_ms") != "" {
		t.Fatal("KafkaTargetMetricName eşlemesi")
	}
	// JSON gidiş-dönüş: kafka alanları taşınır, stmtHash boşsa yazılmaz.
	enc := encodeRuleTarget(ok.Target)
	if strings.Contains(enc, "stmtHash") || !strings.Contains(enc, `"service":"loan-svc"`) || !strings.Contains(enc, `"topic":"orders"`) {
		t.Fatalf("encode: %s", enc)
	}
	if d := decodeRuleTarget(enc); d == nil || d.Kind != RuleTargetKafkaClient || d.Service != "loan-svc" {
		t.Fatalf("decode: %+v", d)
	}
}
