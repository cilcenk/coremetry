package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.10.705 — http_route hedefli kural: doğrulama, metrik eşlemesi, SQL şekli,
// pencere hizası ve oran/hız türetimi; codec round-trip.
func TestValidateRuleTargetHTTPRoute(t *testing.T) {
	ok := AlertRule{Name: "pay p95", Metric: "http_route_p95_ms", Threshold: 800, Target: &RuleTarget{Kind: RuleTargetHTTPRoute, Service: "shop-payment", Route: "/api/pay"}}
	if err := ValidateRuleTarget(ok); err != nil {
		t.Fatalf("geçerli hedef reddedildi: %v", err)
	}
	cases := []struct {
		name string
		mut  func(r *AlertRule)
		want string
	}{
		{"servis yok", func(r *AlertRule) { r.Target.Service = " " }, "service"},
		{"route / ile başlamıyor", func(r *AlertRule) { r.Target.Route = "api/pay" }, "route"},
		{"route boşluklu", func(r *AlertRule) { r.Target.Route = "/api pay" }, "route"},
		{"route çok kısa", func(r *AlertRule) { r.Target.Route = "/" }, "route"},
		{"servis metriği route hedefinde", func(r *AlertRule) { r.Metric = "p95_ms" }, "http_route_"},
		{"route metriği hedefsiz", func(r *AlertRule) { r.Target = nil }, "http_route"},
		{"eşik sıfır", func(r *AlertRule) { r.Threshold = 0 }, "threshold"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ok
			tg := *ok.Target
			r.Target = &tg
			c.mut(&r)
			err := ValidateRuleTarget(r)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("hata bekleniyordu (%q), geldi: %v", c.want, err)
			}
		})
	}
	// Diğer kardeşler etkilenmez.
	if err := ValidateRuleTarget(AlertRule{Metric: "db_stmt_p95_ms", Threshold: 100, Target: &RuleTarget{Kind: RuleTargetDBStatement, StmtHash: "42"}}); err != nil {
		t.Fatalf("db hedefi: %v", err)
	}
	if err := ValidateRuleTarget(AlertRule{Metric: "error_rate", Threshold: 5}); err != nil {
		t.Fatalf("hedefsiz servis kuralı: %v", err)
	}
}

func TestRouteMetricValueAndCodec(t *testing.T) {
	st := RouteWindowStats{P95Ms: 120, P99Ms: 450, ErrorRate: 2.5, Rate: 7.5}
	for m, want := range map[string]float64{"http_route_p95_ms": 120, "http_route_p99_ms": 450, "http_route_error_rate": 2.5, "http_route_rate": 7.5, "unknown": 120} {
		if got := RouteMetricValue(st, m); got != want {
			t.Errorf("%s → %v, beklenen %v", m, got, want)
		}
	}
	tg := &RuleTarget{Kind: RuleTargetHTTPRoute, Service: "shop-payment", Route: "/api/pay"}
	back := decodeRuleTarget(encodeRuleTarget(tg))
	if back == nil || back.Kind != RuleTargetHTTPRoute || back.Route != "/api/pay" || back.Service != "shop-payment" {
		t.Fatalf("codec round-trip: %+v", back)
	}
	if !IsHTTPRouteMetric("http_route_rate") || IsHTTPRouteMetric("request_rate") {
		t.Fatal("IsHTTPRouteMetric ailesi")
	}
}

func TestRouteWindowSQLShapeAndBounds(t *testing.T) {
	sql := routeWindowSQL()
	for label, sub := range map[string]string{
		"MV":          "FROM spanmetrics_1m",
		"identity":    "service_name = ? AND http_route = ?",
		"entry span":  "kind NOT IN ('client', 'producer')",
		"lower bound": "time_bucket >= ?",
		"upper bound": "time_bucket < ?",
		"p95 idx 3":   "duration_q_state), 3) / 1e6",
		"p99 idx 4":   "duration_q_state), 4) / 1e6",
		"exec bound":  "max_execution_time = 10",
	} {
		if !strings.Contains(sql, sub) {
			t.Errorf("%s eksik: %q", label, sub)
		}
	}
	if strings.Contains(sql, "FROM spans") {
		t.Error("ham spans taranmamalı")
	}
	now := time.Date(2026, 9, 13, 10, 3, 40, 0, time.UTC)
	from, to := routeWindowBounds(now, 10*time.Minute)
	if !to.Equal(time.Date(2026, 9, 13, 10, 3, 0, 0, time.UTC)) || !from.Equal(to.Add(-10*time.Minute)) {
		t.Fatalf("1 dk hiza + tam kovalar: %v..%v", from, to)
	}
	if f, tt := routeWindowBounds(now, 10*time.Second); tt.Sub(f) != time.Minute {
		t.Fatal("pencere tabanı 1 dk")
	}
	st := routeStatsFinish(RouteWindowStats{Calls: 600, Errors: 15}, from, to)
	if st.ErrorRate != 2.5 || st.Rate != 1 {
		t.Fatalf("oran %%2.5 ve 1 istek/sn beklenir: %+v", st)
	}
	if z := routeStatsFinish(RouteWindowStats{}, from, to); z.ErrorRate != 0 || z.Rate != 0 {
		t.Fatalf("boş pencere sıfır: %+v", z)
	}
}
