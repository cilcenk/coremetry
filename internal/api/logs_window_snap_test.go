package api

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// v0.10.442 (log arama denetimi A6-V1) — anahtar penceresi: canlı pencere
// 10 dk sınıra oturur (süre korunur), mutlak/eski pencere ve boş sınırlar
// aynen; dört uç da yardımcıyı kullanır; sorgunun kendisi değişmez.
func TestSnapLogsKeyWindow(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 7, 33, 123456789, time.UTC)
	from, to := now.Add(-30*time.Minute), now
	a, b, ok := snapLogsKeyWindow(from, to, now, logsKeySnap)
	if !ok || !b.Equal(time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)) || b.Sub(a) != 30*time.Minute {
		t.Fatalf("canlı pencere: ok=%v %s–%s", ok, a, b)
	}
	// Aynı sınır içinde iki istek → aynı anahtar penceresi; sınır aşılınca değişir.
	a2, b2, _ := snapLogsKeyWindow(from.Add(2*time.Minute), to.Add(2*time.Minute), now.Add(2*time.Minute), logsKeySnap)
	if !a2.Equal(a) || !b2.Equal(b) {
		t.Fatalf("aynı sınır paylaşılmalı: %s–%s vs %s–%s", a, b, a2, b2)
	}
	if _, b3, _ := snapLogsKeyWindow(from.Add(3*time.Minute), to.Add(3*time.Minute), now.Add(3*time.Minute), logsKeySnap); !b3.After(b) {
		t.Fatalf("sınır aşımı yeni pencere: %s", b3)
	}
	// Mutlak (eski) pencere: dokunulmaz.
	oldFrom, oldTo := now.Add(-3*time.Hour), now.Add(-2*time.Hour)
	if a, b, ok := snapLogsKeyWindow(oldFrom, oldTo, now, logsKeySnap); ok || !a.Equal(oldFrom) || !b.Equal(oldTo) {
		t.Fatalf("mutlak pencere aynen kalmalı: ok=%v", ok)
	}
	// Boş sınır / gelecek / ters pencere: dokunulmaz.
	if _, _, ok := snapLogsKeyWindow(time.Time{}, to, now, logsKeySnap); ok {
		t.Fatal("boş from")
	}
	if _, _, ok := snapLogsKeyWindow(from, now.Add(time.Hour), now, logsKeySnap); ok {
		t.Fatal("gelecek to")
	}
	if _, _, ok := snapLogsKeyWindow(to, from, now, logsKeySnap); ok {
		t.Fatal("ters pencere")
	}
	// Yardımcı: canlı → oturtulmuş ns dizeleri; mutlak → ham dizeler; boş ham + dolu pencere → pencere.
	f1, t1 := logsKeyWindow(time.Now().Add(-time.Hour), time.Now(), "1", "2")
	if f1 == "1" || t1 == "2" {
		t.Fatalf("canlı pencere oturtulmalı: %s %s", f1, t1)
	}
	if n, _ := strconv.ParseInt(t1, 10, 64); n%int64(logsKeySnap) != 0 {
		t.Fatalf("to sınıra oturmalı: %s", t1)
	}
	if f2, t2 := logsKeyWindow(oldFrom, oldTo, "1", "2"); f2 != "1" || t2 != "2" {
		t.Fatalf("mutlak pencere ham dizeler: %s %s", f2, t2)
	}
	if f3, t3 := logsKeyWindow(oldFrom, oldTo, "", ""); f3 == "" || t3 == "" {
		t.Fatal("sunucu varsayılan penceresi anahtara girmeli")
	}
	if f4, t4 := logsKeyWindow(time.Time{}, time.Time{}, "", ""); f4 != "" || t4 != "" {
		t.Fatal("boş pencere boş kalır")
	}
}

// Dört uç (search, fieldstats, timeseries, patterns) anahtarı yardımcıdan
// alır; sorgunun f.From/f.To'su değişmez (V1 sözleşmesi).
func TestLogsKeyWindowWiredIntoAllFourHandlers(t *testing.T) {
	a, _ := os.ReadFile("api_logs.go")
	b, _ := os.ReadFile("api_logs_patterns.go")
	src := string(a) + string(b)
	if n := strings.Count(src, "logsKeyWindow(f.From, f.To, q.Get(\"from\"), q.Get(\"to\"))"); n != 4 {
		t.Fatalf("dört uç yardımcıyı kullanmalı, %d", n)
	}
	for _, old := range []string{`logsSearchKey(f, q.Get("from")`, `logsFieldStatsKey(field, f, q.Get("from")`, `logsTimeseriesKey(f, q.Get("from")`, `logsPatternsKey(f, q.Get("from")`} {
		if strings.Contains(src, old) {
			t.Fatalf("ham from/to hâlâ anahtara giriyor: %s", old)
		}
	}
	if strings.Contains(string(a), "f.From, f.To = ") || strings.Contains(string(b), "f.From, f.To = ") {
		t.Fatal("V1: sorgu penceresi oturtulmaz (V2 operatör kararı)")
	}
}
