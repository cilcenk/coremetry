package chstore

import (
	"context"
	"math"
	"strconv"
	"time"
)

// v0.8.243 — granularity slice B (operator: "metric charts aren't as
// smooth as Grafana"). Requesting buckets FINER than a metric's actual
// export cadence doesn't add resolution — it adds holes: a 10s-exported
// gauge drawn at step=1s is 90% empty buckets rendered as sawtooth or
// gaps. Grafana solves this with $__rate_interval (never below the
// scrape interval); Coremetry's equivalent is this observed-export-
// interval clamp: the effective step never drops below what the metric
// actually ships.

// metricIvEntry is one cached probe result. iv == 0 means "couldn't
// infer" (young/sparse metric, probe error) → no clamp applied.
type metricIvEntry struct {
	at time.Time
	iv int
}

const (
	metricIvTTL         = 60 * time.Second
	metricIvProbeWindow = 10 * time.Minute
	// metricIvMinPoints — below this many points the interval estimate
	// is noise (a metric that just started reporting); don't clamp.
	metricIvMinPoints = 5
	// metricIvMaxSeconds — an inferred interval above this is treated
	// as "effectively sparse"; clamping a chart to >1h buckets would
	// fight the operator, and such metrics look fine unclamped.
	metricIvMaxSeconds = 3600
)

// exportIntervalQuantile — v0.9.672 (operatör-bildirimi: "kesikli
// çıkıyor bu da kötü bir görünüm").
//
// clamp'in TABANI artık en yoğun seriden değil, serilerin P90'ından
// geliyor. Ölçüm (yerel, http.server.duration, 1 saat):
//
//	28 seri · iv_min 4s · p50 8s · p90 29s · max 30s   → 7.5× yayılım
//	28 serinin 7'si 29-30s'de yayımlıyor
//	adımı belirleyen ise TEK bir 4s serisi
//
// 4s tabanla 30s'lik bir seri çizilince kovaların ~%87'si boş kalıyor —
// operatörün gördüğü "kesikli" tam bu. Clamp zaten delik ÖNLEMEK için
// yazılmıştı (v0.8.243, "$__rate_interval muadili"); niyet doğruydu,
// kusur tabanı en yoğun seriden almasıydı — yani "clamp yanlış" değil,
// "clamp yeterince yükseltmiyor".
//
// P90 SEÇİLDİ, max değil: tek bir bozuk/çok seyrek seri tüm grafiği
// kabalaştırmasın. Bedeli açık — karışık hızlı bir kümede en yoğun seri
// çözünürlük kaybediyor. TEK serili grafikte (Service Overview
// throughput) p90 = o serinin kendisi, yani davranış DEĞİŞMİYOR;
// dejenerasyon doğru yönde.
func exportIntervalQuantile(ivSec float64, series uint64) int {
	if series == 0 || ivSec <= 0 {
		return 0
	}
	iv := int(math.Round(ivSec))
	if iv < 1 {
		iv = 1
	}
	if iv > metricIvMaxSeconds {
		return 0
	}
	return iv
}

// clampStepToExport lifts a requested step to the metric's observed
// export interval. Only ever RAISES the step — a coarse request stays
// coarse. Pure.
func clampStepToExport(step, exportIv int) int {
	if exportIv > step {
		return exportIv
	}
	return step
}

// metricExportIntervalQuantileSQL — v0.9.672. Seri BAŞINA aralığı
// hesaplar ve P90'ını döndürür (+ seri sayısı).
//
// CH sınırları korunuyor (CLAUDE.md sert kısıtı): zaman-sınırlı WHERE,
// iç sorguda LIMIT, max_execution_time. Alt sorgu seri sayısıyla
// sınırlı — metric_points'in kendisi taranmıyor, GROUP BY sonucu.
func metricExportIntervalQuantileSQL(withService bool) string {
	q := `
		SELECT quantileExact(0.9)(iv) AS iv90, count() AS series FROM (
			SELECT count() AS cnt,
			       dateDiff('second', min(time), max(time)) AS spanSec,
			       spanSec / greatest(cnt - 1, 1) AS iv
			FROM metric_points
			WHERE metric = ? AND time >= ? AND time <= ?`
	if withService {
		q += `
			  AND service_name = ?`
	}
	q += `
			GROUP BY service_name, host_name, attr_values
			HAVING cnt >= ` + strconv.Itoa(metricIvMinPoints) + ` AND spanSec > 0
			LIMIT 20000
		)
		SETTINGS max_execution_time = 5`
	return q
}

// metricExportInterval returns the cached/probed export interval for a
// metric (optionally service-scoped — a metric can ship at different
// cadences per service). 0 = unknown → caller applies no clamp; a
// probe failure must never break the chart read.
func (s *Store) metricExportInterval(ctx context.Context, name, service string) int {
	key := name + "\x00" + service
	s.metricIvMu.RLock()
	if e, ok := s.metricIv[key]; ok && time.Since(e.at) < metricIvTTL {
		s.metricIvMu.RUnlock()
		return e.iv
	}
	s.metricIvMu.RUnlock()

	to := time.Now()
	from := to.Add(-metricIvProbeWindow)
	args := []any{name, from, to}
	if service != "" {
		args = append(args, service)
	}
	iv := 0
	var iv90 float64
	var series uint64
	if err := s.conn.QueryRow(ctx, metricExportIntervalQuantileSQL(service != ""), args...).Scan(&iv90, &series); err == nil {
		iv = exportIntervalQuantile(iv90, series)
	}

	s.metricIvMu.Lock()
	if s.metricIv == nil {
		s.metricIv = map[string]metricIvEntry{}
	}
	s.metricIv[key] = metricIvEntry{at: time.Now(), iv: iv}
	s.metricIvMu.Unlock()
	return iv
}
