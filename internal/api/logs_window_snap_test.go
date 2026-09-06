package api

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
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

// v0.10.446 (A6-V2) — dört uç canlı pencereyi KENDİ TTL'ine oturtur ve
// sorguyu da taşır; mutlak pencere aynen; V1 yardımcısı çağrılmaz.
func TestSnapLogsWindowV2(t *testing.T) {
	now := time.Now()
	f := logstore.Filter{From: now.Add(-time.Hour), To: now}
	kf, kt := snapLogsWindow(&f, "1", "2", 15*time.Second)
	if f.To.UnixNano()%int64(15*time.Second) != 0 || f.To.Sub(f.From) != time.Hour || kf == "1" || kt == "2" {
		t.Fatalf("canlı pencere TTL'e oturmalı, süre korunmalı: %s–%s (%s %s)", f.From, f.To, kf, kt)
	}
	if now.Sub(f.To) > 15*time.Second {
		t.Fatalf("sağ uç TTL'den fazla geriye kaymamalı: %s", now.Sub(f.To))
	}
	if n, _ := strconv.ParseInt(kt, 10, 64); n != f.To.UnixNano() {
		t.Fatal("anahtar sorgu penceresiyle aynı olmalı")
	}
	old := logstore.Filter{From: now.Add(-3 * time.Hour), To: now.Add(-2 * time.Hour)}
	of, ot := old.From, old.To
	if kf, kt := snapLogsWindow(&old, "1", "2", 15*time.Second); kf != "1" || kt != "2" || !old.From.Equal(of) || !old.To.Equal(ot) {
		t.Fatal("mutlak pencere aynen kalmalı")
	}
	empty := logstore.Filter{}
	if kf, kt := snapLogsWindow(&empty, "", "", 15*time.Second); kf != "" || kt != "" || !empty.From.IsZero() {
		t.Fatal("boş pencere boş kalır")
	}
}

func TestLogsWindowSnapWiredIntoAllFourHandlers(t *testing.T) {
	a, _ := os.ReadFile("api_logs.go")
	b, _ := os.ReadFile("api_logs_patterns.go")
	src := string(a) + string(b)
	for _, want := range []string{
		`snapLogsWindow(&f, q.Get("from"), q.Get("to"), 15*time.Second)`,
		`snapLogsWindow(&f, q.Get("from"), q.Get("to"), 60*time.Second)`,
		`snapLogsWindow(&f, q.Get("from"), q.Get("to"), 30*time.Second)`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("uç TTL'iyle snap yok: %s", want)
		}
	}
	if n := strings.Count(src, "snapLogsWindow(&f,"); n != 4 {
		t.Fatalf("dört uç snap'lemeli, %d", n)
	}
	if strings.Contains(src, "logsKeyWindow(") {
		t.Fatal("V1 yardımcısı handler'larda kalmamalı")
	}
	// Histogram: snap taban kovadan ÖNCE (kova pencereye göre seçilir).
	i, j := strings.Index(string(a), `30*time.Second) // v0.10.446`), strings.Index(string(a), "bucketSec = floorBucketByWindow(")
	if i < 0 || j < 0 || i > j {
		t.Fatal("timeseries snap'i floorBucketByWindow'dan önce olmalı")
	}
}
