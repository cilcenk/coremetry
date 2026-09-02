package chstore

// trace_raw_light_test.go — v0.10.235 (Operator-reported, prod): attribute
// filtreli + süre sıralamalı 12 saatlik trace araması CH 241 (3.73 GiB)
// veriyordu. Hafif 1. aşama sözleşmesi: string durum YOK, aynı WHERE/HAVING,
// LIMIT ? OFFSET ?, spill ayarları, sıralama ifadeleri liste sorgusunun
// kolonlarıyla birebir; uygunluk yalnız sayısal sıralamalarda.

import (
	"errors"
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
	for _, sort := range []string{"", "time", "service", "operation", "weird"} {
		if _, ok := traceRawStage1LightSQL("", "", sort, "DESC"); ok {
			t.Fatalf("sort %q must not be light-eligible (string key or probe path)", sort)
		}
	}
}

func TestRawListLightEligible(t *testing.T) {
	cases := []struct {
		name string
		f    TraceFilter
		want bool
	}{
		{"duration", TraceFilter{Sort: "duration"}, true},
		{"spans", TraceFilter{Sort: "spans"}, true},
		{"status", TraceFilter{Sort: "status"}, true},
		{"time → probe path", TraceFilter{Sort: "time"}, false},
		{"default (time)", TraceFilter{}, false},
		{"service (string key)", TraceFilter{Sort: "service"}, false},
		{"operation (string key)", TraceFilter{Sort: "operation"}, false},
		{"trace id list already bounded", TraceFilter{Sort: "duration", TraceIDs: []string{"a"}}, false},
		{"trace id prefix already bounded", TraceFilter{Sort: "duration", TraceID: "ab"}, false},
	}
	for _, c := range cases {
		if got := rawListLightEligible(c.f); got != c.want {
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
