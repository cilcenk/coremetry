package chstore

// messaging_clients.go — v0.10.550 (Messaging Kafka client metrikleri, Faz 1).
//
// Topic'in üretici/tüketici SERVİS listesi — VM'e gidecek `kafka_*` sorgusunun
// kapsamı (kapsamsız sorgu tüm filoyu toplar; audit §3.8). Kaynak
// messaging_caller_summary_5m MV'si (kind boyutu burada), ham spans DEĞİL.
// GetMessagingDetail aynı MV'yi pod kırılımıyla okuyor; bu okuma servis+rol
// düzeyinde ve E2E/Top-ops ham taramalarını TAŞIMAZ — o yüzden ayrı metot.

import (
	"context"
	"time"
)

// MsgCallerService — bir topic'e dokunan servis ve rolü (producer | consumer |
// client = kind boş). Aynı servis iki rolde iki satır.
type MsgCallerService struct {
	Service string `json:"service"`
	Role    string `json:"role"`
}

const msgCallerServicesSQL = `
		SELECT service_name,
		       coalesce(nullIf(kind, ''), 'client') AS role,
		       countMerge(span_count_state) AS n
		FROM messaging_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?
		  AND msg_system = ? AND cluster = ? AND destination = ?
		GROUP BY service_name, role
		ORDER BY n DESC
		LIMIT 200
		SETTINGS max_execution_time = 8`

// MessagingCallerServices — (system, cluster, destination) için pencere içindeki
// servis+rol listesi, span sayısına göre azalan; en fazla 200 satır.
func (s *Store) MessagingCallerServices(ctx context.Context, system, cluster, destination string, from, to time.Time) ([]MsgCallerService, error) {
	if system == "" || destination == "" {
		return nil, nil
	}
	if cluster == "" {
		cluster = "(default)"
	}
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	rows, err := s.telemetryReadConn().Query(ctx, msgCallerServicesSQL, alignBucketStart(from), to, system, cluster, destination)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MsgCallerService{}
	for rows.Next() {
		var r MsgCallerService
		var n uint64
		if err := rows.Scan(&r.Service, &r.Role, &n); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
