package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.10.331 — hedefli kural: doğrulama tablosu, codec gidiş-dönüş, metrik →
// ölçü, SQL sözleşmeleri.
func TestValidateRuleTarget(t *testing.T) {
	ok := AlertRule{Metric: "db_stmt_p95_ms", Threshold: 1000, Target: &RuleTarget{Kind: RuleTargetDBStatement, StmtHash: "123", DBSystem: "oracle"}}
	if err := ValidateRuleTarget(ok); err != nil {
		t.Fatalf("geçmeli: %v", err)
	}
	for _, tc := range []struct {
		n string
		r AlertRule
	}{
		{"metrik hedefsiz", AlertRule{Metric: "db_stmt_p95_ms", Threshold: 1}},
		{"kind", AlertRule{Metric: "db_stmt_p95_ms", Threshold: 1, Target: &RuleTarget{Kind: "x", StmtHash: "1"}}},
		{"hash", AlertRule{Metric: "db_stmt_p95_ms", Threshold: 1, Target: &RuleTarget{Kind: RuleTargetDBStatement, StmtHash: "abc"}}},
		{"metrik", AlertRule{Metric: "p99_ms", Threshold: 1, Target: &RuleTarget{Kind: RuleTargetDBStatement, StmtHash: "1"}}},
		{"eşik", AlertRule{Metric: "db_stmt_max_ms", Threshold: 0, Target: &RuleTarget{Kind: RuleTargetDBStatement, StmtHash: "1"}}},
	} {
		if err := ValidateRuleTarget(tc.r); err == nil {
			t.Errorf("%s: hata bekleniyordu", tc.n)
		}
	}
	if err := ValidateRuleTarget(AlertRule{Metric: "p99_ms", Threshold: 1}); err != nil {
		t.Errorf("hedefsiz sıradan kural geçmeli: %v", err)
	}
}

func TestRuleTargetCodecAndMetricValue(t *testing.T) {
	in := &RuleTarget{Kind: RuleTargetDBStatement, DBSystem: "oracle", DBName: "CRD", StmtHash: "42", Sample: "SELECT 1"}
	out := decodeRuleTarget(encodeRuleTarget(in))
	if out == nil || *out != *in {
		t.Errorf("codec: %+v", out)
	}
	if decodeRuleTarget("") != nil || decodeRuleTarget("{bad") != nil || encodeRuleTarget(nil) != "" {
		t.Error("boş/bozuk → nil, nil → ''")
	}
	st := StatementWindowStats{P95Ms: 1, P99Ms: 2, MaxMs: 3, AvgMs: 4}
	for m, want := range map[string]float64{"db_stmt_p95_ms": 1, "db_stmt_p99_ms": 2, "db_stmt_max_ms": 3, "db_stmt_avg_ms": 4, "other": 1} {
		if got := TargetMetricValue(st, m); got != want {
			t.Errorf("%s = %v, want %v", m, got, want)
		}
	}
}

func TestStatementSQLContracts(t *testing.T) {
	w := statementWindowSQL(true, false)
	for _, s := range []string{"FROM db_statement_summary_5m", "stmt_hash = ?", "time_bucket >= ?", "AND db_system = ?", "maxMerge(duration_max_state)", "max_execution_time = 10"} {
		if !strings.Contains(w, s) {
			t.Errorf("window SQL: %q yok", s)
		}
	}
	if strings.Contains(w, "db_name = ?") || strings.Count(w, "?") != 3 {
		t.Errorf("window SQL bind sayısı: %s", w)
	}
	// v0.10.515 (operator-reported, prod 241 / 3.73 GiB): süzgeç SATIR
	// düzeyinde WHERE'de, HAVING yok; örnek kırpık; grup tavanı; servis
	// kapsamı isteğe bağlı ve bind sırası (service, q, limit).
	q := statementSearchSQL(false)
	for _, s := range []string{
		"INTERVAL 24 HOUR", "GROUP BY db_system, db_name, stmt_hash",
		"AND positionCaseInsensitiveUTF8(finalizeAggregation(sample_stmt_state), ?) > 0",
		"any(substring(finalizeAggregation(sample_stmt_state), 1, 400))",
		"LIMIT ?", "max_execution_time = 10", "max_rows_to_group_by = 50000", "group_by_overflow_mode = 'any'",
	} {
		if !strings.Contains(q, s) {
			t.Errorf("search SQL: %q yok", s)
		}
	}
	if strings.Contains(q, "HAVING") || strings.Contains(q, "anyMerge(sample_stmt_state)") || strings.Contains(q, "service_name = ?") || strings.Count(q, "?") != 2 {
		t.Errorf("search SQL (servissiz) biçimi: %s", q)
	}
	qs := statementSearchSQL(true)
	if !strings.Contains(qs, "AND service_name = ?") || strings.Count(qs, "?") != 3 ||
		strings.Index(qs, "service_name = ?") > strings.Index(qs, "finalizeAggregation(sample_stmt_state), ?") {
		t.Errorf("search SQL (servisli) bind sırası service, q, limit olmalı: %s", qs)
	}
	t.Logf("SEARCH_SQL_SVC: %s", strings.Join(strings.Fields(qs), " "))
}

// v0.10.334 — kolon yokken kayıt reddetmeden önce yeniden probe eder (kaynak pini);
// bayrak atomic.
func TestUpsertReprobesTargetColumn(t *testing.T) {
	b, err := readRepoFile("problem.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b, "!s.hasAlertRuleTargetCol.Load() && !s.probeAlertRuleTargetCol(ctx)") {
		t.Error("UpsertAlertRule reddetmeden önce yeniden probe etmeli")
	}
	st, err := readRepoFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st, "hasAlertRuleTargetCol atomic.Bool") {
		t.Error("bayrak atomic olmalı (kayıt yolunda yazılıyor)")
	}
}

func readRepoFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
