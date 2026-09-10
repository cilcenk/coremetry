package anomaly

import (
	"context"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.592 — tarayıcının ürettiği ext-down / ext-cap kural kimlikleri
// chstore.PollerOwnedRule tarafından TANINMALI; seri Problem'i ise değil.
// Önek literal'i ile yüklem ayrışırsa süpürme yine kapatır — sessizce.
func TestPollerOwnedRuleIDsAreRecognised(t *testing.T) {
	f := &fakeExtStore{cfg: chstore.DefaultAnomalySensitivity()}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	s := newExtScanner(f, now)
	for i := 0; i < externalDownAfter; i++ {
		s.ReportSourceHealth(context.Background(), "o-1", "oracle-errlog", "down", now.Add(time.Duration(i)*time.Minute))
	}
	if len(f.upserts) != 1 || !chstore.PollerOwnedRule(f.upserts[0].RuleID) {
		t.Fatalf("ext-down kuralı poller'a ait sayılmalı: %+v", f.upserts)
	}
	cfg := chstore.DefaultAnomalySensitivity()
	cfg.ExternalOpenCapPerTick = 1
	f2 := &fakeExtStore{cfg: cfg, series: spikingSeries(3, now, cfg.DwellBuckets, 60)}
	if _, err := newExtScanner(f2, now).Scan(context.Background(), extTarget); err != nil {
		t.Fatal(err)
	}
	var cap, series int
	for _, p := range f2.upserts {
		if chstore.PollerOwnedRule(p.RuleID) {
			cap++
		} else {
			series++
		}
	}
	if cap != 1 || series != 1 {
		t.Fatalf("özet Problem poller'a ait (1), seri Problem'i değil (1): cap=%d series=%d", cap, series)
	}
}
