package chstore

import (
	"strings"
	"testing"
)

// v0.10.706 — kategori türetimi: her üretici ailesi beklenen sınıfa düşer;
// görüntü kimliği kararlı, biçimli, tanınır ve normalize edilir.
func TestProblemCategory(t *testing.T) {
	cases := []struct {
		name string
		p    Problem
		want string
	}{
		{"anomali error_rate", Problem{RuleID: "anomaly:shop:error_rate", Metric: "error_rate", Comparator: ">"}, CategoryError},
		{"anomali p99", Problem{RuleID: "anomaly:shop:p99_ms", Metric: "p99_ms", Comparator: ">"}, CategorySlowdown},
		{"anomali trafik çöküşü", Problem{RuleID: "anomaly:shop:request_rate", Metric: "request_rate", Comparator: "<"}, CategoryAvailability},
		{"anomali trafik sıçraması", Problem{RuleID: "anomaly:shop:request_rate", Metric: "request_rate", Comparator: ">"}, CategoryResource},
		{"service silent", Problem{RuleID: "anomaly:shop:service_silent", Metric: "request_rate", Comparator: "<"}, CategoryAvailability},
		{"anomali kümesi", Problem{RuleID: "anomaly-cluster:shop-db", Metric: "cluster", Comparator: ">"}, CategoryCustom},
		{"exception fırtınası", Problem{RuleID: "exception-storm", Metric: "exception_storm"}, CategoryError},
		{"ext down", Problem{RuleID: "anomaly:ext-down:oracle", Metric: "ext:source_down", Kind: ProblemKindExternal}, CategoryAvailability},
		{"ext cap", Problem{RuleID: "anomaly:ext-cap:oracle:x", Metric: "ext:x", Kind: ProblemKindExternal}, CategoryResource},
		{"ext seri", Problem{RuleID: "anomaly:ext:oracle:errors", Metric: "ext:errors", Kind: ProblemKindExternal}, CategoryCustom},
		{"alert rule p95", Problem{RuleID: "a1b2", Metric: "p95_ms", Comparator: ">"}, CategorySlowdown},
		{"alert rule error_count", Problem{RuleID: "a1b2", Metric: "error_count", Comparator: ">="}, CategoryError},
		{"watcher", Problem{RuleID: "w1", Metric: "watcher"}, CategoryError},
		{"log_query", Problem{RuleID: "w2", Metric: "log_query"}, CategoryError},
		{"db statement hedefi", Problem{RuleID: "r", Metric: "db_stmt_p95_ms", Kind: ProblemKindDB}, CategorySlowdown},
		{"kafka lag", Problem{RuleID: "r", Metric: "kafka_lag_max"}, CategoryResource},
		{"kafka producer err", Problem{RuleID: "r", Metric: "kafka_producer_error_rate"}, CategoryError},
		{"http route p99", Problem{RuleID: "r", Metric: "http_route_p99_ms"}, CategorySlowdown},
		{"http route err", Problem{RuleID: "r", Metric: "http_route_error_rate"}, CategoryError},
		{"http route rate düşüş", Problem{RuleID: "r", Metric: "http_route_rate", Comparator: "<"}, CategoryAvailability},
		{"anomali oto-terfi", Problem{RuleID: "anomaly-auto:ev1", Metric: "anomaly_ratio"}, CategoryError},
		{"slo burn", Problem{RuleID: "slo:s1:critical", Metric: "burn_rate_60m"}, CategoryCustom},
		{"db kapasite", Problem{RuleID: "db-capacity:c1", Metric: "db.capacity", Kind: ProblemKindDB}, CategoryResource},
		{"db yavaş sql", Problem{RuleID: "db-slow-stmt", Metric: "db.statement_p95_ms", Kind: ProblemKindDB}, CategorySlowdown},
		{"jvm gc", Problem{RuleID: "runtime:jvm-gc", Metric: "runtime.jvm_gc_pause_ms"}, CategoryResource},
		{"fatal exception", Problem{RuleID: "exception:fatal-infrastructure", Metric: "exception.fatal_infrastructure"}, CategoryError},
		{"shared dependency", Problem{RuleID: "exception:shared-dependency", Metric: "exception.shared_dependency"}, CategoryError},
		{"self ingest stall", Problem{RuleID: "self-ingest-stall", Metric: "ingest_lag_s"}, CategoryAvailability},
		{"self disk eta", Problem{RuleID: "self-disk-eta", Metric: "disk_eta_days", Comparator: "<"}, CategoryResource},
		{"monitor", Problem{RuleID: "monitor:m1", Metric: "uptime", Comparator: "<"}, CategoryAvailability},
		{"mq error", Problem{RuleID: "r", Metric: "mq_publish_error_rate"}, CategoryError},
		{"mq p99", Problem{RuleID: "r", Metric: "mq_process_p99_ms"}, CategorySlowdown},
		{"bilinmeyen", Problem{RuleID: "x", Metric: "whatever"}, CategoryCustom},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ProblemCategory(c.p); got != c.want {
				t.Fatalf("%s: %s, beklenen %s", c.name, got, c.want)
			}
		})
	}
	seen := map[string]bool{}
	for _, c := range ProblemCategories {
		seen[c] = true
	}
	if len(seen) != 5 {
		t.Fatal("beş kategori")
	}
}

func TestProblemDisplayID(t *testing.T) {
	a, b := ProblemDisplayID("3fa9c2d1e4b5a6f7"), ProblemDisplayID("3fa9c2d1e4b5a6f7")
	if a != b || !strings.HasPrefix(a, "P-") || len(a) > 9 || !IsProblemDisplayID(a) {
		t.Fatalf("kararlı + biçimli: %q %q", a, b)
	}
	if ProblemDisplayID("3fa9c2d1e4b5a6f8") == a {
		t.Fatal("farklı id aynı sap")
	}
	if ProblemDisplayID("  ") != "" {
		t.Fatal("boş id → boş")
	}
	for _, bad := range []string{"3fa9c2d1e4b5a6f7", "P-", "P-12345678", "Q-abc", "P-ab cd", ""} {
		if IsProblemDisplayID(bad) {
			t.Errorf("%q görüntü kimliği sayılmamalı", bad)
		}
	}
	if !IsProblemDisplayID("p-3F9A2") || NormalizeProblemDisplayID("p-3F9A2") != "P-3f9a2" {
		t.Fatal("harf duyarsız kabul + kanonik küçük harf")
	}
	// Determinizm: anomaly (16 kr), evaluator (24 kr) ve deterministik id'ler aynı yoldan.
	for _, id := range []string{"db-capacity:c1:inst", "anomaly-cluster:shop-db", "runtime:jvm-gc:svc:pod"} {
		if d := ProblemDisplayID(id); !IsProblemDisplayID(d) {
			t.Errorf("%s → %s", id, d)
		}
	}
}
