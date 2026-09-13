package evaluator

// alert_target_route.go — v0.10.705 (Dynatrace paritesi #3): http_route
// hedefli kural. Ölçü chstore.RouteWindowStats (spanmetrics_1m, tek sınırlı
// sorgu); özne = SERVİS, Kind=service (Kafka emsali — ekip/link/servis şeridi
// akışı bozulmaz; aynı serviste iki route kuralı iki ayrı RuleID taşır, özne
// çakışmaz). MinSamples = penceredeki çağrı sayısı (Statement'taki yürütme
// sayısı gibi). Karar gövdesi ortak settleTargetBreach.

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// routeMetricLabel — açıklama için ölçü adı + birimli değer biçimi.
func routeMetricLabel(metric string, v float64) (label, value string) {
	switch metric {
	case "http_route_p99_ms":
		return "p99", fmtMsShort(v)
	case "http_route_error_rate":
		return "error rate", fmt.Sprintf("%.2f%%", v)
	case "http_route_rate":
		return "request rate", fmt.Sprintf("%.2f/s", v)
	default:
		return "p95", fmtMsShort(v)
	}
}

// describeRouteTargetProblem — gerekçe: kural adı + servis · route + ölçü +
// eşik + pencere + çağrı/hata sayısı + endpoint sayfası yolu. Saf.
func describeRouteTargetProblem(r chstore.AlertRule, st chstore.RouteWindowStats, value float64) string {
	svc, route := "", ""
	if r.Target != nil {
		svc, route = r.Target.Service, r.Target.Route
	}
	label, val := routeMetricLabel(r.Metric, value)
	_, thr := routeMetricLabel(r.Metric, r.Threshold)
	return fmt.Sprintf("%s — %s %s: route %s %s, threshold %s %s over %ds window (%d calls, %d errors) · /endpoint?service=%s&path=%s",
		r.Name, svc, route, label, val, r.Comparator, thr, r.WindowSec, st.Calls, st.Errors, svc, route)
}

func (e *Evaluator) evaluateHTTPRouteTargetRule(ctx context.Context, r chstore.AlertRule, openSnap *chstore.OpenProblems) {
	t := r.Target
	window := time.Duration(r.WindowSec) * time.Second
	st, err := e.store.RouteWindowStats(ctx, *t, window)
	if err != nil {
		log.Printf("[evaluator] http_route rule %s (%s): %v", r.ID, r.Name, err)
		return
	}
	subject, kind := t.Service, chstore.ProblemKindService
	key := breachKey{RuleID: r.ID, Service: subject}
	now := time.Now()
	if r.MinSamples > 0 && st.Calls < uint64(r.MinSamples) {
		e.clearBreach(ctx, key)
		return
	}
	value := chstore.RouteMetricValue(st, r.Metric)
	breached := st.Calls > 0 && compare(value, r.Comparator, r.Threshold)
	e.settleTargetBreach(ctx, r, key, subject, kind, value, breached, now,
		func() string { return describeRouteTargetProblem(r, st, value) }, openSnap, "http route rule")
}
