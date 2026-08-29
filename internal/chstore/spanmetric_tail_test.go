package chstore

// v0.10.147 — kuyruk ön-toplamları (kesin "others" katlaması).
//
// Sözleşme: tailPreagg, top-N'in DIŞINDA kalan serileri zaman-başı ham
// TOPLAM + SAYI'ya indirger (birim bilmez — birim kararı FE'de tek yerde,
// foldTopN). FE, kendi kuyruğunun toplamına bu sayıları ekleyince sonuç
// TAM seriyle katlamaya bayt-bayt eşit olmalı; bu eşdeğerlik burada saf
// bir referans katlamayla pinlenir.
//   • NaN/Inf nokta 0 sayılır ve sayaca girer — teldeki değerle aynı
//     (sanitizeFloats 0 yazar, FE foldTopN 0'ı değer sayar).
//   • Çıktı zamana göre sıralı; boş kuyruk → nil (omitempty).
//   • Sayı, o bucket'ta DEĞERİ OLAN kuyruk serilerinin sayısıdır.

import (
	"math"
	"reflect"
	"testing"
)

func tailSeries(key string, pts ...float64) SpanMetricSeries {
	s := SpanMetricSeries{GroupKey: []string{key}}
	for i, v := range pts {
		s.Points = append(s.Points, SpanMetricPoint{Time: int64(i) * 1e9, Value: v})
	}
	return s
}

func TestTailPreagg_SumAndCountPerTime(t *testing.T) {
	a := tailSeries("a", 1, 2, 3)
	b := SpanMetricSeries{GroupKey: []string{"b"}, Points: []SpanMetricPoint{{Time: 1e9, Value: 10}, {Time: 3e9, Value: 30}}}
	got := tailPreagg([]SpanMetricSeries{a, b})
	want := []TailPoint{
		{Time: 0, Sum: 1, Count: 1},
		{Time: 1e9, Sum: 12, Count: 2},
		{Time: 2e9, Sum: 3, Count: 1},
		{Time: 3e9, Sum: 30, Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestTailPreagg_EmptyAndNaN(t *testing.T) {
	if got := tailPreagg(nil); got != nil {
		t.Fatalf("empty tail must be nil (omitempty), got %+v", got)
	}
	s := SpanMetricSeries{GroupKey: []string{"n"}, Points: []SpanMetricPoint{
		{Time: 0, Value: math.NaN()}, {Time: 1e9, Value: math.Inf(1)}, {Time: 2e9, Value: 5},
	}}
	got := tailPreagg([]SpanMetricSeries{s})
	want := []TailPoint{{Time: 0, Sum: 0, Count: 1}, {Time: 1e9, Sum: 0, Count: 1}, {Time: 2e9, Sum: 5, Count: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NaN/Inf must count as 0 (same as the wire): got %+v want %+v", got, want)
	}
}

// refFold — FE foldTopN'in aritmetiği (saf referans): üst n alan-bazlı,
// kalan zaman-başı toplam (sum) ya da ortalama (mean).
func refFold(all []SpanMetricSeries, n int, mean bool) map[int64]float64 {
	kept, _ := trimTopNByArea(all, n)
	dropped := dropTopN(all, kept)
	sum := map[int64]float64{}
	cnt := map[int64]int{}
	for _, s := range dropped {
		for _, p := range s.Points {
			v := p.Value
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0 // tel kuralı (sanitizeFloats → 0, FE sayar)
			}
			sum[p.Time] += v
			cnt[p.Time]++
		}
	}
	out := map[int64]float64{}
	for t, v := range sum {
		if mean {
			out[t] = v / float64(cnt[t])
		} else {
			out[t] = v
		}
	}
	return out
}

// Eşdeğerlik: (top-N + tail) ile FE katlaması == tam seriyle FE katlaması,
// hem toplam hem ortalama birimlerinde. Sunucu N=serverN keser (bundle
// tavanı), FE n=8'e katlar: FE'nin gördüğü kuyruk (serverN-8 seri) + tail.
func TestTailPreagg_FoldEquivalence(t *testing.T) {
	var all []SpanMetricSeries
	for i := 0; i < 40; i++ {
		s := SpanMetricSeries{GroupKey: []string{string(rune('a' + i%26)), string(rune('0' + i/26))}}
		for k := 0; k < 12; k++ {
			if (i*7+k)%9 == 0 {
				continue // delik
			}
			s.Points = append(s.Points, SpanMetricPoint{Time: int64(k) * 15e9, Value: float64((i*13+k*5)%97) + 0.25})
		}
		if i == 37 { // düşük alanlı bir seride sonlu olmayan nokta: tel kuralı iki yarıda da aynı olmalı
			s.Points[0].Value = math.NaN()
		}
		all = append(all, s)
	}
	const feN = 8
	serverN := DashboardTopN
	kept, tail, total, _ := topNWithTail(all, serverN)
	if total != len(all) || len(kept) != serverN || len(tail) == 0 {
		t.Fatalf("topNWithTail: kept=%d tail=%d total=%d", len(kept), len(tail), total)
	}
	for _, mean := range []bool{false, true} {
		want := refFold(all, feN, mean)
		// FE tarafı: kept içinden top-feN, geri kalan kept + tail birleşir.
		feKept, _ := trimTopNByArea(kept, feN)
		feDropped := dropTopN(kept, feKept)
		sum := map[int64]float64{}
		cnt := map[int64]int{}
		for _, s := range feDropped {
			for _, p := range s.Points {
				v := p.Value
				if math.IsNaN(v) || math.IsInf(v, 0) {
					v = 0
				}
				sum[p.Time] += v
				cnt[p.Time]++
			}
		}
		for _, tp := range tail {
			sum[tp.Time] += tp.Sum
			cnt[tp.Time] += tp.Count
		}
		got := map[int64]float64{}
		for ts, v := range sum {
			if mean {
				got[ts] = v / float64(cnt[ts])
			} else {
				got[ts] = v
			}
		}
		if len(got) != len(want) {
			t.Fatalf("mean=%v: %d buckets, want %d", mean, len(got), len(want))
		}
		for ts, w := range want {
			if math.Abs(got[ts]-w) > 1e-9 {
				t.Fatalf("mean=%v t=%d: got %v want %v", mean, ts, got[ts], w)
			}
		}
	}
}
