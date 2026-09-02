package chstore

// trace_raw_light_test.go — v0.10.235 (Operator-reported, prod): attribute
// filtreli + süre sıralamalı 12 saatlik trace araması CH 241 (3.73 GiB)
// veriyordu. Hafif 1. aşama sözleşmesi: string durum YOK, aynı WHERE/HAVING,
// LIMIT ? OFFSET ?, spill ayarları, sıralama ifadeleri liste sorgusunun
// kolonlarıyla birebir; uygunluk yalnız sayısal sıralamalarda.

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTraceRawStage1LightSQL_Shape(t *testing.T) {
	from := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	wc := buildGetTracesWhere(TraceFilter{From: from, To: from.Add(12 * time.Hour), Service: "svc",
		Filters: []FilterExpr{{Key: "http.route", Op: "=", Values: []string{"/x"}}}}, clusterDeriveExpr)
	sql, ok := traceRawStage1LightSQL(wc.sql(), " HAVING countIf(service_name = ?) > 0", "duration", "DESC")
	if !ok {
		t.Fatal("duration must be light-eligible")
	}
	for _, bad := range []string{"anyIf(", "any(name)", "any(service_name)", "root_name", "root_svc"} {
		if strings.Contains(sql, bad) {
			t.Fatalf("light stage carries a string state %q:\n%s", bad, sql)
		}
	}
	for _, want := range []string{"SELECT trace_id", "FROM spans WHERE", "http_route", "GROUP BY trace_id HAVING countIf(service_name = ?) > 0",
		"ORDER BY (max(toUnixTimestamp64Nano(time) + duration) - toUnixTimestamp64Nano(min(time))) DESC, trace_id",
		"LIMIT ? OFFSET ?", "max_execution_time = 25", "max_bytes_before_external_group_by"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("light stage lacks %q:\n%s", want, sql)
		}
	}
	// Sıralama ifadeleri liste sorgusunun kolon tanımlarıyla birebir.
	list := buildGetTracesListSQL(wc.sql(), "", "dur_ms", "DESC")
	if !strings.Contains(list, "(max(toUnixTimestamp64Nano(time) + duration) -") {
		t.Fatal("list dur_ms expression changed — keep stage-1 in sync")
	}
	if s, _ := traceRawStage1LightSQL("", "", "spans", "ASC"); !strings.Contains(s, "ORDER BY count() ASC, trace_id") {
		t.Fatalf("spans sort: %s", s)
	}
	if s, _ := traceRawStage1LightSQL("", "", "status", "DESC"); !strings.Contains(s, "ORDER BY max(if(status_code = 'error', 1, 0)) DESC, trace_id") {
		t.Fatalf("status sort: %s", s)
	}
	if s, _ := traceRawStage1LightSQL("", "", "duration", "sideways"); !strings.Contains(s, " DESC, trace_id") {
		t.Fatalf("unknown order defaults to DESC: %s", s)
	}
	for _, sort := range []string{"service", "operation", "weird"} {
		if _, ok := traceRawStage1LightSQL("", "", sort, "DESC"); ok {
			t.Fatalf("sort %q must not be light-eligible (string key)", sort)
		}
	}
	// v0.10.239 — zaman sıralaması yalnız kök son-filtreli düşüşte: min(time).
	if s, ok := traceRawStage1LightSQL("", "", "time", "DESC"); !ok || !strings.Contains(s, "ORDER BY min(time) DESC, trace_id") {
		t.Fatalf("time sort light expr: %v %s", ok, s)
	}
}

