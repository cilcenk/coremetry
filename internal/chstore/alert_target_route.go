package chstore

// alert_target_route.go — v0.10.705 (Dynatrace paritesi #3, spec onayı
// 2026-09-13): hedefli kuralın üçüncü kardeşi — bir servisin TEK bir
// http.route'u için statik eşik. Kimlik (service_name, http_route); method
// spanmetrics_1m'de yok, kapsam dışı; RPC (span adı) ve imza kipi bu dilimde
// yok — MV zaten şablon route saklar. Ölçü spanmetrics_1m state'lerinden
// (tDigest idx 3 = p95, 4 = p99 — 0.9 fazlası var), giriş-span yüklemi
// Endpoints ile aynı (kind NOT IN client/producer). Pencere 1 dk grid'e
// hizalı, yalnız TAM kovalar (v0.8.316 dersi) → rate = calls / kapsanan sn.
// deploy_env MV'de yok: kural env-agnostik ölçer (modal söyler).

import (
	"context"
	"strings"
	"time"
)

const RuleTargetHTTPRoute = "http_route"

// IsHTTPRouteMetric — önekli aile; düz p95_ms servis kuralıyla çakışmasın.
func IsHTTPRouteMetric(m string) bool {
	switch m {
	case "http_route_p95_ms", "http_route_p99_ms", "http_route_error_rate", "http_route_rate":
		return true
	}
	return false
}

// ValidRouteTarget — "/" öneki, 2-500 karakter, boşluksuz.
func ValidRouteTarget(route string) bool {
	r := strings.TrimSpace(route)
	return strings.HasPrefix(r, "/") && len(r) >= 2 && len(r) <= 500 && !strings.ContainsAny(r, " \t\n\r")
}

// RouteWindowStats — bir route'un pencere ölçüsü.
type RouteWindowStats struct {
	Calls     uint64
	Errors    uint64
	P95Ms     float64
	P99Ms     float64
	ErrorRate float64 // %
	Rate      float64 // istek/sn (calls / kapsanan saniye)
}

// RouteMetricValue — kural metriği → ölçü. Saf.
func RouteMetricValue(st RouteWindowStats, metric string) float64 {
	switch metric {
	case "http_route_p99_ms":
		return st.P99Ms
	case "http_route_error_rate":
		return st.ErrorRate
	case "http_route_rate":
		return st.Rate
	default:
		return st.P95Ms
	}
}

// routeWindowSQL — SAF (şekil testi): MV, iki zaman sınırı, max_execution_time.
func routeWindowSQL() string {
	return `
		SELECT countMerge(calls_state)                                                                AS calls,
		       countMerge(error_state)                                                                AS errors,
		       arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 3) / 1e6   AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 4) / 1e6   AS p99_ms
		FROM spanmetrics_1m
		WHERE service_name = ? AND http_route = ?
		  AND kind NOT IN ('client', 'producer')
		  AND time_bucket >= ? AND time_bucket < ?
		SETTINGS max_execution_time = 10`
}

// routeWindowBounds — SAF: [now−window, now) 1 dk grid'e kırpılmış; pencere
// ≥ 1 dk. Her iki uç hizalı → kapsanan saniye = tam kova sayısı × 60.
func routeWindowBounds(now time.Time, window time.Duration) (from, to time.Time) {
	if window < time.Minute {
		window = time.Minute
	}
	to = now.UTC().Truncate(time.Minute)
	from = to.Add(-window).Truncate(time.Minute)
	return from, to
}

// routeStatsFinish — SAF: ham sayımlardan oran/hız.
func routeStatsFinish(st RouteWindowStats, from, to time.Time) RouteWindowStats {
	if st.Calls > 0 {
		st.ErrorRate = float64(st.Errors) / float64(st.Calls) * 100
	}
	if sec := to.Sub(from).Seconds(); sec > 0 {
		st.Rate = float64(st.Calls) / sec
	}
	return st
}

// RouteWindowStats — son `window` için (service, route) ölçüsü.
func (s *Store) RouteWindowStats(ctx context.Context, t RuleTarget, window time.Duration) (RouteWindowStats, error) {
	var st RouteWindowStats
	from, to := routeWindowBounds(time.Now(), window)
	err := s.telemetryReadConn().QueryRow(ctx, routeWindowSQL(),
		strings.TrimSpace(t.Service), strings.TrimSpace(t.Route), from, to).
		Scan(&st.Calls, &st.Errors, &st.P95Ms, &st.P99Ms)
	if err != nil {
		return st, err
	}
	return routeStatsFinish(st, from, to), nil
}
