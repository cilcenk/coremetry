package api

// sparkline_slots_test.go — v0.10.262 (CDV-1): 7 g → ≤120 slot; 1 s → ham
// (5 dk); slot içi toplam/ağırlıklı ortalama/max p99; servisler aynı grid.

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestSparklineSlotWidth(t *testing.T) {
	to := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	if w := sparklineSlotWidth(to.Add(-time.Hour), to, 120); w != 5*time.Minute {
		t.Errorf("1s → 5 dk, got %s", w)
	}
	if w := sparklineSlotWidth(to.Add(-7*24*time.Hour), to, 120); w != 85*time.Minute {
		t.Errorf("7g/120 = 84 dk → 85 dk (5 dk katı), got %s", w)
	}
	if w := sparklineSlotWidth(to.Add(-24*time.Hour), to, 120); w != 15*time.Minute {
		t.Errorf("24s/120 = 12 dk → 15 dk, got %s", w)
	}
}

func TestSparklineSlotsAggregate(t *testing.T) {
	to := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)
	var rows []chstore.ServiceSummaryRow
	for i := 0; i < 2016; i++ { // 7 g × 5 dk
		ts := from.Add(time.Duration(i) * 5 * time.Minute).UnixNano()
		rows = append(rows,
			chstore.ServiceSummaryRow{Service: "a", BucketStart: ts, SpanCount: 10, ErrorCount: 1, AvgMs: 100, P99Ms: float64(i % 7)},
			chstore.ServiceSummaryRow{Service: "b", BucketStart: ts, SpanCount: 30, ErrorCount: 0, AvgMs: 300, P99Ms: 1},
		)
	}
	out := sparklineSlots(rows, from, to, 120)
	if n := len(out["a"]); n < 100 || n > 120 {
		t.Fatalf("a slot sayısı %d, 100-120 bekleniyordu (2016 ham)", n)
	}
	if len(out["a"]) != len(out["b"]) || out["a"][0].T != out["b"][0].T {
		t.Error("servisler aynı grid'i paylaşmalı")
	}
	first := out["a"][0] // 85 dk = 17 kova
	if first.Spans != 170 || first.Errs != 17 || first.AvgMs != 100 || first.P99Ms != 6 {
		t.Errorf("slot toplamı: %+v", first)
	}
	// Ağırlıklı ortalama: a+b karışık slot → tek servis içinde ağırlık spans.
	var total uint64
	for _, p := range out["a"] {
		total += p.Spans
	}
	if total != 2016*10 {
		t.Errorf("toplam span korunmalı: %d", total)
	}
	// Kısa pencere: ham 5 dk satırlar birebir.
	short := rows[:24] // 1 saat
	o2 := sparklineSlots(short, from, from.Add(time.Hour), 120)
	if len(o2["a"]) != 12 || o2["a"][1].T-o2["a"][0].T != int64(5*time.Minute) {
		t.Errorf("1s penceresi ham 5 dk kalmalı: %d slot", len(o2["a"]))
	}
}
