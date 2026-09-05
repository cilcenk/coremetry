package api

// v0.10.367 — Operator-reported: health-probe routes (checkLiveness /
// checkReadiness / checkStartup) kept showing on the Service Overview
// metric charts on a VictoriaMetrics-backed install. The exclusion
// rules (Settings → Pipeline → Dışlama) were applied only by the
// ClickHouse reader; the VM adapter delegated the filter untouched
// while its cache keys already carried the rules' digest.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func compiledRules(t *testing.T, rules ...chstore.MetricExclusionRule) *chstore.CompiledMetricExclusions {
	t.Helper()
	ex, err := chstore.CompileMetricExclusions(chstore.MetricExclusions{Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

func TestRouteExclusionFiltersWrapUnanchoredPatterns(t *testing.T) {
	ex := compiledRules(t,
		chstore.MetricExclusionRule{Metric: "*", AttrKey: "http.route", Pattern: "check(Liveness|Readiness|Startup)"},
		chstore.MetricExclusionRule{Metric: "http.server.request.duration", AttrKey: "http.route", Pattern: "^/actuator"},
	)
	got := routeExclusionFilters(ex, "http.server.request.duration")
	if len(got) != 2 {
		t.Fatalf("filters = %+v, want exact-name rule then wildcard", got)
	}
	for _, f := range got {
		if f.Key != "http.route" || f.Op != "!~" || len(f.Values) != 1 {
			t.Fatalf("filter shape: %+v", f)
		}
		re := regexp.MustCompile("^(?:" + f.Values[0] + ")$") // PromQL anchors fully
		switch {
		case strings.Contains(f.Values[0], "actuator"):
			if !re.MatchString("/actuator/health") || re.MatchString("/api/actuator") {
				t.Fatalf("anchored rule must keep its anchor inside the wrapper: %s", f.Values[0])
			}
		default:
			if !re.MatchString("/BSAWEB/bsa/core/server/checkReadiness") || re.MatchString("/BSAWEB/loan/assessment") {
				t.Fatalf("unanchored rule must match anywhere in the path: %s", f.Values[0])
			}
		}
	}
	if got := routeExclusionFilters(ex, "other.metric"); len(got) != 1 {
		t.Fatalf("other metrics get only the wildcard rule: %+v", got)
	}
	if got := routeExclusionFilters(compiledRules(t), "x"); got != nil {
		t.Fatalf("no rules → nil (zero-impact pin), got %+v", got)
	}
}

func TestVMSourceAppendsExclusionsCopyOnWrite(t *testing.T) {
	ex := compiledRules(t, chstore.MetricExclusionRule{Metric: "*", AttrKey: "http.route", Pattern: "/health"})
	v := vmMetricSource{ex: func() *chstore.CompiledMetricExclusions { return ex }}
	base := chstore.MetricQueryFilter{Name: "m", Filters: []chstore.FilterExpr{{Key: "deployment.environment", Op: "=", Values: []string{"prod"}}}}
	got := v.excluded(base)
	if len(got.Filters) != 2 || got.Filters[0].Key != "deployment.environment" || got.Filters[1].Op != "!~" {
		t.Fatalf("excluded = %+v", got.Filters)
	}
	if len(base.Filters) != 1 {
		t.Fatal("caller's filter slice was mutated")
	}
	none := vmMetricSource{}
	if g := none.excluded(base); len(g.Filters) != 1 {
		t.Fatalf("no rule source must pass the filter through: %+v", g.Filters)
	}
}

// Every delegated query on the VM adapter goes through excluded(); a
// new method that forwards `f` bare re-opens the operator's report.
func TestVMSourceQueriesAllApplyExclusions(t *testing.T) {
	src := readRepoFile(t, "metricsource.go")
	i := strings.Index(src, "type vmMetricSource struct")
	if i < 0 {
		t.Fatal("vmMetricSource not found")
	}
	body := src[i:]
	bare := regexp.MustCompile(`v\.svc\.Query\w*\(ctx, f[,)]`)
	if m := bare.FindString(body); m != "" {
		t.Fatalf("VM adapter forwards a filter without exclusions: %s", m)
	}
	if n := strings.Count(body, "v.excluded(f)"); n < 5 {
		t.Fatalf("expected ≥5 excluded(f) call sites (QueryMetric, Histogram, Rate, CountRate, Noted), got %d", n)
	}
	if strings.Contains(src, "vmMetricSource{s.vmetrics}") {
		t.Fatal("constructors must go through newVMMetricSource so the rules are wired")
	}
}
