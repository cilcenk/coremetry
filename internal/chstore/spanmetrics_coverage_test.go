package chstore

// v0.10.520 — spanmetrics kapsam başlangıcı part üst verisinden. Lokal ölçüm:
// min(time_bucket) taraması yük altında 5 s tavanını aşıp fail-safe now()
// döndürüyor, SLO (v0.10.518) ve metrik çözümleyici sessizce ham yola
// düşüyordu ([[feedback-correctness-held-by-a-setting]] sınıfı: zamanlama).

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCoverageFromPartsDate(t *testing.T) {
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	if _, ok := coverageFromPartsDate(time.Time{}, now); ok {
		t.Error("sentinel 1970 → false")
	}
	if d, ok := coverageFromPartsDate(time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), now); !ok || !d.Equal(time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ilk tam gün = minDate+1: %v %v", d, ok)
	}
	// Dün başlayan MV: bugün 00:00 kapsam başı (geçmişte) → ok.
	if d, ok := coverageFromPartsDate(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC), now); !ok || !d.Equal(time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("dün: %v %v", d, ok)
	}
	// Bugün başlayan MV: yarın gelmedi → false (tarama probu karar verir).
	if _, ok := coverageFromPartsDate(time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), now); ok {
		t.Error("bugün başlayan → false")
	}
	// Date kolonu yerel saatle gelirse de gün UTC'ye normalize edilir.
	loc := time.FixedZone("TR", 3*3600)
	if d, ok := coverageFromPartsDate(time.Date(2026, 8, 12, 0, 0, 0, 0, loc), now); !ok || d.Day() != 13 || d.Location() != time.UTC {
		t.Errorf("tz normalize: %v %v", d, ok)
	}
}

func TestSpanmetricsCoverageStartPrefersParts(t *testing.T) {
	b, err := os.ReadFile("metricresolve.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Store) spanmetricsCoverageStart(")
	j := strings.Index(src, "func (s *Store) spanmetricsCoverageFromParts(")
	if i < 0 || j < 0 {
		t.Fatal("fonksiyonlar yok")
	}
	body := src[i:j]
	if strings.Index(body, "s.spanmetricsCoverageFromParts(ctx, time.Now())") > strings.Index(body, "SELECT min(time_bucket)") {
		t.Error("part üst verisi taramadan ÖNCE denenmeli")
	}
	if !strings.Contains(body, "max_execution_time = 5") {
		t.Error("tarama probu süre sınırını kaybetmiş")
	}
	parts := src[j:]
	for _, want := range []string{"system.parts", "clusterAllReplicas('%s', system.parts)", "active = 1", "min(partition_id)", `s.mvStorageName("spanmetrics_1m"), "spanmetrics_1m_local"`, "s.mvInnerTablesCluster(ctx, name)", "table IN (?)", "max_execution_time = 5"} {
		if !strings.Contains(parts, want) {
			t.Errorf("parts probu: %q yok", want)
		}
	}
}
