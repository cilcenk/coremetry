package api

// sparkline_slots_test.go — v0.10.262 (CDV-1): 7 g → ≤120 slot; 1 s → ham
// (5 dk); slot içi toplam/ağırlıklı ortalama/max p99; servisler aynı grid.

import (
	"os"
	"strings"
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

// v0.10.269 — satırlar CH'den zaten slot grid'inde geldiğinde reducer
// geçiştir: sayı korunur, değerler bire bir, grid origin aynı.
func TestSparklineSlotsPassThroughOnSlottedRows(t *testing.T) {
	to := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)
	width := sparklineSlotWidth(from, to, 120) // 85 dk
	var rows []chstore.ServiceSummaryRow
	n := int(to.Sub(from) / width)
	for i := 0; i < n; i++ {
		ts := from.Add(time.Duration(i) * width).UnixNano()
		rows = append(rows, chstore.ServiceSummaryRow{Service: "a", BucketStart: ts, SpanCount: uint64(i + 1), ErrorCount: 1, AvgMs: 42, P99Ms: 7})
	}
	out := sparklineSlots(rows, from, to, 120)
	if len(out["a"]) != n {
		t.Fatalf("geçiş: %d slot bekleniyordu, %d", n, len(out["a"]))
	}
	for i, p := range out["a"] {
		if p.Spans != uint64(i+1) || p.AvgMs != 42 || p.P99Ms != 7 || p.T != rows[i].BucketStart {
			t.Fatalf("slot %d değişti: %+v", i, p)
		}
	}
}

// v0.10.269 — BAĞLANMA pini (feedback-tested-but-unreachable): slot okuması
// var olsa da handler onu çağırmıyorsa eski 5-dk sorgu koşar (ilk imajda
// tam bu oldu: query_log arrayElement×3 gösterdi, result_rows 27.000).
func TestSparklinesHandlerUsesSlotReader(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Server) getServiceSparklines(")
	if i < 0 {
		t.Fatal("getServiceSparklines yok")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j > 0 {
		body = body[:j+1]
	}
	for _, want := range []string{"sparklineSlotWidth(from, to, sparklineMaxSlots)", "s.store.GetServiceSummarySlots(ctx, wantSvcs, from, to, width)", "services-spark:v3"} {
		if !strings.Contains(body, want) {
			t.Errorf("sparklines handler slot okumasına bağlı değil: %q yok", want)
		}
	}
	if strings.Contains(body, "GetServiceSummary5mFor(") {
		t.Error("handler hâlâ 5-dk okuyucuyu çağırıyor")
	}
}
