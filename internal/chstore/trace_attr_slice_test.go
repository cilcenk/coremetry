package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

// trace_attr_slice_test.go — v0.10.301: hangi filtreler indeks dilimini
// kurar, SQL şekli (zaman sınırı + LIMIT + max_execution_time + kolonlar),
// bağlama sırası, ve getTracesFromMV'nin diliminin servis/recency
// dallarından ÖNCE geldiğinin kaynak pini.

func TestAttrSlicePredicates(t *testing.T) {
	withAttrIndex(t, true)
	eq := FilterExpr{Key: "banking.txn_ref", Op: "=", Values: []string{"T1"}}
	in := FilterExpr{Key: "resource.k8s.pod.uid", Op: "IN", Values: []string{"a", "b"}}
	ex := FilterExpr{Key: "error.type", Op: "EXISTS"}
	neq := FilterExpr{Key: "channel", Op: "!=", Values: []string{"x"}}
	rx := FilterExpr{Key: "channel", Op: "=~", Values: []string{"a.*"}}
	known := FilterExpr{Key: "service.name", Op: "=", Values: []string{"api"}}

	sql, args, ok := attrSlicePredicates(TraceFilter{Filters: []FilterExpr{eq, neq, rx, known, ex, in}})
	if !ok {
		t.Fatal("indeksli yüklem varken ok olmalı")
	}
	for _, want := range []string{"has(attr_kvh, ", "has(attr_keys, ?)", "hasAny(res_kvh, "} {
		if !strings.Contains(sql, want) {
			t.Errorf("%q yok: %s", want, sql)
		}
	}
	for _, no := range []string{"!=", "match(", "service_name"} {
		if strings.Contains(sql, no) {
			t.Errorf("indekssiz yüklem dilime girmemeli (%q): %s", no, sql)
		}
	}
	if strings.Count(sql, "?") != len(args) {
		t.Errorf("`?` %d ≠ args %d", strings.Count(sql, "?"), len(args))
	}
	// Yalnız indekssiz yüklemler → dilim yok.
	if _, _, ok := attrSlicePredicates(TraceFilter{Filters: []FilterExpr{neq, rx, known}}); ok {
		t.Error("indekssiz yüklemlerle dilim kurulmamalı")
	}
	// OR grubu → dilim yok (bir kol indekssiz olabilir).
	orRoot := &FilterGroup{Join: "OR", Filters: []FilterExpr{eq, neq}}
	if _, _, ok := attrSlicePredicates(TraceFilter{FilterRoot: orRoot}); ok {
		t.Error("OR grubu dilim kurmamalı")
	}
	// İç içe AND grubu → yapraklar düzleşir.
	andRoot := &FilterGroup{Join: "AND", Filters: []FilterExpr{neq}, Groups: []FilterGroup{{Join: "AND", Filters: []FilterExpr{eq}}}}
	if sql, _, ok := attrSlicePredicates(TraceFilter{FilterRoot: andRoot}); !ok || !strings.Contains(sql, "attr_kvh") {
		t.Errorf("AND ağacı düzleşmeli: %v %s", ok, sql)
	}
	// İndeks yokken hiç.
	withAttrIndex(t, false)
	if _, _, ok := attrSlicePredicates(TraceFilter{Filters: []FilterExpr{eq}}); ok {
		t.Error("indeks kapalıyken dilim kurulmamalı")
	}
}

func TestTraceAttrSliceSQLShape(t *testing.T) {
	s := &Store{}
	sql := s.traceAttrSliceSQL(TraceFilter{Service: "api", Env: "prod", Order: "asc"}, "has(attr_kvh, cityHash64(concat(?, '\\x1F', ?)))")
	for _, want := range []string{"SELECT trace_id, toStartOfFiveMinute(time) AS time_bucket", "FROM spans", "time >= ? AND time < ?", "AND service_name = ?", "AND deploy_env = ?", "AND has(attr_kvh, ", "ORDER BY time ASC", "LIMIT ?", "max_execution_time = 10"} {
		if !strings.Contains(sql, want) {
			t.Errorf("%q yok:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "coremetry.") {
		t.Error("telemetri tablosu şemasız anılmalı")
	}
	plain := s.traceAttrSliceSQL(TraceFilter{}, "x")
	if strings.Contains(plain, "service_name") || strings.Contains(plain, "deploy_env") || !strings.Contains(plain, "ORDER BY time DESC") {
		t.Errorf("servissiz/env'siz şekil: %s", plain)
	}
	// Bağlama sırası: from, to, service, env, yüklem args, (LIMIT scanIDSlice ekler).
	if _, _, _, err := s.traceAttrSlice(nil, TraceFilter{}, 0, "", nil); err != nil {
		t.Errorf("want=0 → sessiz boş: %v", err)
	}
	_ = time.Now()
}

func TestAttrSliceRunsBeforeServiceAndRecencySlices(t *testing.T) {
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Store) getTracesFromMV(")
	if i < 0 {
		t.Fatal("getTracesFromMV yok")
	}
	body := src[i:]
	attr := strings.Index(body, "attrSlicePredicates(f)")
	svc := strings.Index(body, "s.traceServiceSlice(ctx, s1f, want)")
	rec := strings.Index(body, "s.traceRecencySlice(ctx, s1f, budget, errorsOnly)")
	if attr < 0 || svc < 0 || rec < 0 || !(attr < svc && svc < rec) {
		t.Errorf("sıra attr → service → recency olmalı: attr=%d svc=%d rec=%d", attr, svc, rec)
	}
	if !strings.Contains(body[attr:svc], "return []TraceRow{}, 0, false, nil") {
		t.Error("boş attr dilimi BOŞ döner — recency dilimine düşmez")
	}
}
