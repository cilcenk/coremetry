package chstore

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// v0.10.326 — explain kaydı: nil alıcı sessiz; adım SQL/arg/süre/satır/hata
// taşır; GetTraces'in ham yolu adımları kaydeder (kaynak pini).
func TestTraceExplainStepAndNil(t *testing.T) {
	var nilX *TraceExplain
	nilX.note("x")
	nilX.step("a", "SELECT 1", nil, time.Now(), 0, nil) // panic yok
	x := &TraceExplain{}
	x.note("path=%s", "raw-list")
	from := time.Date(2026, 9, 3, 11, 20, 0, 0, time.UTC)
	x.step("list", "SELECT trace_id\n\t\tFROM spans WHERE time >= ? AND service_name = ?", []any{from, "svc", strings.Repeat("y", 200)}, time.Now().Add(-5*time.Millisecond), 51, errors.New("boom"))
	if len(x.Notes) != 1 || x.Notes[0] != "path=raw-list" {
		t.Errorf("notes: %v", x.Notes)
	}
	if len(x.Steps) != 1 {
		t.Fatalf("steps: %+v", x.Steps)
	}
	s := x.Steps[0]
	if s.SQL != "SELECT trace_id FROM spans WHERE time >= ? AND service_name = ?" || s.Rows != 51 || s.Err != "boom" || s.Ms < 4 {
		t.Errorf("step: %+v", s)
	}
	if s.Args[0] != "2026-09-03T11:20:00Z" || s.Args[1] != "svc" || !strings.HasSuffix(s.Args[2], "…") {
		t.Errorf("args: %v", s.Args)
	}
}

func TestGetTracesRecordsExplainSteps(t *testing.T) {
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, w := range []string{`f.Explain.step("list"`, `f.Explain.step("light-stage1"`, `f.Explain.step("light-stage2"`, `f.Explain.note("path=mv`, `f.Explain.note("path=raw-list`, `f.Explain.note("path=light`, `f.Explain.note("path=probe`} {
		if !strings.Contains(src, w) {
			t.Errorf("repo.go'da %s yok — teşhis kaydı eksik", w)
		}
	}
}

// v0.10.329 — boş liste öz-teşhisi: ne zaman istenir + sayım SQL sözleşmesi.
func TestEmptyDiagWantedAndCountSQL(t *testing.T) {
	if emptyDiagWanted(TraceFilter{Search: "x"}, 3) {
		t.Error("dolu sonuçta istenmez")
	}
	if emptyDiagWanted(TraceFilter{}, 0) {
		t.Error("daraltma yokken istenmez")
	}
	if emptyDiagWanted(TraceFilter{Search: "x", TraceID: "abc"}, 0) {
		t.Error("trace id aramasında istenmez")
	}
	for _, f := range []TraceFilter{{Search: "x"}, {Filters: []FilterExpr{{Key: "k", Op: "=", Values: []string{"v"}}}}, {HasError: true}} {
		if !emptyDiagWanted(f, 0) {
			t.Errorf("istenmeli: %+v", f)
		}
	}
	sql := countMatchingSpansSQL("WHERE time >= ? AND service_name = ?")
	if !strings.HasPrefix(sql, "SELECT count() FROM spans WHERE") || !strings.Contains(sql, "max_execution_time = 10") {
		t.Errorf("sayım SQL: %s", sql)
	}
}

// v0.10.530 — Operator-reported (prod): 1 saatlik pencerede aramalı liste
// boş; boş-durum metni "ham veri TTL'i aştı" dedi, oysa pencere saklama
// içindeydi ve arama metni span'lerde geçmiyordu. Ayıran sayım YÜKLEMSİZ
// olmalı: operatörün yazdığı hiçbir daraltma taşınmaz, yalnız kapsam taşınır.
// Bir yüklem sızarsa sayım yine 0 döner ve ipucu yeniden yalan söyler.
func TestServiceSpansFilterKeepsOnlyScope(t *testing.T) {
	from := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	x := &TraceExplain{}
	f := TraceFilter{
		Service: "api-gateway", From: from, To: to, Env: "prod", Cluster: "ocp-a", Explain: x,
		Search: "UPDATE", HasError: true, RootOnly: true, MinMs: 5, MaxMs: 900,
		AttrKey: "k", AttrVal: "v", TraceID: strings.Repeat("a", 32),
		TraceIDs: []string{"x"}, CandidateIDs: []string{"y"}, RequireServices: []string{"other"},
		Filters:    []FilterExpr{{Key: "http.method", Op: "=", Values: []string{"GET"}}},
		FilterRoot: &FilterGroup{Join: "AND", Filters: []FilterExpr{{Key: "k", Op: "=", Values: []string{"v"}}}},
		ExtraAttrs: []string{"a"}, MVGap: true, NoPromoted: true,
	}
	lf := serviceSpansFilter(f)
	keep := map[string]bool{"Service": true, "From": true, "To": true, "Env": true, "Cluster": true, "Explain": true}
	rv := reflect.ValueOf(lf)
	for i := 0; i < rv.NumField(); i++ {
		name := rv.Type().Field(i).Name
		if keep[name] {
			if rv.Field(i).IsZero() {
				t.Errorf("kapsam alanı %s taşınmadı", name)
			}
			continue
		}
		if !rv.Field(i).IsZero() {
			t.Errorf("yüklem alanı %s sızdı: %v", name, rv.Field(i))
		}
	}
	wc := buildGetTracesWhere(lf, clusterColExpr)
	sql := countMatchingSpansSQL(wc.sql())
	for _, want := range []string{"SELECT count() FROM spans", "time >= ?", "time <= ?", "service_name = ?", "deploy_env = ?", "max_execution_time = 10"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL %q eksik:\n%s", want, sql)
		}
	}
	for _, bad := range []string{"trace_id", "status_code", "parent_id", "duration", "multiSearch", "service_name IN", "attr_", "http.method"} {
		if strings.Contains(sql, bad) {
			t.Errorf("SQL yüklem taşıyor %q:\n%s", bad, sql)
		}
	}
	if got, want := len(wc.args), strings.Count(sql, "?"); got != want {
		t.Errorf("arg sayısı %d, yer tutucu %d: %s", got, want, sql)
	}
	// RequireServices tek başına Service'i düşürür (WHERE switch'i) — kapsam
	// filtresi onu taşımadığı için service_name = ? kalır.
	if strings.Contains(sql, "IN (") {
		t.Errorf("RequireServices sızdı: %s", sql)
	}
}

// v0.10.530 — kaynak pini: handler ikinci sayımı yalnız servis seçili VE
// eşleşen 0 iken ister; yanıt anahtarı serviceSpans.
func TestEmptyDiagServiceSpansWired(t *testing.T) {
	b, err := os.ReadFile("../api/api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`if f.Service != "" && cerr == nil && n == 0 {`,
		`s.store.CountServiceSpans(ctx, f)`,
		`diag["serviceSpans"] = sn`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("api.go %q içermiyor", want)
		}
	}
}
