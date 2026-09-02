package api

// sparkline_slots.go — v0.10.262 (perf profili §7 madde 6, CDV-1).
//
// /api/services/sparklines ham 5-dk MV satırlarını olduğu gibi basıyordu:
// 7 g × 50 servis = 2016 × 50 ≈ 100k JSON nesnesi, Services açılışının en
// büyük gövdesi. Çizim tarafı (Sparkline.tsx) zaten genişlik bütçesine
// indirgiyor (~40-80 çubuk); telin taşıdığı fazlalık saf israftı.
//
// Slot grid: pencere en çok maxSlots dilime bölünür (dilim 5 dk'nın katı,
// başlangıç 5 dk'ya hizalı); dilim içinde spans/errs TOPLANIR, avgMs span
// ağırlıklı ortalama, p99Ms MAX (eşik aşan 5 dk kırmızı kalır — Sparkline
// 'max' birleştirmesiyle aynı sözleşme). Tüm servisler AYNI grid'i
// paylaşır (Services.tsx sayfa serisini `t` ile birleştiriyor). Pencere
// ≤ maxSlots × 5 dk ise çıktı ham satırlarla birebir (dilim = 5 dk).
// SAF; sparkline_slots_test.go pinler.

import (
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	sparklineMaxSlots = 120
	sparklineBucket   = 5 * time.Minute
)

// sparklineSlotRungs — v0.10.286 (chart audit D1 / Dilim 1.7): istemcinin
// piksel bütçesinden gelen `maxSlots` isteği bu basamaklara SNAP edilir.
// Basamak = sınırlı kardinalite (v0.5.187 cache-key kuralı: serbest bir
// tamsayı her genişlikte ayrı cache satırı üretirdi). 120 = eski sabit
// (bütçesiz istemci aynı gövdeyi görür); 40 = ~80 px'lik sütun (2 px/slot).
var sparklineSlotRungs = []int{40, 60, 80, sparklineMaxSlots}

// sparklineSlotRung — istenen slot sayısını basamağa yuvarlar (yukarı:
// istemci en az istediği kadar nokta alır); 0/negatif/aşırı → 120.
func sparklineSlotRung(want int) int {
	if want <= 0 {
		return sparklineMaxSlots
	}
	for _, r := range sparklineSlotRungs {
		if want <= r {
			return r
		}
	}
	return sparklineMaxSlots
}

type sparkPoint struct {
	T     int64   `json:"t"`
	Spans uint64  `json:"spans"`
	Errs  uint64  `json:"errs"`
	AvgMs float64 `json:"avgMs"`
	P99Ms float64 `json:"p99Ms"`
}

// sparklineSlotWidth — pencereyi maxSlots'a sığdıran, 5 dk'nın katı dilim.
func sparklineSlotWidth(from, to time.Time, maxSlots int) time.Duration {
	win := to.Sub(from)
	if maxSlots <= 0 || win <= 0 {
		return sparklineBucket
	}
	w := win / time.Duration(maxSlots)
	if w <= sparklineBucket {
		return sparklineBucket
	}
	if r := w % sparklineBucket; r != 0 {
		w += sparklineBucket - r
	}
	return w
}

// sparklineSlots — 5-dk satırlar → servis başına slot serisi (t artan).
func sparklineSlots(rows []chstore.ServiceSummaryRow, from, to time.Time, maxSlots int) map[string][]sparkPoint {
	width := sparklineSlotWidth(from, to, maxSlots)
	origin := from.Truncate(sparklineBucket).UnixNano()
	wNs := width.Nanoseconds()
	type acc struct {
		spans, errs uint64
		avgW        float64
		p99         float64
	}
	perSvc := map[string]map[int64]*acc{}
	for _, r := range rows {
		slot := origin
		if r.BucketStart > origin {
			slot = origin + ((r.BucketStart-origin)/wNs)*wNs
		}
		m := perSvc[r.Service]
		if m == nil {
			m = map[int64]*acc{}
			perSvc[r.Service] = m
		}
		a := m[slot]
		if a == nil {
			a = &acc{}
			m[slot] = a
		}
		a.spans += r.SpanCount
		a.errs += r.ErrorCount
		a.avgW += r.AvgMs * float64(r.SpanCount)
		if r.P99Ms > a.p99 {
			a.p99 = r.P99Ms
		}
	}
	out := make(map[string][]sparkPoint, len(perSvc))
	for svc, m := range perSvc {
		pts := make([]sparkPoint, 0, len(m))
		for t, a := range m {
			p := sparkPoint{T: t, Spans: a.spans, Errs: a.errs, P99Ms: a.p99}
			if a.spans > 0 {
				p.AvgMs = a.avgW / float64(a.spans)
			}
			pts = append(pts, p)
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].T < pts[j].T })
		out[svc] = pts
	}
	return out
}
