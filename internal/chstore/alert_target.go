package chstore

// alert_target.go — v0.10.331 (operatör 2026-09-03: "belirli bir SQL sorgusu
// 1 saniyeyi ya da belirtilen eşiği geçince alarm üretecek özel kural";
// spec onayı: p95 varsayılan / max seçilebilir, kimlik = stmt_hash, kapsam =
// ifadeyi çağıran tüm servisler, /alerts'te SQL arayıp seçme).
//
// AlertRule ailesine HEDEFLİ kural türü: rule.Target (alert_rules.target_json)
// bir DB ifadesini (db_system, db_name, stmt_hash) gösterir; metrik
// db_stmt_{p95,p99,max,avg}_ms, ölçü db_statement_summary_5m'den (tDigest
// merge, pencere 5 dk kovaya hizalı). Problem öznesi DBSubjectID → Kind=db →
// mevcut kanallar + DB sahibi/SRE maili. Bu dosya: hedef codec, doğrulama,
// pencere ölçüsü ve SQL arama; karar evaluator/alert_target.go'da.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const RuleTargetDBStatement = "db_statement"

// RuleTarget — kuralın hedefi. Sample yalnız görüntü (normalize SQL, kısa).
type RuleTarget struct {
	Kind     string `json:"kind"`
	DBSystem string `json:"dbSystem,omitempty"`
	DBName   string `json:"dbName,omitempty"`
	StmtHash string `json:"stmtHash"`
	Sample   string `json:"sample,omitempty"`
}

// ErrRuleTargetColumnMissing — küme kipinde iki-boot sözleşmesi: kolon
// ertelenmiş DDL ile gelir; gelene dek hedefli kural KAYDEDİLEMEZ (409).
var ErrRuleTargetColumnMissing = errors.New("alert_rules.target_json column not yet available — the deferred DDL has not landed yet; retry in a minute (a restart is not required once it lands)")

func IsDBStatementMetric(m string) bool {
	switch m {
	case "db_stmt_p95_ms", "db_stmt_p99_ms", "db_stmt_max_ms", "db_stmt_avg_ms":
		return true
	}
	return false
}

// ValidateRuleTarget — hedef ile kuralın tutarlılığı (API 400 kaynağı).
func ValidateRuleTarget(r AlertRule) error {
	if r.Target == nil {
		if IsDBStatementMetric(r.Metric) {
			return fmt.Errorf("metric %s requires a db_statement target", r.Metric)
		}
		return nil
	}
	t := r.Target
	if t.Kind != RuleTargetDBStatement {
		return fmt.Errorf("unknown target kind %q", t.Kind)
	}
	if _, err := strconv.ParseUint(strings.TrimSpace(t.StmtHash), 10, 64); err != nil || strings.TrimSpace(t.StmtHash) == "" {
		return fmt.Errorf("target.stmtHash must be a decimal uint64 statement hash")
	}
	if !IsDBStatementMetric(r.Metric) {
		return fmt.Errorf("db_statement target requires a db_stmt_* metric, got %q", r.Metric)
	}
	if r.Threshold <= 0 {
		return fmt.Errorf("threshold must be > 0 ms")
	}
	if len(t.Sample) > 2000 {
		return fmt.Errorf("target.sample too long")
	}
	return nil
}

