package chstore

// rootcause_series.go — v0.10.700 (Dynatrace paritesi #2, dilim 1).
//
// Hipotez işçisinin zamansal çarpanı için: anchor + yapısal komşuların
// 5 dk hata-oranı serileri TEK sorguda (service_summary_5m, IN-listesi
// bağlı literal). Dedektörün buildAllBucketsQuery kalıbı: MV okuması,
// alt+üst zaman sınırı, servis başına + toplam LIMIT, max_execution_time.
// Fark: servis listesi sınırlı (temporalSeriesMaxServices), pencere kısa
// (işçi onset−60m..now), çıktı grid-hizalı — eksik kova NaN ki ölçü
// "veri yok"u "sıfır hata" sanmasın.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	// temporalSeriesMaxServices — anchor + ≤10 aday; fazlası düşürülür.
	temporalSeriesMaxServices = 12
	temporalSeriesBucket      = 5 * time.Minute
)

// buildErrorRateSeriesQuery — SAF (şekil testi). n = IN listesindeki
// yer tutucu sayısı; ardından iki zaman sınırı bağlanır.
func buildErrorRateSeriesQuery(n int) string {
	holders := strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
	return fmt.Sprintf(`
		SELECT service_name, toUnixTimestamp(time_bucket) AS t,
		       ifNull(countMerge(error_count_state) / nullIf(countMerge(span_count_state), 0) * 100, 0) AS v
		FROM service_summary_5m
		WHERE service_name IN (%s) AND time_bucket >= ? AND time_bucket < ?
		GROUP BY service_name, t
		ORDER BY service_name, t
		LIMIT 300 BY service_name
		LIMIT 5000
		SETTINGS max_execution_time = 10`, holders)
}

// boundSeriesServices — SAF: boşları at, tekrarsız, sıra korunur,
// temporalSeriesMaxServices tavanı.
func boundSeriesServices(services []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(services))
	for _, s := range services {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= temporalSeriesMaxServices {
			break
		}
	}
	return out
}

// alignSeriesGrid — SAF: (servis → kova-unix → değer) noktalarını
// [start, end) 5 dk grid'ine oturtur; eksik kova NaN, aralık dışı nokta
// düşer. Her istenen servis için bir dizi döner (hiç noktası yoksa tamamen
// NaN) — çağıran "yok" ile "sessiz"i karıştırmasın.
func alignSeriesGrid(services []string, points map[string]map[int64]float64, start, end time.Time) map[string][]float64 {
	n := int(end.Sub(start) / temporalSeriesBucket)
	out := make(map[string][]float64, len(services))
	if n <= 0 {
		return out
	}
	step := int64(temporalSeriesBucket / time.Second)
	for _, svc := range services {
		series := make([]float64, n)
		for i := range series {
			series[i] = math.NaN()
		}
		for t, v := range points[svc] {
			idx := (t - start.Unix()) / step
			if idx < 0 || idx >= int64(n) {
				continue
			}
			series[idx] = v
		}
		out[svc] = series
	}
	return out
}

// ServiceErrorRateSeries5m — servis kümesinin [from, to) 5 dk hata-oranı
// (%) serileri, grid-hizalı. from/to 5 dk grid'e kırpılır; boş küme ya da
// boş pencere → boş harita, sorgu yok.
func (s *Store) ServiceErrorRateSeries5m(ctx context.Context, services []string, from, to time.Time) (map[string][]float64, error) {
	svcs := boundSeriesServices(services)
	start := from.UTC().Truncate(temporalSeriesBucket)
	end := to.UTC().Truncate(temporalSeriesBucket)
	if len(svcs) == 0 || !end.After(start) {
		return map[string][]float64{}, nil
	}
	args := make([]any, 0, len(svcs)+2)
	for _, v := range svcs {
		args = append(args, v)
	}
	args = append(args, start, end)
	rows, err := s.TelemetryReadConn().Query(ctx, buildErrorRateSeriesQuery(len(svcs)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make(map[string]map[int64]float64, len(svcs))
	for rows.Next() {
		var svc string
		var t uint32
		var v float64
		if err := rows.Scan(&svc, &t, &v); err != nil {
			return nil, err
		}
		if points[svc] == nil {
			points[svc] = map[int64]float64{}
		}
		points[svc][int64(t)] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return alignSeriesGrid(svcs, points, start, end), nil
}
