package chstore

// entity_pod_latency.go — v0.10.136 (DETAY SAYFALARI adım 2: servis detay).
// Ham spans üzerinde TELEMETRİ SELECT'i (state tablosu değil) — bu yüzden
// entity_queries.go'dan AYRI dosyada: telemetryReadConn RoundRobin okuma
// havuzunu yalnız telemetri okumaları kullanır (TestTelemetryReadConnCallSurface,
// v0.9.486); state tabloları in-order ana bağlantıda kalır.

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// ── v0.10.136 — DETAY SAYFALARI adım 2 (servis detay): pod başına latency ──
// entity_seen_5m ortalama taşır, yüzdelik taşımaz (sumState/countState).
// p50/p95/p99 için ham spans: servis + pencere sınırlı, giriş-span ilkesi
// (kind server/consumer), terfi k8s_pod kolonuyla gruplanır; LIMIT 200.
type PodLatencyRow struct {
	Cluster    string  `json:"cluster"`
	Namespace  string  `json:"namespace"`
	Pod        string  `json:"pod"`
	EntrySpans int64   `json:"entrySpans"`
	Errors     int64   `json:"errors"`
	P50Ms      float64 `json:"p50Ms"`
	P95Ms      float64 `json:"p95Ms"`
	P99Ms      float64 `json:"p99Ms"`
}

// podLatencySQL — saf; tablo-testli. clusterValue boş = tüm cluster'lar
// (satırlar cluster kolonuyla AYRIŞIR, birleşmez). Dönen sayı bind arg adedi.
// PodLatencyLimit — satır tavanı; dolunca çağıran "yalnız en yoğun N pod" der.
const PodLatencyLimit = 200

func podLatencySQL(clusterValue string) (string, int) {
	where := "service_name = ? AND time >= ? AND time <= ? AND k8s_pod != '' AND kind IN ('server', 'consumer')"
	n := 3
	if clusterValue != "" {
		where += " AND cluster = ?"
		n = 4
	}
	return `SELECT cluster, k8s_namespace, k8s_pod,
		       count()                                                              AS entry_spans,
		       countIf(status_code = 'error')                                       AS errors,
		       arrayElement(quantilesTDigest(0.5, 0.95, 0.99)(duration), 1) / 1e6   AS p50_ms,
		       arrayElement(quantilesTDigest(0.5, 0.95, 0.99)(duration), 2) / 1e6   AS p95_ms,
		       arrayElement(quantilesTDigest(0.5, 0.95, 0.99)(duration), 3) / 1e6   AS p99_ms
		FROM spans
		WHERE ` + where + `
		GROUP BY cluster, k8s_namespace, k8s_pod
		ORDER BY entry_spans DESC
		LIMIT ` + strconv.Itoa(PodLatencyLimit) + `
		SETTINGS max_execution_time = 15`, n
}

func (s *Store) PodLatencyForService(ctx context.Context, service, clusterValue string, from, to time.Time) ([]PodLatencyRow, error) {
	sql, _ := podLatencySQL(clusterValue)
	args := []any{service, from, to}
	if clusterValue != "" {
		args = append(args, clusterValue)
	}
	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("pod latency: %w", err)
	}
	defer rows.Close()
	out := []PodLatencyRow{}
	for rows.Next() {
		var r PodLatencyRow
		var n, e uint64
		if err := rows.Scan(&r.Cluster, &r.Namespace, &r.Pod, &n, &e, &r.P50Ms, &r.P95Ms, &r.P99Ms); err != nil {
			return nil, err
		}
		r.EntrySpans, r.Errors = int64(n), int64(e)
		out = append(out, r)
	}
	return out, rows.Err()
}
