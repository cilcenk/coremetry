package chstore

// spanmetric_tail.go — v0.10.147 kuyruk ön-toplamları.
//
// Top-N kırpması (v0.8.x) gövdeyi küçültür ama kuyruğu ATAR; dashboard'ın
// "others" katlaması (FE foldTopN, v0.9.946) kuyruğu tam seriden toplar.
// Kırpılmış girdiyle "others" çizgisi kuyruk kütlesini sessizce kaybediyordu
// (v0.10.146 incelemesi; tekil uç + fallback yolunda zaten böyleydi). Çözüm:
// sunucu top-N'in YANINDA kuyruğun zaman-başı ham TOPLAM + SAYI'sını döner.
// Birim BİLMEZ — toplanabilir (rps/count) ya da ortalanır (%/ms/s) kararı
// FE'de tek yerde kalır (foldTopN); sunucu yalnız ham sayıları taşır, yani
// v0.9.946'nın "ikinci kopya birim kuralını bir gün kaybeder" kaygısı
// burada doğmaz. Eşdeğerlik (top-N + tail katlaması == tam seri katlaması)
// spanmetric_tail_test.go'da pinli.

import (
	"context"
	"math"
	"sort"
	"strings"
)

// TailPoint — kuyruk serilerinin bir bucket'taki ham toplamı ve o bucket'ta
// değeri OLAN kuyruk serisi sayısı. FE: sum birimde Σ, mean birimde
// (Σ + kendi kuyruğu) / (Count + kendi sayısı).
type TailPoint struct {
	Time  int64   `json:"time"` // unix nanos (bucket start) — SpanMetricPoint ile aynı eksen
	Sum   float64 `json:"sum"`
	Count int     `json:"count"`
}

// tailPreagg — SAF: atılan serileri zaman-başı sum/count'a indirger.
// Boş kuyruk → nil.
//
// Sonlu olmayan nokta (NaN/Inf) 0 SAYILIR ve sayaca girer — bilinçli:
// tutulan serilerin aynı noktası tele sanitizeFloats (v0.5.303) ile 0
// olarak çıkar ve FE foldTopN onu bir değer sayar (`value == null` değil).
// Kuyruk başka bir kural uygulasaydı (atlamak) aynı grafiğin iki yarısı
// farklı bölenle ortalanırdı (v0.10.147 incelemesi). Eşdeğerlik testi bu
// kuralı referans katlamaya da uygular.
func tailPreagg(dropped []SpanMetricSeries) []TailPoint {
	if len(dropped) == 0 {
		return nil
	}
	sum := map[int64]float64{}
	cnt := map[int64]int{}
	for _, s := range dropped {
		for _, p := range s.Points {
			v := p.Value
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0 // teldeki değerle aynı (sanitizeFloats)
			}
			sum[p.Time] += v
			cnt[p.Time]++
		}
	}
	if len(sum) == 0 {
		return nil
	}
	out := make([]TailPoint, 0, len(sum))
	for t, v := range sum {
		out = append(out, TailPoint{Time: t, Sum: v, Count: cnt[t]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

func seriesKey(s SpanMetricSeries) string { return strings.Join(s.GroupKey, "\x1f") }

// dropTopN — all içinden kept'te OLMAYAN serileri döner (groupKey kimliği;
// bir sorguda groupKey tekildir). trimTopNByArea kopya döndürdüğü için
// işaretçi kıyası olmaz.
func dropTopN(all, kept []SpanMetricSeries) []SpanMetricSeries {
	if len(kept) >= len(all) {
		return nil
	}
	keep := make(map[string]struct{}, len(kept))
	for _, s := range kept {
		keep[seriesKey(s)] = struct{}{}
	}
	var dropped []SpanMetricSeries
	for _, s := range all {
		if _, ok := keep[seriesKey(s)]; !ok {
			dropped = append(dropped, s)
		}
	}
	return dropped
}

// topNWithTail — trimTopNByArea + kuyruk ön-toplamı. total = kırpma öncesi
// seri sayısı; tail yalnız kırpma olduysa (nil değilse) dolar.
func topNWithTail(all []SpanMetricSeries, n int) (kept []SpanMetricSeries, tail []TailPoint, total int, trimmed bool) {
	kept, total = trimTopNByArea(all, n)
	if total <= n {
		return kept, nil, total, false
	}
	return kept, tailPreagg(dropTopN(all, kept)), total, true
}

// DashboardTopN — dashboard bundle'ının seri tavanı (api/dashboards_data.go
// bunu geçer). Tekil uç Explore için spanMetricTopN (50) tutar; dashboard
// paneli 8 + "others" çizer (FE foldTopN DEFAULT_MAX_SERIES), kuyruğun
// kütlesi tail ile taşınır. Neden 8 değil 16: FE üst-8'i alan bazlı KENDİ
// sıralar; eşit alanlı seriler ve tel yuvarlaması sunucuyla FE'nin sınırı
// farklı çekmesine yol açabilir — 2× pay, çizilen 8'in gerçek üst-8 olmasını
// garanti eder, gövde payı ~%10. Eşdeğerlik testi bu sabitten türer.
const DashboardTopN = 16

// QuerySpanMetricTopNTail — QuerySpanMetricTopN'in n-parametreli, kuyruk
// ön-toplamlı hali. Tekil /api/spans/metric spanMetricTopN (50, Explore
// TOP_N_MAX ile aynı) ister; dashboard bundle'ı DashboardTopN ile çağırır.
func (s *Store) QuerySpanMetricTopNTail(ctx context.Context, f SpanMetricFilter, n int) (series []SpanMetricSeries, tail []TailPoint, total int, capped bool, err error) {
	all, err := s.QuerySpanMetric(ctx, f)
	if err != nil {
		return nil, nil, 0, false, err
	}
	// v0.9.458 (dürüstlük A1) — satır tavanı TRIM'den ÖNCE ölçülür:
	// totalSeries top-N kırpmasını anlatır, capped ise LIMIT'in alfabetik
	// kestiğini — ikisi ayrı yalanlardır.
	capped = SeriesRowsCapped(all)
	kept, tail, total, _ := topNWithTail(all, n)
	return kept, tail, total, capped, nil
}
