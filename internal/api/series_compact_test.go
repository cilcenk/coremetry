package api

// series_compact_test.go — v0.10.186 sözleşmesi + v0.10.189 null dolgulu ızgara (series_compact.go başlığı).

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func pts(t0, step int64, vals ...float64) []chstore.SpanMetricPoint {
	out := make([]chstore.SpanMetricPoint, len(vals))
	for i, v := range vals {
		out[i] = chstore.SpanMetricPoint{Time: t0 + int64(i)*step, Value: v}
	}
	return out
}

func TestCompactSeriesShapes(t *testing.T) {
	reg := chstore.SpanMetricSeries{GroupKey: []string{"a"}, Points: pts(1_700_000_000e9, 15e9, 1, 2.5, 3)}
	one := chstore.SpanMetricSeries{GroupKey: []string{"b"}, Points: pts(1_700_000_000e9, 15e9, 0)}
	empty := chstore.SpanMetricSeries{GroupKey: []string{"c"}}
	gap := chstore.SpanMetricSeries{GroupKey: []string{"g"}, Points: []chstore.SpanMetricPoint{{Time: 0, Value: 1}, {Time: 15e9, Value: 2}, {Time: 45e9, Value: 3}}}
	uniformGap := chstore.SpanMetricSeries{GroupKey: []string{"u"}, Points: pts(0, 300e9, 1, 2, 3)} // cron: tekdüze boşluk = düzenli
	cs := compactSeriesSet([]chstore.SpanMetricSeries{reg, one, empty, gap, uniformGap})
	if cs[0].T0 != 1_700_000_000e9 || cs[0].Step != 15e9 || cs[0].T != nil || cs[0].V[1] != 2.5 {
		t.Fatalf("düzenli: %+v", cs[0])
	}
	if cs[1].Step != 0 || len(cs[1].V) != 1 || cs[1].T != nil {
		t.Fatalf("tek nokta: %+v", cs[1])
	}
	if cs[2].Step != 0 || len(cs[2].V) != 0 || cs[2].T0 != 0 {
		t.Fatalf("boş: %+v", cs[2])
	}
	// v0.10.189 — ızgaralı boşluk (kapsama 3/4): null dolgulu adımlı şekil
	if cs[3].T != nil || cs[3].Step != 15e9 || len(cs[3].V) != 4 || !reflect.DeepEqual(cs[3].Gap, []bool{false, false, true, false}) || cs[3].V[3] != 3 {
		t.Fatalf("ızgaralı seyrek seri null dolgulu adımlı olmalı: %+v", cs[3])
	}
	if j, _ := json.Marshal(cs[3]); string(j) != `{"groupKey":["g"],"step":15000000000,"v":[1,2,null,3]}` {
		t.Fatalf("tel şekli: %s", j)
	}
	// kapsama < %20 → açık t[]; adım saniye-altı OBEB → açık t[]
	sparse := chstore.SpanMetricSeries{GroupKey: []string{"s"}, Points: []chstore.SpanMetricPoint{{Time: 0, Value: 1}, {Time: 15e9, Value: 2}, {Time: 15e9 * 200, Value: 3}}}
	if c := compactOne(sparse); c.T == nil || c.Gap != nil || !reflect.DeepEqual(c.T, []int64{0, 15e9, 15e9 * 200}) {
		t.Fatalf("seyrek (%%1,5) seri açık t[] kalmalı: %+v", c)
	}
	subSec := chstore.SpanMetricSeries{GroupKey: []string{"m"}, Points: []chstore.SpanMetricPoint{{Time: 0, Value: 1}, {Time: 500e6, Value: 2}, {Time: 1500e6, Value: 3}}}
	if c := compactOne(subSec); c.T == nil || c.Gap != nil {
		t.Fatalf("saniye-altı OBEB açık t[] kalmalı: %+v", c)
	}
	if g, ok := gridStep([]chstore.SpanMetricPoint{{Time: 0}, {Time: 30e9}, {Time: 75e9}}); !ok || g != 15e9 {
		t.Fatalf("OBEB 15e9 bekleniyor: %d %v", g, ok)
	}
	// NaN/Inf tel üstünde null (emniyet; prod yolunda cache.go sanitizeFloats 0'a çeker)
	if j, err := json.Marshal(compactSeries{GroupKey: []string{"n"}, V: []float64{1, math.NaN(), math.Inf(1)}}); err != nil || string(j) != `{"groupKey":["n"],"v":[1,null,null]}` {
		t.Fatalf("NaN/Inf: %s %v", j, err)
	}
	// float biçimi encoding/json ile birebir (inceleme #1: 'g' 1e6 üstünü şişiriyordu)
	for _, v := range []float64{536870912, 1234567, 0.5, 1e-7, 1e21, 123456789012345678, 0, -2.5, 1e-5} {
		want, _ := json.Marshal(v)
		if got := appendJSONFloat(nil, v); string(got) != string(want) {
			t.Fatalf("float %v: %s ≠ encoding/json %s", v, got, want)
		}
	}
	if cs[4].Step != 300e9 || cs[4].T != nil {
		t.Fatalf("tekdüze boşluk düzenli sayılmalı: %+v", cs[4])
	}
	if _, ok := regularStep([]chstore.SpanMetricPoint{{Time: 5}, {Time: 5}}); ok {
		t.Fatal("sıfır adım düzenli sayıldı")
	}
	// yuvarlak-yolculuk: FE formülünün Go aynası girdiyi birebir geri verir
	for _, s := range []chstore.SpanMetricSeries{reg, one, gap, uniformGap, sparse, subSec} {
		if got := expandCompact(compactOne(s)); !reflect.DeepEqual(got.Points, s.Points) {
			t.Fatalf("round-trip bozuk: %+v → %+v", s.Points, got.Points)
		}
	}
	if got := expandCompact(compactOne(empty)); len(got.Points) != 0 {
		t.Fatalf("boş seri round-trip: %+v", got)
	}
}

