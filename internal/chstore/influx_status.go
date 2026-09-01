package chstore

// influx_status.go — v0.10.223 (Influx D2 durum ucu). SAF TELEMETRİ: tek
// FROM'u metric_points (`ext:` önekli dış seriler). influx.go'dan AYRI
// dosya, bilinçli: o dosya system_settings blobunu (state) okuyup yazıyor
// ve conn_strategy_test.go'nun dosya-yüzeyi kapısı RoundRobin okuma
// havuzunu yalnız state'siz dosyalara açıyor (v0.9.486) — metric_points
// okuması kardeş telemetri okumalarıyla (hosts.go / metricquery.go) aynı
// havuzda, ayar blobu in-order ana bağlantıda kalır.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// InfluxIngestRow — bir kaynağın metric_points'teki izi (v0.10.223, D2
// durum ucu). Kaynak = service_name; dış metrikler `ext:` önekli.
type InfluxIngestRow struct {
	Source      string `json:"source"`
	LastPointAt int64  `json:"lastPointAt"` // unix ms
	Points1h    uint64 `json:"points1h"`
	Series1h    uint64 `json:"series1h"`
}

// influxIngestStatusSQL — SAF (tablo-testli): zaman sınırlı WHERE + LIMIT +
// max_execution_time (CLAUDE.md CH kuralı). ORDER BY öneki service_name.
func influxIngestStatusSQL(n int) string {
	return `
		SELECT service_name,
		       max(time)                                AS last_point,
		       count()                                  AS points,
		       uniqExact(metric, attr_values)           AS series
		FROM metric_points
		WHERE service_name IN (` + strings.TrimRight(strings.Repeat("?,", n), ",") + `)
		  AND metric LIKE 'ext:%'
		  AND time >= now() - INTERVAL 1 HOUR
		GROUP BY service_name
		LIMIT 100
		SETTINGS max_execution_time = 10`
}

// InfluxIngestStatus — son 1 saatte kaynak başına nokta/seri sayısı ve
// son nokta zamanı. Boş liste → boş sonuç, sorgu yok.
func (s *Store) InfluxIngestStatus(ctx context.Context, sources []string) ([]InfluxIngestRow, error) {
	if len(sources) == 0 {
		return []InfluxIngestRow{}, nil
	}
	if len(sources) > 100 {
		return nil, fmt.Errorf("en çok 100 kaynak")
	}
	args := make([]any, 0, len(sources))
	for _, n := range sources {
		args = append(args, n)
	}
	rows, err := s.telemetryReadConn().Query(ctx, influxIngestStatusSQL(len(sources)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InfluxIngestRow{}
	for rows.Next() {
		var r InfluxIngestRow
		var last time.Time
		if err := rows.Scan(&r.Source, &last, &r.Points1h, &r.Series1h); err != nil {
			return nil, err
		}
		r.LastPointAt = last.UnixMilli()
		out = append(out, r)
	}
	return out, rows.Err()
}
