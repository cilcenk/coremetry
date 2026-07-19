package chstore

import (
	"context"
	"time"
)

// runtime_pods.go — JVM pod-level runtime örnekleri, evaluator'ın runtime
// detector'ı için (v0.9.90, operatör talebi: "sorunlu pod olursa haber
// versin / problem tetiklesin").
//
// Overview'daki Runtime paneliyle (v0.9.91) AYNI pod anahtar zinciri
// (deploys.go instanceIdExpr emsali): k8s.pod.name (collector
// k8sattributes'a bağlı, her ortamda yok) → host.name (container
// hostname = k8s pod adı, javaagent default; podservice.go:11) →
// service.instance.id ilk 8 (UUID son çare) → '' (aggregate; problem
// yine servis düzeyinde açılır, kırılmaz).
//
// Distributed-güvenli: yalnız metric_points'in HER kurulumda var olan
// kolonları (metric/time/value/res_keys/attr_keys) — yeni kolon yok,
// hasXCol probe gerekmez.
const runtimePodExpr = `coalesce(
	nullIf(res_values[indexOf(res_keys, 'k8s.pod.name')], ''),
	nullIf(res_values[indexOf(res_keys, 'host.name')], ''),
	nullIf(substring(res_values[indexOf(res_keys, 'service.instance.id')], 1, 8), ''),
	''
)`

// runtimeWindow — sustained penceresi: 10 dk ortalaması eşiği aşıyorsa
// tekil spike değil kalıcı durumdur (ForSec eşdeğeri pencereleme ile).
const runtimeWindow = 10 * time.Minute

// JVMHeapPodUsage returns per-(service, pod) heap saturation samples:
// Usage = 10-dk ortalaması toplam heap kullanımı (byte), Limit = -Xmx.
//
// İki seviyeli toplama ŞART: jvm.memory.used HAVUZ BAŞINA datapoint'tir
// (jvm.memory.pool.name attr'lı — G1 Eden/Old/Survivor…). Düz avg havuz
// sayısına böler (heap/N gibi görünür); doğrusu her timestamp'te havuzlar
// ÜZERİNDEN SUM, sonra pencere üzerinden AVG. jvm.memory.limit'i yalnız
// cap'i tanımlı havuzlar emit eder (G1'de Old Gen = -Xmx); sum ≈ -Xmx.
func (s *Store) JVMHeapPodUsage(ctx context.Context) ([]CapacitySample, error) {
	now := time.Now()
	from := now.Add(-runtimeWindow)
	q := `
		SELECT svc, pod, avg(used_ts) AS usage, avg(lim_ts) AS lim
		FROM (
			SELECT
				service_name AS svc,
				` + runtimePodExpr + ` AS pod,
				time,
				sumIf(value, metric = 'jvm.memory.used')  AS used_ts,
				sumIf(value, metric = 'jvm.memory.limit') AS lim_ts
			FROM metric_points
			WHERE time >= ? AND time <= ?
			  AND metric IN ('jvm.memory.used', 'jvm.memory.limit')
			  AND attr_values[indexOf(attr_keys, 'jvm.memory.type')] = 'heap'
			GROUP BY svc, pod, time
		)
		GROUP BY svc, pod
		HAVING lim > 0
		LIMIT 2000
		SETTINGS max_execution_time = 10`
	rows, err := s.conn.Query(ctx, q, from, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapacitySample{}
	for rows.Next() {
		var c CapacitySample
		if err := rows.Scan(&c.Instance, &c.Subkey, &c.Usage, &c.Limit); err != nil {
			continue
		}
		if c.Instance == "" || c.Limit <= 0 {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// JVMGCPodPause returns per-(service, pod) average GC pause over the
// window, in MILLISECONDS (Usage; Limit=0 — eşik evaluator'da).
//
// jvm.gc.duration histogram'dır; ingest value kolonu per-export ORTALAMA
// pause'dur (Sum/Count, convert.go) — avg(value) pencere-ortalama pause
// verir. HAVING n >= 3: MinSamples tabanı (tek örnekli pencere flapping'i).
func (s *Store) JVMGCPodPause(ctx context.Context) ([]CapacitySample, error) {
	now := time.Now()
	from := now.Add(-runtimeWindow)
	q := `
		SELECT service_name AS svc, ` + runtimePodExpr + ` AS pod,
		       avg(value) * 1000 AS pause_ms, count() AS n
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric = 'jvm.gc.duration'
		GROUP BY svc, pod
		HAVING n >= 3
		LIMIT 2000
		SETTINGS max_execution_time = 10`
	rows, err := s.conn.Query(ctx, q, from, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapacitySample{}
	for rows.Next() {
		var c CapacitySample
		var n uint64
		if err := rows.Scan(&c.Instance, &c.Subkey, &c.Usage, &n); err != nil {
			continue
		}
		if c.Instance == "" {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
