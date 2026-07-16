// pivot_flatten_test.go — v0.8.555 regression.
//
// getSeriesExemplars flattened the gk→fingerprints map with a direct range
// over the map. Go randomises map iteration order on purpose, so past
// ~pivotMaxFingerprints total fingerprints two things changed on every
// call: which groups won the budget, and which gk claimed a fingerprint
// appearing under several groups. The visible symptom was ◆ exemplar
// markers hopping between chart series every time the 30s cache expired.
//
// flattenSeriesFPs visits group keys in sorted order, which pins both.
package api

import (
	"reflect"
	"testing"
)

func TestFlattenSeriesFPs(t *testing.T) {
	cases := []struct {
		name     string
		in       map[string][]uint64
		max      int
		wantFlat []uint64            // exact order
		wantAttr map[uint64][]string // spot checks; nil = skip
	}{
		{
			name: "empty input", in: map[string][]uint64{}, max: 100,
			wantFlat: []uint64{},
		},
		{
			// The ungrouped query shape: one entry under the empty gk.
			// Its attribution must be the EMPTY slice, not a [""] —
			// strings.Split("", "|") would produce the latter.
			name: "ungrouped empty gk yields empty parts",
			in:   map[string][]uint64{"": {7}}, max: 100,
			wantFlat: []uint64{7},
			wantAttr: map[uint64][]string{7: {}},
		},
		{
			// The budget bug: with max=3 over three groups, the winners
			// must be the lexicographically-first groups, always.
			name: "budget goes to sorted-first groups",
			in:   map[string][]uint64{"b": {3, 4}, "a": {1, 2}, "c": {5}},
			max:  3,
			wantFlat: []uint64{1, 2, 3},
		},
		{
			// The attribution bug: a fingerprint under several groups must
			// always be claimed by the sorted-first gk.
			name: "shared fp attributed to first sorted gk",
			in:   map[string][]uint64{"zzz": {9}, "aaa": {9}}, max: 100,
			wantFlat: []uint64{9},
			wantAttr: map[uint64][]string{9: {"aaa"}},
		},
		{
			name: "multi-part gk splits on pipe",
			in:   map[string][]uint64{"svc|op": {1}}, max: 100,
			wantFlat: []uint64{1},
			wantAttr: map[uint64][]string{1: {"svc", "op"}},
		},
		{
			// Fingerprints past the cap still enter the attribution map —
			// harmless (reads only return rows for flat) and pinned so a
			// future "optimisation" doesn't silently change it.
			name: "over-cap fps keep their attribution",
			in:   map[string][]uint64{"a": {1, 2, 3}}, max: 2,
			wantFlat: []uint64{1, 2},
			wantAttr: map[uint64][]string{3: {"a"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 50 rounds: one pass can get lucky with map order — the old
			// code passed single-shot runs often enough to ship.
			for i := 0; i < 50; i++ {
				flat, attr := flattenSeriesFPs(c.in, c.max)
				if !reflect.DeepEqual(flat, c.wantFlat) {
					t.Fatalf("round %d: flat = %v, want %v", i, flat, c.wantFlat)
				}
				for fp, want := range c.wantAttr {
					if !reflect.DeepEqual(attr[fp], want) {
						t.Fatalf("round %d: attr[%d] = %v, want %v", i, fp, attr[fp], want)
					}
				}
			}
		})
	}
}