func encodeRuleTarget(t *RuleTarget) string {
	if t == nil {
		return ""
	}
	b, err := json.Marshal(t)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeRuleTarget(raw string) *RuleTarget {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var t RuleTarget
	if err := json.Unmarshal([]byte(raw), &t); err != nil || t.Kind == "" {
		return nil
	}
	return &t
}

// StatementWindowStats — bir ifadenin pencere ölçüsü (tüm çağıranlar).
type StatementWindowStats struct {
	Count    uint64
	P95Ms    float64
	P99Ms    float64
	MaxMs    float64
	AvgMs    float64
	Sample   string
	Exemplar string
	Services []string
}

// TargetMetricValue — kural metriği → ölçü. Saf.
func TargetMetricValue(st StatementWindowStats, metric string) float64 {
	switch metric {
	case "db_stmt_p99_ms":
		return st.P99Ms
	case "db_stmt_max_ms":
		return st.MaxMs
	case "db_stmt_avg_ms":
		return st.AvgMs
	default:
		return st.P95Ms
	}
}

func statementWindowSQL(withSystem, withName bool) string {
	sql := `
		SELECT countMerge(span_count_state)                                                    AS execs,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms,
		       maxMerge(duration_max_state) / 1e6                                             AS max_ms,
		       sumMerge(duration_sum_state) / nullIf(countMerge(span_count_state), 0) / 1e6   AS avg_ms,
		       anyMerge(sample_stmt_state)                                                    AS sample_stmt,
		       argMaxMerge(slow_exemplar_state)                                               AS exemplar,
		       groupUniqArray(10)(service_name)                                               AS services
		FROM db_statement_summary_5m
		WHERE stmt_hash = ? AND time_bucket >= ?`
	if withSystem {
		sql += ` AND db_system = ?`
	}
	if withName {
		sql += ` AND db_name = ?`
	}
	return sql + `
		SETTINGS max_execution_time = 10`
}

// StatementWindowStats — son `window` (≥ 5 dk, 5 dk kovaya hizalı).
func (s *Store) StatementWindowStats(ctx context.Context, t RuleTarget, window time.Duration) (StatementWindowStats, error) {
	var st StatementWindowStats
	if !s.hasDBStmtHashCol {
		return st, errors.New("db_stmt_hash column missing — db_statement_summary_5m unavailable")
	}
	hash, err := strconv.ParseUint(strings.TrimSpace(t.StmtHash), 10, 64)
	if err != nil {
		return st, fmt.Errorf("bad stmtHash %q", t.StmtHash)
	}
	if window < 5*time.Minute {
		window = 5 * time.Minute
	}
	since := time.Now().Add(-window).Truncate(5 * time.Minute)
	args := []any{hash, since}
	if t.DBSystem != "" {
		args = append(args, t.DBSystem)
	}
	if t.DBName != "" {
		args = append(args, t.DBName)
	}
	var avg *float64
	err = s.telemetryReadConn().QueryRow(ctx, statementWindowSQL(t.DBSystem != "", t.DBName != ""), args...).
		Scan(&st.Count, &st.P95Ms, &st.P99Ms, &st.MaxMs, &avg, &st.Sample, &st.Exemplar, &st.Services)
	if err != nil {
		return st, err
	}
	if avg != nil {
		st.AvgMs = *avg
	}
	return st, nil
}

// StatementSearchRow — /alerts SQL arama seçici satırı.
type StatementSearchRow struct {
	DBSystem string   `json:"dbSystem"`
	DBName   string   `json:"dbName"`
	StmtHash string   `json:"stmtHash"`
	Sample   string   `json:"sample"`
	Execs    uint64   `json:"execs"`
	P95Ms    float64  `json:"p95Ms"`
	Services []string `json:"services"`
}

func statementSearchSQL() string {
	return `
		SELECT db_system, db_name, stmt_hash,
		       anyMerge(sample_stmt_state)                                                    AS sample_stmt,
		       countMerge(span_count_state)                                                   AS execs,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       groupUniqArray(5)(service_name)                                                AS services
		FROM db_statement_summary_5m
		WHERE time_bucket >= now() - INTERVAL 24 HOUR
		GROUP BY db_system, db_name, stmt_hash
		HAVING positionCaseInsensitiveUTF8(sample_stmt, ?) > 0
		ORDER BY execs DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`
}

// SearchStatements — son 24 saatte örnek SQL'i q'yu içeren ifadeler.
func (s *Store) SearchStatements(ctx context.Context, q string, limit int) ([]StatementSearchRow, error) {
	q = strings.TrimSpace(q)
	if len(q) < 3 {
		return []StatementSearchRow{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	if !s.hasDBStmtHashCol {
		return []StatementSearchRow{}, nil
	}
	rows, err := s.telemetryReadConn().Query(ctx, statementSearchSQL(), q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatementSearchRow{}
	for rows.Next() {
		var r StatementSearchRow
		var hash uint64
		if err := rows.Scan(&r.DBSystem, &r.DBName, &hash, &r.Sample, &r.Execs, &r.P95Ms, &r.Services); err != nil {
			return nil, err
		}
		r.StmtHash = strconv.FormatUint(hash, 10)
		if len(r.Sample) > 400 {
			r.Sample = r.Sample[:400] + "…"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
