package anomaly

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/correlator"
)

// rootcause_temporal_test.go — v0.10.700. Servis seçimi, pencere ve işçi
// kablolaması (iki anchor yolu da Synthesize ÖNCESİ attachTemporal çağırır).
func TestTemporalServices(t *testing.T) {
	nbs := []correlator.ScoredCause{
		{Service: "shop-db", Score: 0.6}, {Service: "node:w1", Kind: "node", Score: 0.9},
		{Service: "shop-db", Score: 0.2}, {Service: "", Score: 0.5},
		{Service: "shop-cache", Score: 0.1}, {Service: "shop-x", Score: 0.05},
	}
	got := temporalServices("shop-api", nbs, 2)
	if strings.Join(got, ",") != "shop-api,shop-db,shop-cache" {
		t.Fatalf("anchor önde, node/boş/tekrar dışarıda, tavan 2: %v", got)
	}
	if len(temporalServices("shop-api", nil, 10)) != 1 {
		t.Fatal("komşusuz → yalnız anchor")
	}
}

func TestTemporalWindow(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 3, 20, 0, time.UTC)
	onset := time.Date(2026, 9, 12, 9, 40, 0, 0, time.UTC)
	from, to := temporalWindow(onset.UnixNano(), now)
	if !to.Equal(time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("üst sınır son TAM kova: %v", to)
	}
	if !from.Equal(onset.Add(-evidenceWindow)) {
		t.Fatalf("alt sınır onset−60m: %v", from)
	}
	old := now.Add(-9 * time.Hour)
	from, to = temporalWindow(old.UnixNano(), now)
	if to.Sub(from) != temporalMaxWindow {
		t.Fatalf("eski onset 3 saate kırpılmalı: %v..%v", from, to)
	}
	from, to = temporalWindow(0, now)
	if to.Sub(from) != evidenceWindow {
		t.Fatalf("onset yoksa son 60 dk: %v..%v", from, to)
	}
	future := now.Add(2 * time.Hour)
	from, to = temporalWindow(future.UnixNano(), now)
	if from.After(to) {
		t.Fatal("gelecek onset ters pencere üretmemeli")
	}
}

func TestTemporalWorkerWiring(t *testing.T) {
	b, err := os.ReadFile("rootcause_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Count(src, "s.attachTemporal(ctx, ") != 2 {
		t.Fatalf("iki anchor yolu (anomali + problem) attachTemporal çağırmalı, %d", strings.Count(src, "s.attachTemporal(ctx, "))
	}
	a := strings.Index(src, `s.attachTemporal(ctx, ev.Service, ev.StartedAt, now, &synthIn)`)
	sa := strings.Index(src, `"anomaly", ev.ID, ev.Service, now.UnixNano(),`)
	if a < 0 || sa < 0 || a > sa {
		t.Error("anomali yolu: attachTemporal Synthesize'dan ÖNCE olmalı")
	}
	p := strings.Index(src, `s.attachTemporal(ctx, p.Service, p.StartedAt, now, &synthIn)`)
	sp := strings.Index(src, `"problem", p.ID, p.Service, now.UnixNano(),`)
	if p < 0 || sp < 0 || p > sp {
		t.Error("problem yolu: attachTemporal Synthesize'dan ÖNCE olmalı")
	}
}
