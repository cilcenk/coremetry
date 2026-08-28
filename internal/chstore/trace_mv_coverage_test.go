package chstore

import (
	"testing"
	"time"
)

// v0.10.124 — MV boşluğu farkında okuma: pencere → gün eşlemesi ve
// eligibility kapısı. Operator-reported: "servis seçince 2 gün öncesi
// boş, servis + operation seçince geliyor".
func TestTraceWindowTouchesGap(t *testing.T) {
	d := func(s string) time.Time { x, _ := time.Parse("2006-01-02 15:04", s); return x }
	gaps := map[string]bool{"2026-08-26": true, "2026-08-25": true}
	cases := []struct {
		name     string
		from, to string
		want     bool
	}{
		{"pencere boş günün içinde", "2026-08-26 10:00", "2026-08-26 11:00", true},
		{"dolu günler", "2026-08-27 10:00", "2026-08-28 10:00", false},
		{"iki gün, ilki boş", "2026-08-25 23:00", "2026-08-27 01:00", true},
		{"from boş günün son dakikası", "2026-08-26 23:59", "2026-08-27 00:30", true},
		{"ters pencere", "2026-08-27 00:00", "2026-08-26 00:00", false},
	}
	for _, c := range cases {
		if got := traceWindowTouchesGap(d(c.from), d(c.to), gaps); got != c.want {
			t.Errorf("%s: %v, istenen %v", c.name, got, c.want)
		}
	}
	if traceWindowTouchesGap(d("2026-08-26 10:00"), d("2026-08-26 11:00"), nil) {
		t.Error("boş harita boşluk üretti")
	}
	f := TraceFilter{From: d("2026-08-26 00:00"), To: d("2026-08-27 00:00"), CountMode: "skip"}
	if !tracesMVEligible(f) {
		t.Fatal("temel filtre MV'ye uygun olmalı")
	}
	f.MVGap = true
	if tracesMVEligible(f) {
		t.Error("MVGap MV yolunu düşürmedi")
	}
	if _, _, _, reason := traceCountPlan(f); reason != traceCountReasonRawPath {
		t.Errorf("sayım planı ham yola düşmedi: %q", reason)
	}
}

// TestTraceMVCoverageCacheTTL — TTL içinde harita yeniden sorulmaz
// (conn nil olsa da çalışır); dolu gün boşluk sayılmaz.
func TestTraceMVCoverageCacheTTL(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	s := &Store{}
	s.mvCoverage.now = func() time.Time { return now }
	s.mvCoverage.gaps = map[string]bool{"2026-08-26": true}
	s.mvCoverage.fetched = now
	if !s.TraceMVGap(nil, time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC), time.Date(2026, 8, 26, 2, 0, 0, 0, time.UTC)) {
		t.Fatal("önbellekteki boşluk görülmedi")
	}
	if s.TraceMVGap(nil, time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC), time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC)) {
		t.Fatal("dolu gün boşluk sayıldı")
	}
}