func TestRawListLightEligible(t *testing.T) {
	cases := []struct {
		name string
		f    TraceFilter
		root bool
		want bool
	}{
		{"duration", TraceFilter{Sort: "duration"}, false, true},
		{"spans", TraceFilter{Sort: "spans"}, false, true},
		{"status", TraceFilter{Sort: "status"}, false, true},
		{"time → probe path (no root post-filter)", TraceFilter{Sort: "time"}, false, false},
		{"default (time), no root", TraceFilter{}, false, false},
		{"time + root post-filter → light full-window fallback (v0.10.239)", TraceFilter{Sort: "time"}, true, true},
		{"default (time) + root post-filter", TraceFilter{}, true, true},
		{"service (string key) even with root", TraceFilter{Sort: "service"}, true, false},
		{"operation (string key)", TraceFilter{Sort: "operation"}, false, false},
		{"trace id list already bounded", TraceFilter{Sort: "duration", TraceIDs: []string{"a"}}, false, false},
		{"trace id prefix already bounded", TraceFilter{Sort: "duration", TraceID: "ab"}, false, false},
	}
	for _, c := range cases {
		if got := rawListLightEligible(c.f, c.root); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// v0.10.237 — kaynak tükenmesinde pencere yarılanır (MV yolunun sabitleri),
// başka hatada asla; taban ve deneme tavanı aşılmaz.
func TestNarrowOnExhaustion(t *testing.T) {
	to := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	from := to.Add(-12 * time.Hour)
	mem := errors.New("code: 241, message: Query memory limit exceeded: would use 3.73 GiB")
	next, ok := narrowOnExhaustion(mem, from, to, 0)
	if !ok || !next.Equal(to.Add(-6*time.Hour)) {
		t.Fatalf("first exhaustion halves 12h → 6h: %v %v", next, ok)
	}
	next2, ok := narrowOnExhaustion(mem, next, to, 1)
	if !ok || !next2.Equal(to.Add(-3*time.Hour)) {
		t.Fatalf("second halves 6h → 3h: %v %v", next2, ok)
	}
	if _, ok := narrowOnExhaustion(mem, next2, to, 2); ok {
		t.Fatal("retry ceiling (2) must stop narrowing")
	}
	if _, ok := narrowOnExhaustion(mem, to.Add(-20*time.Minute), to, 0); ok {
		t.Fatal("window at/below the 30m floor must not narrow")
	}
	if _, ok := narrowOnExhaustion(errors.New("code: 62, syntax error"), from, to, 0); ok {
		t.Fatal("a non-resource error must surface unchanged")
	}
	if _, ok := narrowOnExhaustion(nil, from, to, 0); ok {
		t.Fatal("nil error is not a retry")
	}
	timeout := errors.New("code: 159, message: Timeout exceeded: elapsed 25.1 s")
	if _, ok := narrowOnExhaustion(timeout, from, to, 0); !ok {
		t.Fatal("timeout is resource exhaustion too")
	}
}

// v0.10.238 — rootOnly kök kontrolü 1. aşamanın adayları üstünde (sınırlı),
// tüm pencerede değil: aday penceresi ve sayfa kesimi sözleşmesi.
func TestRawLightStage1Window(t *testing.T) {
	if l, o := rawLightStage1Window(100, 51, false); l != 51 || o != 100 {
		t.Fatalf("no post-filter → page itself: %d/%d", l, o)
	}
	if l, o := rawLightStage1Window(0, 51, true); l != 204 || o != 0 {
		t.Fatalf("post-filter: 4×(limit+1) ≥ 200 from offset 0: %d/%d", l, o)
	}
	if l, _ := rawLightStage1Window(0, 11, true); l != 200 {
		t.Fatalf("floor 200: %d", l)
	}
	if l, _ := rawLightStage1Window(5000, 51, true); l != 5204 {
		t.Fatalf("deep page: offset + 4×(limit+1): %d", l)
	}
	if l, _ := rawLightStage1Window(9000, 51, true); l != traceStage2MaxIDs {
		t.Fatalf("cap at traceStage2MaxIDs: %d", l)
	}
}

func TestApplyRootFilterPage(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e", "f"}
	hasRoot := map[string]bool{"a": true, "c": true, "d": true, "f": true}
	got := applyRootFilterPage(ids, hasRoot, 0, 3)
	if len(got) != 3 || got[0] != "a" || got[1] != "c" || got[2] != "d" {
		t.Fatalf("order kept, rootless dropped, limit+1 taken: %v", got)
	}
	got = applyRootFilterPage(ids, hasRoot, 2, 3)
	if len(got) != 2 || got[0] != "d" || got[1] != "f" {
		t.Fatalf("offset applies AFTER filtering: %v", got)
	}
	if got := applyRootFilterPage(ids, hasRoot, 9, 3); got != nil {
		t.Fatalf("offset past the end → empty: %v", got)
	}
	if got := applyRootFilterPage(ids, map[string]bool{}, 0, 3); got != nil {
		t.Fatalf("nothing has a root → empty: %v", got)
	}
}

// TestRawLightStage2UsesLightHaving — v0.10.241 regresyon kapısı.
// Operator-reported: 7 gün + http.route filtresi → boş sayfa / 159. Kök
// neden: hafif iki-aşamalı yolun 2. aşaması havingSQL'i (rootOnly'nin TÜM
// pencereyi tarayan GLOBAL IN trace_summary_5m alt sorgusu dahil) taşıyordu;
// 238 alt sorguyu yalnız 1. aşamadan çıkarmıştı. Kök kontrolü id'ler
// üstünde filterRootTraces ile yapıldığı için 2. aşama HAFİF HAVING'i
// kullanmak ZORUNDA. Kaynak taraması: hafif blok içinde
// buildGetTracesListSQL lightHavingSQL/lightHavingArgs ile çağrılır,
// havingSQL/havingArgs ASLA.
func TestRawLightStage2UsesLightHaving(t *testing.T) {
	src, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "rawListLightEligible(f, rootPostFilter) {")
	if start < 0 {
		t.Fatal("hafif blok bulunamadı — test çapası kaydı")
	}
	end := strings.Index(text[start:], "served = true")
	if end < 0 {
		t.Fatal("hafif bloğun sonu (served = true) bulunamadı")
	}
	block := text[start : start+end]
	if !strings.Contains(block, "buildGetTracesListSQL(lwc.sql(), lightHavingSQL, sortCol, order)") {
		t.Error("2. aşama lightHavingSQL ile kurulmuyor")
	}
	if !strings.Contains(block, "append(args, lightHavingArgs...)") {
		t.Error("2. aşama lightHavingArgs bağlamıyor")
	}
	for _, bad := range []string{"(lwc.sql(), havingSQL,", "append(args, havingArgs...)"} {
		if strings.Contains(block, bad) {
			t.Errorf("2. aşama ağır HAVING taşıyor (%q) — rootOnly GLOBAL IN alt sorgusu tüm pencereyi tarar (v0.10.241)", bad)
		}
	}
}
