package api

// series_compact.go — v0.10.186 (perf bütçesi P5, «nokta kodlaması»):
// dashboard bundle'ındaki seriler satır-nesne ({time,value} ≈ 45-50 B/nokta)
// yerine SÜTUNSAL kodlanır. Seri başına iki şekil:
//   - düzenli ızgara (tüm ardışık farklar eşit; tek bakış: cron gibi
//     TEKDÜZE boşluk da düzenli sayılır, kayıpsız): {groupKey,t0,step,v[]}
//     ≈ 8-10 B/nokta;
//   - düzensiz (boşluklu): {groupKey,t[],v[]} — açık zaman dizisi, ≈ 28 B/nokta
//     (yine satır-nesneden küçük). Bundle yolları bucket doldurmuyor
//     (QuerySpanMetric / metricquery GROUP BY, WITH FILL yok) → gruplu
//     panellerde seyrek seri OLAĞAN; slot-geneli vazgeçmek kazancı sıfırlardı
//     (inceleme #4).
// İstemci OPT-IN: gövdede enc:"col" (yeni FE gönderir); eski sekme düz
// şekli alır (inceleme #3). Önbellek anahtarı enc'i taşır + v3 (#2).
// Zaman: step tam saniye (StepSeconds int) → step_ns 1e9'un katı, 1e9 ⊃ 512
// ≥ ulp(1.7e18) → FE'de t0+i*step double aritmetiği TAM (inceleme ölçtü,
// 0 sapma); saniye-altı adım gelirse bu varsayım bozulur — pinli test.
// Sözleşme: series_compact_test.go (Go) + lib/seriesCompact.test.ts (FE).

import "github.com/cilcenk/coremetry/internal/chstore"

const seriesEncColumnar = "col"

type compactSeries struct {
	GroupKey []string  `json:"groupKey"`
	T0       int64     `json:"t0,omitempty"`   // unix ns, ilk nokta (düzenli)
	Step     int64     `json:"step,omitempty"` // ns (düzenli; 0 = ≤1 nokta)
	T        []int64   `json:"t,omitempty"`    // düzensiz: açık zaman dizisi
	V        []float64 `json:"v"`
}

// regularStep — ardışık farklar eşitse (0 nokta → 0,true; 1 nokta → 0,true).
func regularStep(pts []chstore.SpanMetricPoint) (int64, bool) {
	if len(pts) < 2 {
		return 0, true
	}
	step := pts[1].Time - pts[0].Time
	if step <= 0 {
		return 0, false
	}
	for i := 2; i < len(pts); i++ {
		if pts[i].Time-pts[i-1].Time != step {
			return 0, false
		}
	}
	return step, true
}

// compactOne — tek serinin sütunsal kopyası (düzenli ya da açık zamanlı).
func compactOne(s chstore.SpanMetricSeries) compactSeries {
	c := compactSeries{GroupKey: s.GroupKey, V: make([]float64, len(s.Points))}
	for i, p := range s.Points {
		c.V[i] = p.Value
	}
	if step, ok := regularStep(s.Points); ok {
		if len(s.Points) > 0 {
			c.T0 = s.Points[0].Time
		}
		c.Step = step
		return c
	}
	c.T = make([]int64, len(s.Points))
	for i, p := range s.Points {
		c.T[i] = p.Time
	}
	return c
}

// compactSeriesSet — her seri kodlanır (boş küme → boş dilim).
func compactSeriesSet(series []chstore.SpanMetricSeries) []compactSeries {
	out := make([]compactSeries, 0, len(series))
	for _, s := range series {
		out = append(out, compactOne(s))
	}
	return out
}

// expandCompact — FE çözücüsünün (lib/seriesCompact.ts) birebir Go aynası;
// yalnız yuvarlak-yolculuk testi için (t0 + i*step / t[]).
func expandCompact(c compactSeries) chstore.SpanMetricSeries {
	out := chstore.SpanMetricSeries{GroupKey: c.GroupKey, Points: make([]chstore.SpanMetricPoint, len(c.V))}
	for i, v := range c.V {
		t := c.T0 + int64(i)*c.Step
		if c.T != nil {
			t = c.T[i]
		}
		out.Points[i] = chstore.SpanMetricPoint{Time: t, Value: v}
	}
	return out
}
