package api

// messaging_prior_test.go — v0.8.364 (Stage-2 slice M1). Pins the
// pure prior-window merge behind /api/messaging?compare=prior:
// prior counters land on the current rows by full
// (system, cluster, destination) identity — never by rank, never
// by a partial key — and rows without a prior twin keep zero
// Prior* fields so the JSON omits them and the frontend renders
// no delta badge.

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func msgRow(system, cluster, dest string, spans, errs, produce, consume uint64, avg, p50, p99 float64) chstore.MessagingInstance {
	return chstore.MessagingInstance{
		System: system, Cluster: cluster, Destination: dest,
		SpanCount: spans, ErrorCount: errs,
		ProduceCount: produce, ConsumeCount: consume,
		AvgMs: avg, P50Ms: p50, P99Ms: p99,
	}
}

func TestMergeMessagingPrior(t *testing.T) {
	cases := []struct {
		name  string
		cur   []chstore.MessagingInstance
		prior []chstore.MessagingInstance
		// want[i] = expected Prior* tuple on cur[i]:
		// spans, errors, produce, consume, avg, p50, p99
		want []struct {
			spans, errs, produce, consume uint64
			avg, p50, p99                 float64
		}
	}{
		{
			name:  "exact identity match copies every prior counter",
			cur:   []chstore.MessagingInstance{msgRow("kafka", "(default)", "orders", 100, 5, 60, 40, 12, 8, 90)},
			prior: []chstore.MessagingInstance{msgRow("kafka", "(default)", "orders", 80, 2, 50, 30, 10, 7, 70)},
			want: []struct {
				spans, errs, produce, consume uint64
				avg, p50, p99                 float64
			}{{80, 2, 50, 30, 10, 7, 70}},
		},
		{
			name:  "row absent from prior window keeps zero Prior fields",
			cur:   []chstore.MessagingInstance{msgRow("kafka", "(default)", "brand-new-topic", 10, 0, 10, 0, 1, 1, 2)},
			prior: []chstore.MessagingInstance{msgRow("kafka", "(default)", "orders", 80, 2, 50, 30, 10, 7, 70)},
			want: []struct {
				spans, errs, produce, consume uint64
				avg, p50, p99                 float64
			}{{0, 0, 0, 0, 0, 0, 0}},
		},
		{
			name: "cluster is part of the identity — same destination on two clusters never cross-matches",
			cur: []chstore.MessagingInstance{
				msgRow("kafka", "kafka-a:9092", "orders", 100, 0, 100, 0, 5, 4, 9),
				msgRow("kafka", "kafka-b:9092", "orders", 200, 0, 0, 200, 6, 5, 11),
			},
			prior: []chstore.MessagingInstance{
				msgRow("kafka", "kafka-b:9092", "orders", 150, 3, 0, 150, 7, 6, 13),
			},
			want: []struct {
				spans, errs, produce, consume uint64
				avg, p50, p99                 float64
			}{
				{0, 0, 0, 0, 0, 0, 0},
				{150, 3, 0, 150, 7, 6, 13},
			},
		},
		{
			name: "system is part of the identity too",
			cur:  []chstore.MessagingInstance{msgRow("rabbitmq", "(default)", "orders", 10, 0, 10, 0, 1, 1, 2)},
			prior: []chstore.MessagingInstance{
				msgRow("kafka", "(default)", "orders", 80, 2, 50, 30, 10, 7, 70),
			},
			want: []struct {
				spans, errs, produce, consume uint64
				avg, p50, p99                 float64
			}{{0, 0, 0, 0, 0, 0, 0}},
		},
		{
			name: "prior rank order is irrelevant — merge is by key, not index",
			cur: []chstore.MessagingInstance{
				msgRow("kafka", "(default)", "a", 1, 0, 1, 0, 1, 1, 1),
				msgRow("kafka", "(default)", "b", 2, 0, 0, 2, 2, 2, 2),
			},
			prior: []chstore.MessagingInstance{
				msgRow("kafka", "(default)", "b", 20, 1, 0, 20, 22, 21, 29),
				msgRow("kafka", "(default)", "a", 10, 0, 10, 0, 11, 10, 19),
			},
			want: []struct {
				spans, errs, produce, consume uint64
				avg, p50, p99                 float64
			}{
				{10, 0, 10, 0, 11, 10, 19},
				{20, 1, 0, 20, 22, 21, 29},
			},
		},
		{
			name:  "empty prior slice is a no-op",
			cur:   []chstore.MessagingInstance{msgRow("kafka", "(default)", "orders", 100, 5, 60, 40, 12, 8, 90)},
			prior: []chstore.MessagingInstance{},
			want: []struct {
				spans, errs, produce, consume uint64
				avg, p50, p99                 float64
			}{{0, 0, 0, 0, 0, 0, 0}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mergeMessagingPrior(tc.cur, tc.prior)
			for i, w := range tc.want {
				got := tc.cur[i]
				if got.PriorSpanCount != w.spans || got.PriorErrorCount != w.errs ||
					got.PriorProduceCount != w.produce || got.PriorConsumeCount != w.consume ||
					got.PriorAvgMs != w.avg || got.PriorP50Ms != w.p50 || got.PriorP99Ms != w.p99 {
					t.Errorf("row %d: got prior (spans=%d errs=%d produce=%d consume=%d avg=%v p50=%v p99=%v), want (%d %d %d %d %v %v %v)",
						i, got.PriorSpanCount, got.PriorErrorCount, got.PriorProduceCount, got.PriorConsumeCount,
						got.PriorAvgMs, got.PriorP50Ms, got.PriorP99Ms,
						w.spans, w.errs, w.produce, w.consume, w.avg, w.p50, w.p99)
				}
			}
		})
	}
}

// The merge must never mutate the CURRENT counters — only Prior*.
func TestMergeMessagingPriorLeavesCurrentIntact(t *testing.T) {
	cur := []chstore.MessagingInstance{msgRow("kafka", "(default)", "orders", 100, 5, 60, 40, 12, 8, 90)}
	prior := []chstore.MessagingInstance{msgRow("kafka", "(default)", "orders", 80, 2, 50, 30, 10, 7, 70)}
	mergeMessagingPrior(cur, prior)
	got := cur[0]
	if got.SpanCount != 100 || got.ErrorCount != 5 || got.ProduceCount != 60 ||
		got.ConsumeCount != 40 || got.AvgMs != 12 || got.P50Ms != 8 || got.P99Ms != 90 {
		t.Errorf("current counters mutated by merge: %+v", got)
	}
}
