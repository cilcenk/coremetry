package chstore

// trace_haserror_test.go — v0.10.258 regresyon kapısı. Operator-reported:
// "aynı sorguyu root diye aratınca geliyor ama error seçince no traces".
// Kök neden: Errors span-düzeyi WHERE (status_code='error') iken search /
// root / attr filtreleri trace-düzeyi HAVING'di → aynı span hem hata hem
// aranan olmak zorundaydı. Sözleşme: başka span-düzeyi yüklem yoksa WHERE
// (idx_status budar), varsa yalnız HAVING (max(if(status_code='error')) = 1)
// — liste (heavy + light) ve aggregate iç HAVING.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestHasErrorSpanLocal(t *testing.T) {
	base := TraceFilter{HasError: true, From: time.Now().Add(-time.Hour), To: time.Now(), Service: "svc"}
	cases := []struct {
		name string
		mut  func(f *TraceFilter)
		want bool
	}{
		{"yalnız errors", func(f *TraceFilter) {}, true},
		{"errors + service", func(f *TraceFilter) { f.Service = "api" }, true},
		{"errors + search", func(f *TraceFilter) { f.Search = "execute" }, false},
		{"errors + rootOnly", func(f *TraceFilter) { f.RootOnly = true }, false},
		{"errors + attr filtresi", func(f *TraceFilter) { f.Filters = []FilterExpr{{Key: "http.route", Op: "=", Values: []string{"/x"}}} }, false},
		{"errors + requireServices", func(f *TraceFilter) { f.RequireServices = []string{"b"} }, false},
		{"errors kapalı", func(f *TraceFilter) { f.HasError = false }, false},
	}
	for _, c := range cases {
		f := base
		c.mut(&f)
		if got := hasErrorSpanLocal(f); got != c.want {
			t.Errorf("%s: %v, istenen %v", c.name, got, c.want)
		}
	}
}

func TestHasErrorWhereOnlyWhenSpanLocal(t *testing.T) {
	from := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	only := TraceFilter{HasError: true, From: from, To: from.Add(12 * time.Hour), Service: "svc"}
	wcOnly := buildGetTracesWhere(only, clusterDeriveExpr)
	if sql := wcOnly.sql(); !strings.Contains(sql, "status_code = 'error'") {
		t.Errorf("yalnız errors: WHERE status yüklemi bekleniyordu:\n%s", sql)
	}
	mixed := only
	mixed.Search = "/BSAWEB/execute"
	mixed.RootOnly = true
	wcMixed := buildGetTracesWhere(mixed, clusterDeriveExpr)
	if sql := wcMixed.sql(); strings.Contains(sql, "status_code = 'error'") {
		t.Errorf("errors + search/root: WHERE status yüklemi OLMAMALI (aynı-span tuzağı):\n%s", sql)
	}
	agg := AggregateFilter{HasError: true, Search: "x"}
	if aggHasErrorSpanLocal(agg) {
		t.Error("aggregate: search varken span-local olmamalı")
	}
	agg.Search = ""
	if !aggHasErrorSpanLocal(agg) {
		t.Error("aggregate: yalnız errors span-local olmalı")
	}
}

// Kaynak taraması: HAVING bloğu hata yüklemini HER İKİ listeye (heavy +
// light) ve aggregate iç HAVING'e ekliyor — biri düşerse 238/241 sınıfı
// "ikinci yarı" tekrar eder.
func TestHasErrorHavingWiredInBothLists(t *testing.T) {
	src, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		"havingParts = append(havingParts, traceHasErrorHaving)",
		"lightHavingParts = append(lightHavingParts, traceHasErrorHaving)",
		`searchHaving += " AND " + traceHasErrorHaving`,
		"lf.HasError = false",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("repo.go %q içermiyor — trace-düzeyi hata yüklemi bir yoldan düşmüş", want)
		}
	}
}
