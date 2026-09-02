package api

// sparkline_slots_test.go — v0.10.262 (CDV-1): 7 g → ≤120 slot; 1 s → ham
// (5 dk); slot içi toplam/ağırlıklı ortalama/max p99; servisler aynı grid.

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
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
	// v0.10.286 — sabit 120 yerine basamaklı maxSlots; anahtar v4.
	for _, want := range []string{"sparklineSlotWidth(from, to, maxSlots)", "s.store.GetServiceSummarySlots(ctx, wantSvcs, from, to, width)", "services-spark:v4"} {
		if !strings.Contains(body, want) {
			t.Errorf("sparklines handler slot okumasına bağlı değil: %q yok", want)
		}
	}
	if strings.Contains(body, "GetServiceSummary5mFor(") {
		t.Error("handler hâlâ 5-dk okuyucuyu çağırıyor")
	}
}

// v0.10.286 (D1 / Dilim 1.7) — istemci bütçesi basamağa snap; anahtar taşır.
func TestSparklineSlotRung(t *testing.T) {
	for _, tc := range []struct{ want, rung int }{
		{0, 120}, {-5, 120}, {1, 40}, {40, 40}, {41, 60}, {60, 60}, {61, 80}, {80, 80}, {81, 120}, {120, 120}, {500, 120},
	} {
		if got := sparklineSlotRung(tc.want); got != tc.rung {
			t.Errorf("sparklineSlotRung(%d) = %d; want %d", tc.want, got, tc.rung)
		}
	}
	// Basamak, 7 günlük pencerede slot genişliğini gerçekten değiştirir
	// (40 slot → 7g/40 = 4.2 s → 5 dk katına yuvarlanmış 255 dk).
	to := time.Unix(1_788_000_000, 0)
	if w := sparklineSlotWidth(to.Add(-7*24*time.Hour), to, 40); w != 255*time.Minute {
		t.Errorf("7g/40 slot genişliği %v; 255 dk bekleniyordu", w)
	}
}

func TestSparklinesHandlerHonoursMaxSlots(t *testing.T) {
	src := readAPISourceNoComments(t, "api.go")
	i := strings.Index(src, "func (s *Server) getServiceSparklines(")
	if i < 0 {
		t.Fatal("getServiceSparklines bulunamadı")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	for _, want := range []string{`sparklineSlotRung(parseInt(q.Get("maxSlots"), 0))`, "services-spark:v4", "max=%d", "sparklineSlots(rows, from, to, maxSlots)"} {
		if !strings.Contains(body, want) {
			t.Errorf("handler %q taşımıyor — maxSlots ya okunmuyor ya anahtara/gövdeye girmiyor", want)
		}
	}
	if strings.Contains(body, "sparklineSlots(rows, from, to, sparklineMaxSlots)") {
		t.Error("handler hâlâ sabit 120 ile dilimliyor")
	}
}

// TestSparklineRungsMatchFrontend — FE lib/sparkline.ts SPARK_SLOT_RUNGS ile
// Go sparklineSlotRungs birebir (chstore/route_pins_test.go deseni: iki
// dilin sabitleri tek testte). Ayrışırsa FE'nin istediği basamak sunucuda
// başka basamağa yuvarlanır ve cache anahtarı beklenmedik biçimde çoğalır.
func TestSparklineRungsMatchFrontend(t *testing.T) {
	b, err := os.ReadFile("../../frontend/src/lib/sparkline.ts")
	if err != nil {
		t.Fatalf("sparkline.ts okunamadı: %v", err)
	}
	m := regexp.MustCompile(`(?s)export const SPARK_SLOT_RUNGS = \[(.*?)\]`).FindSubmatch(b)
	if m == nil {
		t.Fatal("SPARK_SLOT_RUNGS bulunamadı — yeniden adlandırıldıysa bu pin de taşınmalı")
	}
	var fe []int
	for _, tok := range regexp.MustCompile(`\d+`).FindAllString(string(m[1]), -1) {
		n, _ := strconv.Atoi(tok)
		fe = append(fe, n)
	}
	if fmt.Sprint(fe) != fmt.Sprint(sparklineSlotRungs) {
		t.Errorf("FE basamakları %v ≠ Go %v", fe, sparklineSlotRungs)
	}
}
