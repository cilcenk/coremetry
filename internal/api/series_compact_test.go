package api

// series_compact_test.go — v0.10.186 sözleşmesi (series_compact.go başlığı).

import (
	"encoding/json"
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
	if cs[3].T == nil || cs[3].Step != 0 || !reflect.DeepEqual(cs[3].T, []int64{0, 15e9, 45e9}) {
		t.Fatalf("düzensiz açık zaman dizisi bekleniyor: %+v", cs[3])
	}
	if cs[4].Step != 300e9 || cs[4].T != nil {
		t.Fatalf("tekdüze boşluk düzenli sayılmalı: %+v", cs[4])
	}
	if _, ok := regularStep([]chstore.SpanMetricPoint{{Time: 5}, {Time: 5}}); ok {
		t.Fatal("sıfır adım düzenli sayıldı")
	}
	// yuvarlak-yolculuk: FE formülünün Go aynası girdiyi birebir geri verir
	for _, s := range []chstore.SpanMetricSeries{reg, one, gap, uniformGap} {
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
	if !strings.Contains(string(b), `"series":null`) || !strings.Contains(string(b), `"cols":[`) || !strings.Contains(string(b), `"enc":"col"`) {
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
