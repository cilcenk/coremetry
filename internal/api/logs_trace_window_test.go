package api

// logs_trace_window_test.go — v0.10.690 (operatör, prod: kiosk'ta trace'in
// logları görünüyor, Logs sayfasında aynı trace "log backend yavaş").
//
// Kök neden: Logs sayfası traceId ile PENCERE göndermiyor (tüm saklama
// süresi; ES trace kapsamında pencereyi kırpmaz) → 3 sn pivot bütçesi aşılıp
// {degraded}. Kiosk/trace sayfası span'lere çapalı pencereyle aynı logları
// alıyordu. SÖZLEŞME:
//   1. traceLogsWindowFallback yalnız iki sınır da boşken uygular; istemci
//      penceresi asla ezilmez; geçersiz pencere (hi ≤ lo) uygulanmaz.
//   2. Trace dalı pencere yokken s.store.TraceWindow'u çağırır (kaynak pini —
//      GetTrace ile aynı kademeli/sınırlı probe).
//   3. Türetilen pencere yanıtta ilan edilir (traceWindow).

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

func TestTraceLogsWindowFallback(t *testing.T) {
	lo, hi := time.Unix(1_700_000_000, 0), time.Unix(1_700_000_600, 0)
	f, ok := traceLogsWindowFallback(logstore.Filter{TraceID: "t"}, lo, hi)
	if !ok || !f.From.Equal(lo) || !f.To.Equal(hi) {
		t.Fatalf("boş pencere doldurulmalı: %+v %v", f, ok)
	}
	c := logstore.Filter{TraceID: "t", From: lo.Add(time.Minute), To: hi}
	if g, ok := traceLogsWindowFallback(c, lo, hi); ok || !g.From.Equal(lo.Add(time.Minute)) {
		t.Fatalf("istemci penceresi ezilmemeli: %+v %v", g, ok)
	}
	if _, ok := traceLogsWindowFallback(logstore.Filter{TraceID: "t"}, hi, lo); ok {
		t.Fatal("hi ≤ lo uygulanmamalı")
	}
	if _, ok := traceLogsWindowFallback(logstore.Filter{TraceID: "t"}, time.Time{}, hi); ok {
		t.Fatal("sıfır sınır uygulanmamalı")
	}
}

func TestLogsTraceBranchDerivesWindow(t *testing.T) {
	src, err := os.ReadFile("api_logs.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, `if f.TraceID != "" {`)
	j := strings.Index(s, "logstore.SearchWithTimeout(ctx, s.logs, f, 0)")
	if i < 0 || j < 0 || j < i {
		t.Fatal("trace dalı bulunamadı")
	}
	block := s[i:j]
	for _, want := range []string{"f.From.IsZero() && f.To.IsZero()", "s.store.TraceWindow(ctx, f.TraceID)", "traceLogsWindowFallback(f, lo, hi)"} {
		if !strings.Contains(block, want) {
			t.Errorf("trace dalı %q taşımalı", want)
		}
	}
	if !strings.Contains(s, `out["traceWindow"]`) {
		t.Error("türetilen pencere yanıtta ilan edilmeli")
	}
}
