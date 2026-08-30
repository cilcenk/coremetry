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
// v0.10.189 — ÜÇÜNCÜ şekil, seyrek-ama-ızgaralı seri: {groupKey,t0,step,v[]}
// ve v[] içinde BOŞLUK = null (≈5 B/delik). Ölçüm (preset-dependencies 1h,
// 12 panel/122 seri): 86 seri seyrekti (kapsama 0,43–1,0) ve açık t[]
// yolunda 527 KB tutuyordu; null dolgusu ~230 KB → gövde 725 → ~480 KB.
// Izgara = ardışık farkların OBEB'i (bucket'lar toStartOfInterval çıktısı,
// hepsi adımın katı); kapsama < %20 ise (delik başına 5 B > nokta başına
// ~20 B'lik açık-zaman farkı) yine açık t[]. Adım tam saniye değilse
// (OBEB 1e9'a bölünmüyorsa) açık t[] — FE double aritmetiği varsayımı.
// Sözleşme: series_compact_test.go (Go) + lib/seriesCompact.test.ts (FE).

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// seriesEncColumnar — v0.10.189 "col2": tel şekli değişti (v[] içinde null
// delik). Eski FE (186-188, açık sekme) "col" ister → sunucu düz şekli
// döner; yeni FE eski pod'a "col2" gönderir → düz şekil; eski FE yeni
// pod'dan "col2" görürse decodeBundleSlot series'i siler → panel kendi
// fetch'ine düşer (belgeli kaçış). enc srcTag'de → cache anahtarı da ayrılır.
const seriesEncColumnar = "col2"

type compactSeries struct {
	GroupKey []string  `json:"groupKey"`
	T0       int64     `json:"t0,omitempty"`   // unix ns, ilk nokta (düzenli / ızgaralı)
	Step     int64     `json:"step,omitempty"` // ns (düzenli / ızgaralı; 0 = ≤1 nokta)
	T        []int64   `json:"t,omitempty"`    // düzensiz: açık zaman dizisi
	V        []float64 `json:"v"`              // ızgaralıda len = ızgara boyu; delikler Gap ile null yazılır
	Gap      []bool    `json:"-"`              // v0.10.189: nil → delik yok; Gap[i] → v[i] tel üstünde null
}

// gapCoverageMin — null dolgusunun açık t[]'den ucuz olduğu alt sınır:
// delik ≈5 B ("null,"), açık zaman ≈20 B/nokta fazlası → n·20 ≥ (span−n)·5
// ⇔ kapsama ≥ 0,2.
const gapCoverageMin = 0.2

// MarshalJSON — v[] deliklerde null yazar. Alan sırası/omitempty'si struct
// etiketiyle aynı; float biçimi encoding/json ile birebir (inceleme #1:
// 'g' 1e6 üstünü bilimsel yazıp gövdeyi ŞİŞİRİYORDU — jvm.memory.used
// 536870912 → 5.36870912e+08, +5 B/nokta). NaN/±Inf → null yalnız emniyet:
// cache katmanı (cache.go sanitizeFloats) zaten 0'a çeker, prod yolunda
// buraya sonlu değer gelir.
func (c compactSeries) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"groupKey":`)
	gk, err := json.Marshal(c.GroupKey)
	if err != nil {
		return nil, err
	}
	b.Write(gk)
	if c.T0 != 0 {
		b.WriteString(`,"t0":`)
		b.WriteString(strconv.FormatInt(c.T0, 10))
	}
	if c.Step != 0 {
		b.WriteString(`,"step":`)
		b.WriteString(strconv.FormatInt(c.Step, 10))
	}
	if len(c.T) > 0 {
		b.WriteString(`,"t":[`)
		for i, t := range c.T {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.FormatInt(t, 10))
		}
		b.WriteByte(']')
	}
	b.WriteString(`,"v":[`)
	for i, v := range c.V {
		if i > 0 {
			b.WriteByte(',')
		}
		if (c.Gap != nil && c.Gap[i]) || math.IsNaN(v) || math.IsInf(v, 0) {
			b.WriteString("null")
			continue
		}
		b.Write(appendJSONFloat(b.AvailableBuffer(), v))
	}
	b.WriteString("]}")
	return b.Bytes(), nil
}

// appendJSONFloat — encoding/json floatEncoder'ın aynısı: 'f', yalnız
// |v| < 1e-6 ya da ≥ 1e21 ise 'e' ve "e-09" → "e-9" temizliği.
func appendJSONFloat(dst []byte, v float64) []byte {
	f := byte('f')
	if abs := math.Abs(v); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		f = 'e'
	}
	dst = strconv.AppendFloat(dst, v, f, -1, 64)
	if f == 'e' {
		if n := len(dst); n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst
}

// gridStep — ardışık farkların OBEB'i (hepsi > 0 olmalı; <2 nokta → 0,false).
func gridStep(pts []chstore.SpanMetricPoint) (int64, bool) {
	if len(pts) < 2 {
		return 0, false
	}
	var g int64
	for i := 1; i < len(pts); i++ {
		d := pts[i].Time - pts[i-1].Time
		if d <= 0 {
			return 0, false
		}
		for d != 0 {
			g, d = d, g%d
		}
	}
	return g, g > 0
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
	c := compactSeries{GroupKey: s.GroupKey}
	dense := func() {
		c.V = make([]float64, len(s.Points))
		for i, p := range s.Points {
			c.V[i] = p.Value
		}
	}
	if step, ok := regularStep(s.Points); ok {
		if len(s.Points) > 0 {
			c.T0 = s.Points[0].Time
		}
		c.Step = step
		dense()
		return c
	}
	// v0.10.189 — ızgaralı seyrek seri: OBEB adımı tam saniye ve kapsama ≥ %20
	// ise null dolgulu adımlı şekil.
	if g, ok := gridStep(s.Points); ok && g%1e9 == 0 {
		first, last := s.Points[0].Time, s.Points[len(s.Points)-1].Time
		span := (last-first)/g + 1
		if float64(len(s.Points)) >= gapCoverageMin*float64(span) {
			c.T0, c.Step = first, g
			c.V = make([]float64, span)
			c.Gap = make([]bool, span)
			for i := range c.Gap {
				c.Gap[i] = true
			}
			for _, p := range s.Points {
				k := (p.Time - first) / g
				c.V[k], c.Gap[k] = p.Value, false
			}
			return c
		}
	}
	dense()
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
	out := chstore.SpanMetricSeries{GroupKey: c.GroupKey, Points: make([]chstore.SpanMetricPoint, 0, len(c.V))}
	for i, v := range c.V {
		if c.Gap != nil && c.Gap[i] { // null → nokta yok (FE: null atlanır)
			continue
		}
		t := c.T0 + int64(i)*c.Step
		if len(c.T) > 0 { // MarshalJSON ile aynı muhafız (inceleme #5)
			t = c.T[i]
		}
		out.Points = append(out.Points, chstore.SpanMetricPoint{Time: t, Value: v})
	}
	return out
}
