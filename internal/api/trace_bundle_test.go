package api

// trace_bundle_test.go — v0.10.671 (trace kiosk modu Dilim 1;
// docs/audit/trace-kiosk-mode-audit-2026-09-11.md §7-§9).
//
// SÖZLEŞME:
//   1. Rota kendi dosyasında ve defterde; api.go kaydetmez (logs_routes_test
//      emsali — kalıp düşerse istemci 404 değil "boş 200" görür).
//   2. Anahtar id + iki limiti taşır (limit cevabın UZUNLUĞUNU değiştirir;
//      v0.5.187 sınıfı): farklı girdi → farklı anahtar, aynı girdi → aynı.
//   3. Limit clamp: boş/0/negatif/çöp → varsayılan, tavan üstü → tavan.
//   4. spanWindow, hooks.ts traceLogWindow'un aynası: min(start)−buffer ..
//      max(end)+buffer; 0 zaman yok sayılır; kullanılabilir zaman yoksa ok=false.
//   5. bundleTruncation: span tavanı / log toplamı > sayfa ya da alt-sınır /
//      oracle satırı == limit → ilgili bayrak.
// Mutasyon (ölçülecek): anahtardan limiti düşürmek 2'yi, buffer'ı
// kaldırmak 4'ü düşürür.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestTraceBundleRouteLivesInOwnFile(t *testing.T) {
	src, err := os.ReadFile("trace_bundle.go")
	if err != nil {
		t.Fatal(err)
	}
	api := readAPISourceNoComments(t, "api.go")
	const pat = `"GET /api/traces/{id}/bundle"`
	if !strings.Contains(string(src), pat) {
		t.Errorf("trace_bundle.go %s kalıbını taşımıyor — rota DÜŞTÜ", pat)
	}
	if strings.Contains(api, pat) {
		t.Errorf("api.go %s kaydediyor — api.go BÜYÜMEZ", pat)
	}
	if !strings.Contains(string(src), `registerRoutesExtra("trace-bundle"`) {
		t.Error("trace_bundle.go deftere kayıt olmuyor")
	}
}

func TestTraceBundleKeyDistinctAndStable(t *testing.T) {
	a := traceBundleKey("abc", 500, 200)
	if a != traceBundleKey("abc", 500, 200) {
		t.Fatalf("aynı girdi farklı anahtar: %q", a)
	}
	for _, other := range []string{
		traceBundleKey("abd", 500, 200),
		traceBundleKey("abc", 1000, 200),
		traceBundleKey("abc", 500, 100),
	} {
		if other == a {
			t.Fatalf("farklı girdi aynı anahtar: %q", other)
		}
	}
}

func TestClampBundleLimit(t *testing.T) {
	cases := []struct {
		raw            string
		def, max, want int
	}{
		{"", 500, 1000, 500},
		{"0", 500, 1000, 500},
		{"-3", 500, 1000, 500},
		{"abc", 500, 1000, 500},
		{"250", 500, 1000, 250},
		{"1000", 500, 1000, 1000},
		{"5000", 500, 1000, 1000},
	}
	for _, c := range cases {
		if got := clampBundleLimit(c.raw, c.def, c.max); got != c.want {
			t.Errorf("clampBundleLimit(%q,%d,%d)=%d want %d", c.raw, c.def, c.max, got, c.want)
		}
	}
}

func TestSpanWindowMirrorsTraceLogWindow(t *testing.T) {
	buf := time.Minute
	t0 := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	sp := func(start, end time.Time) chstore.SpanRow {
		return chstore.SpanRow{StartTime: start.UnixNano(), EndTime: end.UnixNano()}
	}
	if _, _, ok := spanWindow(nil, buf); ok {
		t.Fatal("boş dilim → ok=false olmalı")
	}
	if _, _, ok := spanWindow([]chstore.SpanRow{{}}, buf); ok {
		t.Fatal("0 zamanlı span → ok=false olmalı (JS: min=Infinity)")
	}
	spans := []chstore.SpanRow{
		sp(t0.Add(5*time.Second), t0.Add(6*time.Second)),
		sp(t0, t0.Add(10*time.Second)),
		sp(t0.Add(2*time.Second), t0.Add(3*time.Second)),
	}
	from, to, ok := spanWindow(spans, buf)
	if !ok || !from.Equal(t0.Add(-buf)) || !to.Equal(t0.Add(10*time.Second).Add(buf)) {
		t.Fatalf("sırasız span'ler: from=%s to=%s ok=%v", from, to, ok)
	}
	// EndTime 0 olan satır (Tempo'dan gelebilir) yalnız min'e katılır.
	spans2 := []chstore.SpanRow{
		{StartTime: t0.UnixNano(), EndTime: 0},
		sp(t0.Add(time.Second), t0.Add(2*time.Second)),
	}
	from, to, ok = spanWindow(spans2, buf)
	if !ok || !from.Equal(t0.Add(-buf)) || !to.Equal(t0.Add(2*time.Second).Add(buf)) {
		t.Fatalf("end=0 satırı: from=%s to=%s ok=%v", from, to, ok)
	}
}

func TestBundleTruncation(t *testing.T) {
	cases := []struct {
		name string
		in   bundleTruncationInput
		want traceBundleTruncated
	}{
		{"hiçbiri", bundleTruncationInput{LogsTotal: 10, LogsLen: 10, OracleLen: 3, OracleLimit: 200}, traceBundleTruncated{}},
		{"span tavanı", bundleTruncationInput{SpanCapped: true, OracleLimit: 200}, traceBundleTruncated{Spans: true}},
		{"log toplam > sayfa", bundleTruncationInput{LogsTotal: 900, LogsLen: 500, OracleLimit: 200}, traceBundleTruncated{Logs: true}},
		{"log alt sınır", bundleTruncationInput{LogsTotal: 500, LogsLen: 500, LogsLowerBound: true, OracleLimit: 200}, traceBundleTruncated{Logs: true}},
		{"oracle limit dolu", bundleTruncationInput{OracleLen: 200, OracleLimit: 200}, traceBundleTruncated{Oracle: true}},
		{"oracle limit altı", bundleTruncationInput{OracleLen: 199, OracleLimit: 200}, traceBundleTruncated{}},
	}
	for _, c := range cases {
		if got := bundleTruncation(c.in); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}
