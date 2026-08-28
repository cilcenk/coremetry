package chstore

import (
	"context"
	"log"
	"sync"
	"time"
)

// trace_mv_coverage.go — MV BOŞLUĞU FARKINDA OKUMA (v0.10.124).
//
// Operator-reported (prod, 2026-08-28): "servis seçip 2 gün öncesini
// getirmeye çalışınca liste boş; servis + operation seçince geliyor".
// Servis-tek-başına MV yolundan (trace_service_index_5m →
// trace_summary_5m) okur; v0.10.97 MV upgrade'inin düşürdüğü ve
// sihirbazın henüz doldurmadığı günde MV boştur → liste SESSİZCE boş.
// Operation seçimi `search=` ile MV'yi diskalifiye edip ham `spans`'e
// düştüğü için "çalışıyor" görünüyordu.
//
// Karar: pencere MV'de boşluk olan bir güne değiyorsa MV yolu
// atlanır, ham yol (25 s tavanlı, bugün de var) kullanılır ve yanıt
// `mvGap` ile bunu söyler. Boşluk hükmü sihirbazın preflight'ından
// (`system.parts`, gün başına ham/MV satır oranı — veri TARANMAZ,
// anlık) gelir; 60 sn önbellekle. Boşluk yoksa davranış bire bir
// eskisi. Sihirbaz günü doldurunca 60 sn içinde MV yoluna dönülür.

// traceMVCoverageTTL — gün haritasının tazelik süresi. Preflight
// system.parts'tan okur (ms); 60 sn, aynı anda gelen yüzlerce liste
// isteğinin tek sorguyu paylaşması için yeter.
const traceMVCoverageTTL = 60 * time.Second

type traceMVCoverage struct {
	mu      sync.Mutex
	gaps    map[string]bool // "2026-08-26" → true (boşluk)
	fetched time.Time
	now     func() time.Time // test seam
}

// traceMVGapDays — harita; TTL dolmuşsa yeniler. Yenileme HATASINDA eski
// harita kalır (yoksa boş harita = MV yolu, bugünkü davranış) ve log
// bir kez söyler — cluster erişimi kesildi diye her liste isteği ham
// yola düşmemeli.
func (s *Store) traceMVGapDays(ctx context.Context) map[string]bool {
	c := &s.mvCoverage
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	if c.gaps != nil && now().Sub(c.fetched) < traceMVCoverageTTL {
		return c.gaps
	}
	fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	days, err := s.TraceBackfillPreflight(fctx, 31)
	if err != nil {
		log.Printf("[traces] MV kapsam haritası yenilenemedi (%v) — eski harita/MV yolu", err)
		if c.gaps == nil {
			c.gaps = map[string]bool{}
		}
		c.fetched = now()
		return c.gaps
	}
	gaps := map[string]bool{}
	for _, d := range days {
		if d.Gap {
			gaps[d.Day] = true
		}
	}
	c.gaps, c.fetched = gaps, now()
	return gaps
}

// traceWindowTouchesGap — [from, to] penceresinin UTC günlerinden biri
// boşluk mu? Saf; tablo-testli. Boş harita → false.
func traceWindowTouchesGap(from, to time.Time, gaps map[string]bool) bool {
	if len(gaps) == 0 || from.IsZero() || to.IsZero() || !from.Before(to) {
		return false
	}
	day := from.UTC().Truncate(24 * time.Hour)
	end := to.UTC()
	for !day.After(end) {
		if gaps[day.Format("2006-01-02")] {
			return true
		}
		day = day.AddDate(0, 0, 1)
	}
	return false
}

// TraceMVGap — pencere MV boşluğuna değiyor mu (api yanıtı `mvGap` için;
// Store içi karar da aynı haritayı okur).
func (s *Store) TraceMVGap(ctx context.Context, from, to time.Time) bool {
	if s == nil {
		return false
	}
	return traceWindowTouchesGap(from, to, s.traceMVGapDays(ctx))
}
