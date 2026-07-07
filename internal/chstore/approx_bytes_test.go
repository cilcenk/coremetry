package chstore

// v0.8.355 (HA audit 🟡#1) — sizer sanity for the ingest byte budget.
// The estimates feed consumer.NewSized's OOM safety valve; what matters
// is that (a) every sizer has a non-trivial floor, and (b) the variable
// payload — the part that actually OOMs pods, like 15-25KB Java
// stack-trace log bodies — is reflected byte-for-byte in the estimate.
// Exact heap accounting is a non-goal.

import (
	"strings"
	"testing"
)

func TestApproxBytesFloors(t *testing.T) {
	tests := []struct {
		name  string
		got   int
		floor int
	}{
		{"span", SpanApproxBytes(&Span{}), spanFixedBytes},
		{"log", LogApproxBytes(&Log{}), logFixedBytes},
		{"metric", MetricPointApproxBytes(&MetricPoint{}), metricFixedBytes},
		{"exemplar", ExemplarRowApproxBytes(&ExemplarRow{}), exemplarFixedBytes},
		{"span_link", SpanLinkRowApproxBytes(&SpanLinkRow{}), spanLinkFixedBytes},
	}
	for _, tt := range tests {
		if tt.got < tt.floor {
			t.Errorf("%s: zero-value estimate %d below fixed floor %d", tt.name, tt.got, tt.floor)
		}
	}
}

// The variable payload must move the estimate by at least its own byte
// length — the whole point of the budget is that a 20KB body costs 20KB.
func TestApproxBytesTrackPayload(t *testing.T) {
	fat := strings.Repeat("x", 20_000)
	attrs := []string{"k1", "k2"}
	vals := []string{strings.Repeat("v", 1000), "v2"}

	tests := []struct {
		name    string
		base    int
		grown   int
		minGrow int
	}{
		{
			name:    "log body dominates",
			base:    LogApproxBytes(&Log{}),
			grown:   LogApproxBytes(&Log{Body: fat}),
			minGrow: len(fat),
		},
		{
			name:    "span db.statement + attrs",
			base:    SpanApproxBytes(&Span{}),
			grown:   SpanApproxBytes(&Span{DBStatement: fat, AttrKeys: attrs, AttrValues: vals}),
			minGrow: len(fat) + len(vals[0]),
		},
		{
			name:    "metric histogram buckets",
			base:    MetricPointApproxBytes(&MetricPoint{}),
			grown:   MetricPointApproxBytes(&MetricPoint{BucketBounds: make([]float64, 100), BucketCounts: make([]uint64, 101)}),
			minGrow: 100*8 + 101*8,
		},
		{
			name:    "exemplar filtered attrs",
			base:    ExemplarRowApproxBytes(&ExemplarRow{}),
			grown:   ExemplarRowApproxBytes(&ExemplarRow{FilteredAttrs: map[string]string{"pod": fat}}),
			minGrow: len(fat),
		},
		{
			name:    "span link attrs",
			base:    SpanLinkRowApproxBytes(&SpanLinkRow{}),
			grown:   SpanLinkRowApproxBytes(&SpanLinkRow{AttrKeys: attrs, AttrVals: vals}),
			minGrow: len(vals[0]),
		},
	}
	for _, tt := range tests {
		if grow := tt.grown - tt.base; grow < tt.minGrow {
			t.Errorf("%s: estimate grew %d bytes for ≥%d bytes of payload — fat items would evade the budget", tt.name, grow, tt.minGrow)
		}
	}
}
