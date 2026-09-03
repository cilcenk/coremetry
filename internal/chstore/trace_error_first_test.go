package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// trace_error_first_test.go — v0.10.307 regresyon (Operator-reported).

func TestErrorFirstEligible(t *testing.T) {
	name := FilterExpr{Key: "name", Op: "=", Values: []string{"POST"}}
	for _, tc := range []struct {
		n    string
		f    TraceFilter
		want bool
	}{
		{"prod şekli: servis + Errors + filtre", TraceFilter{Service: "svc", HasError: true, Filters: []FilterExpr{name}}, true},
		{"servis + Errors + arama", TraceFilter{Service: "svc", HasError: true, Search: "timeout"}, true},
		{"servis + Errors + kök", TraceFilter{Service: "svc", HasError: true, RootOnly: true}, true},
		{"Errors tek yüklem → WHERE'de zaten (idx_status), gerek yok", TraceFilter{Service: "svc", HasError: true}, false},
		{"servis yok", TraceFilter{HasError: true, Filters: []FilterExpr{name}}, false},
		{"Errors yok", TraceFilter{Service: "svc", Filters: []FilterExpr{name}}, false},
		{"tek trace", TraceFilter{Service: "svc", HasError: true, Filters: []FilterExpr{name}, TraceID: "abc"}, false},
		{"DQL id listesi", TraceFilter{Service: "svc", HasError: true, Filters: []FilterExpr{name}, TraceIDs: []string{"a"}}, false},
	} {
		if got := errorFirstEligible(tc.f); got != tc.want {
			t.Errorf("%s: %v; want %v", tc.n, got, tc.want)
		}
	}
}

func TestErrorFirstFilterAndSQL(t *testing.T) {
	from := time.Date(2026, 9, 3, 3, 58, 24, 0, time.UTC)
	to := from.Add(3 * time.Hour)
	f := TraceFilter{Service: "svc", HasError: true, From: from, To: to, Env: "prod",
		Filters: []FilterExpr{{Key: "name", Op: "=", Values: []string{"POST"}}}, Search: "x", RootOnly: true, MinMs: 5}
	lf := errorFirstFilter(f)
	if lf.HasError || lf.Filters != nil || lf.Search != "" || lf.RootOnly || lf.MinMs != 0 || lf.Service != "svc" || lf.Env != "prod" {
		t.Errorf("errorFirstFilter yalnız pencere+servis+env/cluster taşımalı: %+v", lf)
	}
	wc := buildGetTracesWhere(lf, "")
	wc.add("status_code = 'error'")
	sql := errorFirstSQL(wc.sql())
	for _, want := range []string{"time >= ?", "time <= ?", "service_name = ?", "deploy_env = ?", "status_code = 'error'", "ORDER BY time DESC", "LIMIT ?", "max_execution_time = 10"} {
		if !strings.Contains(sql, want) {
			t.Errorf("%q yok:\n%s", want, sql)
		}
	}
	// v0.10.308: "name = ?" alt-dizesi "service_name = ?"'ın içinde de geçer —
	// sözcük sınırı şart (bkz. gate-kendi-metnini-ısırır dersi).
	for _, no := range []string{`(^|[^_a-z])name = \?`, `HAVING`, `GROUP BY`, `coremetry\.`} {
		if regexp.MustCompile(no).MatchString(sql) {
			t.Errorf("%q olmamalı (span filtreleri aşama 1/2'de):\n%s", no, sql)
		}
	}
	if strings.Count(sql, "?") != len(wc.args)+1 {
		t.Errorf("`?` %d ≠ args %d + LIMIT", strings.Count(sql, "?"), len(wc.args))
	}
}

func TestCandidateIDsOnlyShapeTheWhere(t *testing.T) {
	f := TraceFilter{Service: "svc", HasError: true, From: time.Now().Add(-time.Hour), To: time.Now(),
		Filters: []FilterExpr{{Key: "name", Op: "=", Values: []string{"POST"}}}, CandidateIDs: []string{"a", "b", "c"}}
	wc := buildGetTracesWhere(f, "")
	joined := strings.Join(wc.conds, " AND ")
	if !strings.Contains(joined, "trace_id IN (?,?,?)") {
		t.Errorf("aday listesi WHERE'e inmeli: %s", joined)
	}
	// Uygunluk kapıları aday listesini GÖRMEZ: ham yol probe/light seçimleri
	// ve MV uygunluğu TraceIDs'e bakar, CandidateIDs'e değil.
	f2 := f
	f2.Filters = nil
	if !tracesMVEligible(f2) {
		t.Error("CandidateIDs MV uygunluğunu kapatmamalı (filtre yokken MV yolu)")
	}
	f3 := f
	f3.Sort = "duration"
	if !rawListLightEligible(f3, false) {
		t.Error("CandidateIDs hafif yol uygunluğunu kapatmamalı")
	}
	if errorFirstEligible(f) {
		t.Error("adaylar zaten seçilmişse ikinci kez seçilmez")
	}
}

func TestErrorFirstRunsBeforeRawWhere(t *testing.T) {
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Store) GetTraces(")
	body := src[i:]
	ef := strings.Index(body, "errorFirstEligible(f)")
	wc := strings.Index(body, "wc := buildGetTracesWhere(f, s.clusterExpr())")
	if ef < 0 || wc < 0 || ef > wc {
		t.Errorf("hata-önce daraltma ham WHERE'den ÖNCE koşmalı (probe/light/runList hepsi wc'yi kullanır): ef=%d wc=%d", ef, wc)
	}
	if !strings.Contains(body[ef:wc], "return []TraceRow{}, 0, false, nil") {
		t.Error("hatalı trace yoksa BOŞ dönmeli — tam taramaya düşmemeli")
	}
}