func TestCompactSeriesBytesSmaller(t *testing.T) {
	vals := make([]float64, 240)
	for i := range vals {
		vals[i] = float64(i) * 1.234567
	}
	s := []chstore.SpanMetricSeries{{GroupKey: []string{"payments-orchestrator"}, Points: pts(1_700_000_000e9, 15e9, vals...)}}
	rowsJSON, _ := json.Marshal(s)
	colsJSON, _ := json.Marshal(compactSeriesSet(s))
	if len(colsJSON)*3 > len(rowsJSON) {
		t.Fatalf("sütunsal kodlama yeterince küçülmedi: rows=%d cols=%d", len(rowsJSON), len(colsJSON))
	}
	// kodlanmış slot: series:null + cols (FE null≠undefined kuralı: cols çözülür)
	slot := bundleSlot{Series: nil, Enc: seriesEncColumnar, Cols: compactSeriesSet(s)}
	b, _ := json.Marshal(slot)
	if !strings.Contains(string(b), `"series":null`) || !strings.Contains(string(b), `"cols":[`) || !strings.Contains(string(b), `"enc":"`+seriesEncColumnar+`"`) {
		t.Fatalf("kodlanmış slot şekli: %s", b[:120])
	}
}

// Kaynak kapısı: kodlama yalnız istemci opt-in'iyle (enc:"col") ve anahtar v3 (#2/#3).
func TestBundleEncodingGated(t *testing.T) {
	src := readSrc(t, "dashboards_data.go")
	for _, want := range []string{`dash-panel:v3`, `req.Enc == seriesEncColumnar`, `compactSeriesSet(slot.Series)`} {
		if !strings.Contains(src, want) {
			t.Fatalf("dashboards_data.go %q içermiyor", want)
		}
	}
}
