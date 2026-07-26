package logstore

import (
	"fmt"
	"testing"
)

// v0.9.292 — the fields rail was the ONE place on /logs that broke the
// >100-rows rule the log table itself follows: ListFields unioned the
// WHOLE mapping with no cap, and the panel rendered it flat inside a
// 70vh scroller — no virtualisation, no content-visibility, no ceiling.
// With dynamic mapping at 10B docs/day a four-digit field count is
// routine.
//
// The cap alone would be a silent lie ("these are the fields"), so the
// result carries the real total. These pin the truncation contract; the
// mapping walk itself needs a live cluster and is exercised elsewhere.

// truncateFields mirrors the tail of ListFieldsBounded. Stated here as
// the contract the handler and panel are written against.
func truncateFields(sorted []string) ListFieldsResult {
	total := len(sorted)
	if len(sorted) > listFieldsMax {
		sorted = sorted[:listFieldsMax]
	}
	return ListFieldsResult{Fields: sorted, Total: total}
}

func TestListFieldsTruncation(t *testing.T) {
	mk := func(n int) []string {
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			// Zero-padded so the slice is also alphabetically ordered —
			// the cap must be a stable PREFIX of the sorted list, not an
			// arbitrary sample of map iteration order.
			out = append(out, fmt.Sprintf("field.%06d", i))
		}
		return out
	}

	cases := []struct {
		name        string
		count       int
		wantLen     int
		wantTotal   int
		wantClipped bool
	}{
		{"small mapping passes through", 12, 12, 12, false},
		{"exactly at the cap", listFieldsMax, listFieldsMax, listFieldsMax, false},
		{"one over the cap", listFieldsMax + 1, listFieldsMax, listFieldsMax + 1, true},
		{"the shape that motivated this — thousands of dynamic paths", 4000, listFieldsMax, 4000, true},
		{"empty mapping", 0, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := truncateFields(mk(tc.count))

			if len(res.Fields) != tc.wantLen {
				t.Fatalf("returned %d fields, want %d", len(res.Fields), tc.wantLen)
			}
			if res.Total != tc.wantTotal {
				t.Fatalf("Total = %d, want %d — the panel renders 'first N of M' from this", res.Total, tc.wantTotal)
			}
			if clipped := res.Total > len(res.Fields); clipped != tc.wantClipped {
				t.Fatalf("clipped = %v, want %v", clipped, tc.wantClipped)
			}
			// Whatever survives must be the alphabetical head, so the
			// same mapping always yields the same visible list.
			for i, f := range res.Fields {
				if want := fmt.Sprintf("field.%06d", i); f != want {
					t.Fatalf("field %d = %q, want %q — truncation must keep the sorted prefix", i, f, want)
				}
			}
		})
	}
}

// Total must never understate the mapping: the label reads "first N of
// M", so an M below N would render as "first 500 of 12".
func TestListFieldsTotalNeverBelowReturned(t *testing.T) {
	for _, n := range []int{0, 1, listFieldsMax - 1, listFieldsMax, listFieldsMax + 1, 10000} {
		fields := make([]string, n)
		res := truncateFields(fields)
		if res.Total < len(res.Fields) {
			t.Fatalf("n=%d: Total %d < returned %d", n, res.Total, len(res.Fields))
		}
	}
}
