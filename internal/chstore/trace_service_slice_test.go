package chstore

// trace_service_slice_test.go — v0.10.265 regresyon pini. Operatör-raporlu
// (prod v0.10.239, 7g + servis + hasError, zaman sıralı): her "Next" 40+ s,
// "shortened" rozeti. Ölçüm (lokal v0.10.264, 7g, api-gateway = 409k/7g
// trace): stage 2 `trace_id GLOBAL IN (index GROUP BY trace_id)` 1.7-2.0M
// satır okuyup 12 s tavanına takıldı (iki koşu, iki yarılama); düz servis
// yolu `GROUP BY trace_id ORDER BY maxMerge(last_seen)` 800k satır 10-15 s
// ve asc'yi yok sayıyordu. Şimdi: trace_service_index_5m PK sırasında
// akışkan dilim (GROUP BY yok) → holders + kesim.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestServiceSliceSQLStreamsInIndexOrder(t *testing.T) {
	s := &Store{}
	for _, tc := range []struct{ order, dir string }{{"", "DESC"}, {"desc", "DESC"}, {"asc", "ASC"}} {
		sql := s.traceServiceSliceScanSQL(tc.order)
		if !strings.Contains(sql, "FROM trace_service_index_5m") || !strings.Contains(sql, "service_name = ?") {
			t.Fatalf("%q: servis indeksi + servis yüklemi yok:\n%s", tc.order, sql)
		}
		if !strings.Contains(sql, "ORDER BY time_bucket "+tc.dir) {
			t.Errorf("%q: ORDER BY time_bucket %s bekleniyordu:\n%s", tc.order, tc.dir, sql)
		}
		if strings.Contains(sql, "GROUP BY") || strings.Contains(sql, "Merge(") {
			t.Errorf("%q: dilim toplama YAPMAMALI (PK sırası akış):\n%s", tc.order, sql)
		}
		if !strings.Contains(sql, "optimize_read_in_order = 1") || !strings.Contains(sql, "LIMIT ?") || !strings.Contains(sql, "max_execution_time = 10") {
			t.Errorf("%q: in-order okuma / LIMIT / tavan eksik:\n%s", tc.order, sql)
		}
	}
}

func TestServiceSlicePlan(t *testing.T) {
	cases := []struct {
		name      string
		f         TraceFilter
		wantN     int
		wantOrder string
		sliced    bool
	}{
		{"düz zaman desc sayfa 1 — stage1Limit tabanı (pageLimit×10)", TraceFilter{Service: "gw", Sort: "time", Order: "desc", Offset: 0}, 510, "desc", false},
		{"düz zaman asc — yön korunur (eski yol yok sayıyordu)", TraceFilter{Service: "gw", Sort: "time", Order: "asc", Offset: 50}, 510, "asc", false},
		{"düz zaman derin sayfa — 2×(offset+limit)", TraceFilter{Service: "gw", Order: "desc", Offset: 500}, 1102, "desc", false},
		{"id tavanı ötesi — tavan, boş sayfa", TraceFilter{Service: "gw", Order: "desc", Offset: 7000}, traceStage2MaxIDs, "desc", false},
		{"hasError → üst-küme N, yön f.Order", TraceFilter{Service: "gw", Sort: "time", Order: "asc", HasError: true}, traceRecencySliceN, "asc", true},
		{"rootOnly → üst-küme N", TraceFilter{Service: "gw", Order: "desc", RootOnly: true}, traceRecencySliceN, "desc", true},
		{"süre sıralaması → en yeni N DESC (asc istense de)", TraceFilter{Service: "gw", Sort: "duration", Order: "asc"}, traceRecencySliceN, "desc", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, order, sliced := serviceSlicePlan(tc.f, 51, 510)
			if n != tc.wantN || order != tc.wantOrder || sliced != tc.sliced {
				t.Fatalf("plan = (%d, %q, %v), istenen (%d, %q, %v)", n, order, sliced, tc.wantN, tc.wantOrder, tc.sliced)
			}
		})
	}
}

func TestSliceStageBounds(t *testing.T) {
	cut := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	if fl, ce := sliceStageBounds("desc", cut, false); !fl.Equal(cut) || !ce.IsZero() {
		t.Errorf("desc: taban kesim, tavan yok; (%v, %v)", fl, ce)
	}
	if fl, ce := sliceStageBounds("asc", cut, false); !fl.IsZero() || !ce.Equal(cut) {
		t.Errorf("asc: taban SIFIR (id'ler pencerenin başında), TAVAN kesim; (%v, %v)", fl, ce)
	}
	if fl, ce := sliceStageBounds("desc", cut, true); !fl.IsZero() || !ce.IsZero() {
		t.Errorf("tükenmiş dilim: daraltma yok; (%v, %v)", fl, ce)
	}
	if fl, ce := sliceStageBounds("asc", time.Time{}, false); !fl.IsZero() || !ce.IsZero() {
		t.Errorf("kesimsiz: daraltma yok; (%v, %v)", fl, ce)
	}
}

// v0.10.267 — tavan adayı tam tavanı aşamaz; lookahead ×8 genişlemesi
// runTraceStage2'de tabanın aynasıdır (kaynak pini aşağıda).
func TestStage2Ceiling(t *testing.T) {
	cut := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	full := cut.Add(48 * time.Hour)
	if got := stage2Ceiling(cut, time.Hour, full); !got.Equal(cut.Add(time.Hour)) {
		t.Errorf("kesim+lookahead bekleniyordu, %v", got)
	}
	if got := stage2Ceiling(cut, 96*time.Hour, full); !got.Equal(full) {
		t.Errorf("tam tavanı aşmamalı, %v", got)
	}
	if got := stage2Ceiling(time.Time{}, time.Hour, full); !got.Equal(full) {
		t.Errorf("tavansız → tam tavan, %v", got)
	}
	b, err := os.ReadFile("trace_slice.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"lookahead *= 8", "aggTo = stage2Ceiling(ceil, lookahead, fullAggTo)", "!lastBucket.Before(ceilBucket)"} {
		if !strings.Contains(src, want) {
			t.Errorf("runTraceStage2 tavan genişletmesi/tespiti eksik: %q", want)
		}
	}
	rb, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rb), "max(time_bucket)                                            AS last_bucket") {
		t.Error("stage 2 SELECT last_bucket döndürmüyor — tavan kırpması tespit edilemez")
	}
}

// Kaynak pini: zaman aşımına giren şekil (stage 2'de servis indeksi
// alt-sorgusu) repo.go'ya geri dönmesin; servis yolu dilimden geçsin.
func TestNoServiceIndexSubqueryInStage2(t *testing.T) {
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, "GLOBAL IN (\n\t\t\tSELECT trace_id FROM trace_service_index_5m") {
		t.Fatal("stage 2 yine servis indeksi alt-sorgusuyla tüm pencereyi topluyor (v0.10.265 öncesi şekil)")
	}
	if strings.Contains(src, "ORDER BY maxMerge(last_seen_state) DESC") {
		t.Fatal("servisli 1. aşama yine GROUP BY + maxMerge sıralaması (pencereyle lineer, asc'yi yok sayar)")
	}
	if !strings.Contains(src, "s.traceServiceSlice(") {
		t.Fatal("servis yolu traceServiceSlice'tan geçmiyor")
	}
}
