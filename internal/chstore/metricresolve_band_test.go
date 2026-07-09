package chstore

import (
	"strings"
	"testing"
)

// v0.8.410 (Tempo-parity T1) — agg "band": one resolver call returns
// the whole p50/p90/p95/p99 percentile band. These pin the pure
// pieces: the single-merge projection, the quantile↔index alignment,
// and the fallback relabeling that keeps group keys distinguishable.

func TestBandProjectionSingleMerge(t *testing.T) {
	p := bandProjection()
	if strings.Count(p, "quantilesTDigestMerge") != 1 {
		t.Fatalf("band must finalize the tdigest state exactly once: %s", p)
	}
	// Index alignment with bandQuantileLabels — the state was built
	// with (0.5, 0.9, 0.95, 0.99); reordering either side silently
	// mislabels every line on the chart.
	if !strings.Contains(p, "(0.5, 0.9, 0.95, 0.99)") {
		t.Fatalf("band quantile order must match the doorway state: %s", p)
	}
	want := []string{"p50", "p90", "p95", "p99"}
	if len(bandQuantileLabels) != len(want) {
		t.Fatalf("bandQuantileLabels = %v", bandQuantileLabels)
	}
	for i, w := range want {
		if bandQuantileLabels[i] != w {
			t.Fatalf("bandQuantileLabels[%d] = %q, want %q", i, bandQuantileLabels[i], w)
		}
	}
	if !strings.Contains(p, "/ 1e6") {
		t.Fatalf("band must convert ns → ms like every other duration agg: %s", p)
	}
}

func TestRelabelBandSeries(t *testing.T) {
	in := []SpanMetricSeries{
		{GroupKey: []string{"checkout"}, Points: []SpanMetricPoint{{Time: 1, Value: 2}}},
		{GroupKey: []string{}, Points: nil},
	}
	out := relabelBandSeries(in, "p95")
	if got := strings.Join(out[0].GroupKey, "|"); got != "checkout|p95" {
		t.Errorf("grouped key = %q, want %q", got, "checkout|p95")
	}
	if got := strings.Join(out[1].GroupKey, "|"); got != "p95" {
		t.Errorf("bare key = %q, want %q", got, "p95")
	}
	// The input must not be mutated (callers reuse the fallback series).
	if len(in[0].GroupKey) != 1 {
		t.Errorf("relabel mutated its input: %v", in[0].GroupKey)
	}
	if out[0].Points[0].Value != 2 {
		t.Errorf("points must carry over")
	}
}

// The single-agg projection must keep rejecting unknown aggs — "band"
// is dispatched BEFORE spanmetricStateAgg and must not leak into it.
func TestBandNotASingleAgg(t *testing.T) {
	if _, err := spanmetricStateAgg("band", 60); err == nil {
		t.Fatal("spanmetricStateAgg must not accept band — it is a dedicated multi-column path")
	}
}
