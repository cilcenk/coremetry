package evaluator

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.705 — http_route hedefli kural: gerekçe metni + dispatch pini.
func TestRouteTargetDescribeAndDispatch(t *testing.T) {
	r := chstore.AlertRule{Name: "Pay p95", Metric: "http_route_p95_ms", Comparator: ">", Threshold: 800, WindowSec: 600,
		Target: &chstore.RuleTarget{Kind: chstore.RuleTargetHTTPRoute, Service: "shop-payment", Route: "/api/pay"}}
	d := describeRouteTargetProblem(r, chstore.RouteWindowStats{Calls: 1200, Errors: 30}, 1420)
	for _, want := range []string{"Pay p95", "shop-payment /api/pay", "p95 1.42 s", "> 800 ms", "600s window", "1200 calls", "30 errors", "/endpoint?service=shop-payment&path=/api/pay"} {
		if !strings.Contains(d, want) {
			t.Errorf("gerekçede %q yok: %s", want, d)
		}
	}
	er := r
	er.Metric, er.Threshold = "http_route_error_rate", 5
	if d := describeRouteTargetProblem(er, chstore.RouteWindowStats{Calls: 10, Errors: 2}, 20); !strings.Contains(d, "error rate 20.00%") || !strings.Contains(d, "> 5.00%") {
		t.Errorf("hata oranı biçimi: %s", d)
	}
	rr := r
	rr.Metric, rr.Comparator, rr.Threshold = "http_route_rate", "<", 1
	if d := describeRouteTargetProblem(rr, chstore.RouteWindowStats{Calls: 6}, 0.1); !strings.Contains(d, "request rate 0.10/s") || !strings.Contains(d, "< 1.00/s") {
		t.Errorf("hız biçimi: %s", d)
	}
	b, err := os.ReadFile("alert_target.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "case chstore.RuleTargetHTTPRoute:") || !strings.Contains(string(b), "e.evaluateHTTPRouteTargetRule(ctx, r, openSnap)") {
		t.Error("evaluateTargetRule http_route dalını dağıtmıyor")
	}
}
