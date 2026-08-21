package evaluator

// v0.9.1213 — VM dönüşünün saf yarıları. Korunanlar: (a) pod coalesce
// zinciri chstore.runtimePodExpr ile AYNI sıra + 8-rune kesme,
// (b) türetme matematiği (pause/share/rate) ve sıfır-bölme koruması,
// (c) birleşimde karşılıksız seri sample ÜRETMEZ (yarım veri = alarm
// kararına girmez).

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestVMPodFromTuple(t *testing.T) {
	cases := []struct {
		name string
		key  []string
		want string
	}{
		{"k8s pod önce", []string{"svc", "pod-a", "host-1", "iid-123456789"}, "pod-a"},
		{"pod yoksa host", []string{"svc", "", "host-1", "iid"}, "host-1"},
		{"ikisi de yoksa instance.id 8 rune", []string{"svc", "", "", "a29474161xyz"}, "a2947416"},
		{"kısa instance.id aynen", []string{"svc", "", "", "ab12"}, "ab12"},
		{"hiçbiri yok", []string{"svc", "", "", ""}, ""},
		{"eksik tuple", []string{"svc"}, ""},
	}
	for _, c := range cases {
		if got := vmPodFromTuple(c.key); got != c.want {
			t.Errorf("%s: %q, beklenen %q", c.name, got, c.want)
		}
	}
}

func TestVMGCDerive(t *testing.T) {
	// 10 dk penceresinde 30 sn GC, 120 koleksiyon:
	// pause 250ms · pay %5 · 12 koleksiyon/dk.
	pauseMs, sharePct, rate := vmGCDerive(30, 120, 600)
	if pauseMs != 250 || sharePct != 5 || rate != 12 {
		t.Fatalf("türetme yanlış: pause=%v share=%v rate=%v", pauseMs, sharePct, rate)
	}
	// Sıfır koleksiyon = örnek yok (bölme koruması).
	if p, s, r := vmGCDerive(30, 0, 600); p != 0 || s != 0 || r != 0 {
		t.Fatalf("cnt=0 sıfır dönmeli: %v %v %v", p, s, r)
	}
}

func TestJoinVMGCSeries(t *testing.T) {
	mk := func(key []string, v float64) chstore.SpanMetricSeries {
		return chstore.SpanMetricSeries{GroupKey: key, Points: []chstore.SpanMetricPoint{{Time: 1, Value: v}}}
	}
	sums := []chstore.SpanMetricSeries{
		mk([]string{"checkout", "pod-a", "", ""}, 30),
		mk([]string{"checkout", "pod-b", "", ""}, 6), // cnt karşılığı YOK → atlanır
		mk([]string{"", "pod-c", "", ""}, 9),         // servissiz → atlanır
	}
	cnts := []chstore.SpanMetricSeries{
		mk([]string{"checkout", "pod-a", "", ""}, 120),
	}
	pauses, acts, err := joinVMGCSeries(sums, cnts, 600)
	if err != nil {
		t.Fatal(err)
	}
	if len(pauses) != 1 || len(acts) != 1 {
		t.Fatalf("yalnız tam çift sample üretmeli: %d/%d", len(pauses), len(acts))
	}
	p, a := pauses[0], acts[0]
	if p.Instance != "checkout" || p.Subkey != "pod-a" || p.Usage != 250 {
		t.Errorf("pause örneği yanlış: %+v", p)
	}
	if a.SharePct != 5 || a.RatePerMin != 12 {
		t.Errorf("aktivite örneği yanlış: %+v", a)
	}
}
