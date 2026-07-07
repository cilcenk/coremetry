package chstore

import (
	"reflect"
	"strings"
	"testing"
)

// endpoints_detail_test.go — v0.8.360 (Stage-2 slice E2). Pure-function
// pins for the endpoint detail drawer's backend:
//
//   • CollapseLatencyHistogram — 2-D → 1-D sum semantics (the drawer's
//     latency distribution), incl. ragged/empty grids.
//   • endpointRoutePred — signature mode binds the opSig regex args in
//     placeholder order BEFORE the path (the v0.8.356 server-side-param
//     trap means the patterns are ? args; a mis-ordered splice binds a
//     regex where the path belongs and silently matches nothing).
//   • EndpointSplitDims — the split-by whitelist stays sorted + closed
//     (frontend select and the 400 message read it; a free-form `by`
//     must never reach SQL identity).

func TestCollapseLatencyHistogram(t *testing.T) {
	cases := []struct {
		name string
		in   *LatencyHeatmap
		bins []float64
		cnts []uint64
	}{
		{
			name: "nil heatmap",
			in:   nil,
			bins: nil, cnts: nil,
		},
		{
			name: "empty bins",
			in:   &LatencyHeatmap{},
			bins: nil, cnts: nil,
		},
		{
			name: "sums across time columns per duration bin",
			in: &LatencyHeatmap{
				DurationBins: []float64{1, 10, 100},
				Counts: [][]uint32{
					{1, 0, 5},
					{2, 3, 0},
					{0, 0, 7},
				},
			},
			bins: []float64{1, 10, 100},
			cnts: []uint64{3, 3, 12},
		},
		{
			name: "ragged column shorter than bins is tolerated",
			in: &LatencyHeatmap{
				DurationBins: []float64{1, 10},
				Counts: [][]uint32{
					{4},
					{1, 2},
				},
			},
			bins: []float64{1, 10},
			cnts: []uint64{5, 2},
		},
		{
			name: "column longer than bins never panics (extra cells dropped)",
			in: &LatencyHeatmap{
				DurationBins: []float64{1},
				Counts:       [][]uint32{{2, 9, 9}},
			},
			bins: []float64{1},
			cnts: []uint64{2},
		},
		{
			name: "no time columns yields all-zero distribution",
			in: &LatencyHeatmap{
				DurationBins: []float64{1, 10},
				Counts:       [][]uint32{},
			},
			bins: []float64{1, 10},
			cnts: []uint64{0, 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bins, cnts := CollapseLatencyHistogram(tc.in)
			if !reflect.DeepEqual(bins, tc.bins) {
				t.Errorf("bins = %v, want %v", bins, tc.bins)
			}
			if !reflect.DeepEqual(cnts, tc.cnts) {
				t.Errorf("counts = %v, want %v", cnts, tc.cnts)
			}
		})
	}
}

func TestEndpointRoutePredArgOrder(t *testing.T) {
	t.Run("raw mode is a plain equality with one arg", func(t *testing.T) {
		var wc whereClause
		endpointRoutePred(&wc, "/orders/8421", false)
		if len(wc.conds) != 1 || wc.conds[0] != "http_route = ?" {
			t.Fatalf("conds = %v", wc.conds)
		}
		if !reflect.DeepEqual(wc.args, []interface{}{"/orders/8421"}) {
			t.Fatalf("args = %v", wc.args)
		}
	})
	t.Run("signature mode binds UUID/hex/num regexes then the path", func(t *testing.T) {
		var wc whereClause
		endpointRoutePred(&wc, "/orders/:id", true)
		if len(wc.conds) != 1 {
			t.Fatalf("conds = %v", wc.conds)
		}
		// The opSig placeholders appear innermost-first in the SQL text
		// (UUID, hex, num — the opSigArgs contract), then the compared
		// path. Placeholder count in the text must equal len(args).
		if got := strings.Count(wc.conds[0], "?"); got != 4 {
			t.Fatalf("placeholder count = %d, want 4 (cond %q)", got, wc.conds[0])
		}
		want := []interface{}{OpSigReUUID, OpSigReHex, OpSigReNum, "/orders/:id"}
		if !reflect.DeepEqual(wc.args, want) {
			t.Fatalf("args = %v, want %v", wc.args, want)
		}
		// The regex patterns must arrive as bind args, never inlined —
		// literal braces in the SQL text flip clickhouse-go into
		// server-side parameter mode (the v0.8.356 endpoints 500).
		if strings.Contains(wc.conds[0], "{") {
			t.Fatalf("regex braces leaked into SQL text: %q", wc.conds[0])
		}
	})
}

func TestEndpointSplitDims(t *testing.T) {
	dims := EndpointSplitDims()
	if !sortStringsIsSorted(dims) {
		t.Fatalf("EndpointSplitDims not sorted: %v", dims)
	}
	// Closed whitelist — every id resolves to an expression, and the
	// identity dimensions the drawer is already scoped by stay out.
	for _, d := range dims {
		if _, ok := endpointSplitDims[d]; !ok {
			t.Errorf("dim %q missing from map", d)
		}
	}
	for _, banned := range []string{"service.name", "http.route"} {
		if _, ok := endpointSplitDims[banned]; ok {
			t.Errorf("identity dimension %q must not be splittable", banned)
		}
	}
	// The reader rejects anything outside the whitelist BEFORE any SQL
	// is built (free-form `by` must never reach SQL identity).
	if _, err := (&Store{}).EndpointSplit(t.Context(), EndpointDetailQuery{}, "attr_keys[1]); DROP TABLE spans", 10); err == nil {
		t.Fatal("EndpointSplit accepted a non-whitelisted dimension")
	}
}

func sortStringsIsSorted(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}
