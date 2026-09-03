package chstore

// db_slow_statement.go — v0.10.325 (operatör isteği 2026-09-03: "bir db
// sorgusu 1 saniyenin üzerine çıktığında problem tetiklesin, SRE'ye mail
// gitsin" → "Gerekiyor"; spec onayı "Uygun"). Kaynak: db_statement_summary_5m
// MV — statement (db_system, db_name, service, stmt_hash) başına 5 dk
// kovada p50/p95/p99 tDigest, sayı, hata, en yavaş exemplar, örnek SQL.
// Bu dosya yalnız OKUR (MV) ve ayar blobunu taşır; karar + Problem açma
// evaluator/db_slow_statement.go'da (lider kilidi, dedup, notify).
//
// Ayar `system_settings['db_slow_query']` (invariant #6). Yönlendirme:
// Problem Kind=db özneyle açılınca notify DBOwnerForSubject → sahibi (ug)
// + SRE (sy) takımlarına mail — mevcut kanal, yeni bildirim yolu yok.

import (
	"context"
	"encoding/json"
	"time"
)

type DBSlowQueryConfig struct {
	Enabled bool `json:"enabled"`
	// ThresholdMs — 5 dk kovada p95 bu değeri aşınca "yavaş" (varsayılan 1000).
	ThresholdMs float64 `json:"thresholdMs"`
	// CriticalMs — son kovanın p95'i bunu aşarsa şiddet critical (P1).
	CriticalMs float64 `json:"criticalMs"`
	// MinExecutions — kova başına yürütme tabanı; altı gürültü sayılır.
	MinExecutions uint64 `json:"minExecutions"`
	// ForBuckets — ardışık kaç 5 dk kovada aşılmalı (2 = ~10 dk sürmüş).
	ForBuckets int `json:"forBuckets"`
	// CooldownSec — açık Problem, eşik altına inse bile bu süre dolmadan
	// çözülmez (flap önleyici tutma süresi).
	CooldownSec int `json:"cooldownSec"`
}

func DefaultDBSlowQuery() DBSlowQueryConfig {
	return DBSlowQueryConfig{Enabled: true, ThresholdMs: 1000, CriticalMs: 5000, MinExecutions: 20, ForBuckets: 2, CooldownSec: 900}
}

const dbSlowQueryKey = "db_slow_query"

func NormalizeDBSlowQuery(c DBSlowQueryConfig) DBSlowQueryConfig {
	d := DefaultDBSlowQuery()
	if c.ThresholdMs <= 0 {
		c.ThresholdMs = d.ThresholdMs
	}
	if c.CriticalMs < c.ThresholdMs {
		c.CriticalMs = c.ThresholdMs
	}
	if c.MinExecutions == 0 {
		c.MinExecutions = d.MinExecutions
	}
	if c.ForBuckets <= 0 {
		c.ForBuckets = d.ForBuckets
	}
	if c.ForBuckets > 12 {
		c.ForBuckets = 12
	}
	if c.CooldownSec < 0 {
		c.CooldownSec = 0
	}
	return c
}

func (s *Store) GetDBSlowQuery(ctx context.Context) DBSlowQueryConfig {
	raw, err := s.GetSetting(ctx, dbSlowQueryKey)
	if err != nil || len(raw) == 0 {
		return DefaultDBSlowQuery()
	}
	var c DBSlowQueryConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return DefaultDBSlowQuery()
	}
	return NormalizeDBSlowQuery(c)
}

func (s *Store) SaveDBSlowQuery(ctx context.Context, c DBSlowQueryConfig) (DBSlowQueryConfig, error) {
	c = NormalizeDBSlowQuery(c)
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	return c, s.PutSetting(ctx, dbSlowQueryKey, raw)
}

// SlowStatementBucket — bir statement'ın bir 5 dk kovadaki ölçüsü.
type SlowStatementBucket struct {
	DBSystem string
	DBName   string
	Service  string
	StmtHash uint64
	Bucket   time.Time
	Sample   string
	Count    uint64
	Errors   uint64
	P95Ms    float64
	P99Ms    float64
	Exemplar string
}

// slowStatementBucketsSQL — saf; HAVING eşiği ve tabanı MV'de uygular,
// pencere sınırı + LIMIT + max_execution_time (CH sözleşmesi).
func slowStatementBucketsSQL() string {
	return `
		SELECT db_system, db_name, service_name, stmt_hash, time_bucket,
		       anyMerge(sample_stmt_state)                                              AS sample_stmt,
		       countMerge(span_count_state)                                             AS execs,
		       countMerge(error_count_state)                                            AS errs,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms,
		       argMaxMerge(slow_exemplar_state)                                         AS exemplar
		FROM db_statement_summary_5m
		WHERE time_bucket >= ?
		GROUP BY db_system, db_name, service_name, stmt_hash, time_bucket
		HAVING execs >= ? AND p95_ms >= ?
		ORDER BY p95_ms DESC
		LIMIT 500
		SETTINGS max_execution_time = 10`
}

// SlowStatementBuckets — since'ten bu yana eşiği aşan statement-kova
// satırları. db_stmt_hash kolonu yoksa MV boş olur → nil (dedektör susar).
func (s *Store) SlowStatementBuckets(ctx context.Context, since time.Time, minExecs uint64, thresholdMs float64) ([]SlowStatementBucket, error) {
	if !s.hasDBStmtHashCol {
		return nil, nil
	}
	rows, err := s.conn.Query(ctx, slowStatementBucketsSQL(), since, minExecs, thresholdMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SlowStatementBucket{}
	for rows.Next() {
		var b SlowStatementBucket
		if err := rows.Scan(&b.DBSystem, &b.DBName, &b.Service, &b.StmtHash, &b.Bucket,
			&b.Sample, &b.Count, &b.Errors, &b.P95Ms, &b.P99Ms, &b.Exemplar); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
