package logstore

import "testing"

// v0.9.286 — the ES backend retained a Point-in-Time whenever a search
// returned a FULL page, on the theory that a full page means the caller
// will want the next one. At 10B docs/day the first page is always
// full, so in practice every ordinary read-and-abandon search pinned
// segment readers for the 2m keep_alive — and the Drain puller did it
// from a timer, every interval, for the life of the process, while
// throwing the cursor away.
//
// Retention now needs the caller to SAY it will page. This table is the
// contract; the first row is the leak.
func TestESRetainPIT(t *testing.T) {
	const limit = 100

	cases := []struct {
		name       string
		got        int
		wantCursor bool
		want       bool
	}{
		{
			// THE regression: a full page from a caller that will never
			// come back. Every /logs drawer, every span-detail read,
			// every puller tick.
			name: "full page, no declared intent — release now",
			got:  limit, wantCursor: false, want: false,
		},
		{
			name: "full page, caller will page — retain for search_after",
			got:  limit, wantCursor: true, want: true,
		},
		{
			// A short page is the last page: there is nothing to page to,
			// so release immediately rather than waiting out keep_alive.
			name: "short page, caller will page — nothing left, release",
			got:  limit - 1, wantCursor: true, want: false,
		},
		{
			name: "short page, no intent — release",
			got:  limit - 1, wantCursor: false, want: false,
		},
		{
			name: "empty result, caller will page — release",
			got:  0, wantCursor: true, want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := esRetainPIT(tc.got, limit, tc.wantCursor); got != tc.want {
				t.Fatalf("esRetainPIT(got=%d, limit=%d, wantCursor=%v) = %v, want %v",
					tc.got, limit, tc.wantCursor, got, tc.want)
			}
		})
	}
}

// Intent alone must never be enough either — declaring paging on a
// query that returned a short page would hold a PIT open for a cursor
// nobody can use.
func TestESRetainPITNeedsBothConditions(t *testing.T) {
	const limit = 50
	if esRetainPIT(limit, limit, false) {
		t.Fatal("fullness alone must not retain — that was the leak")
	}
	if esRetainPIT(1, limit, true) {
		t.Fatal("intent alone must not retain — no next page exists")
	}
	if !esRetainPIT(limit, limit, true) {
		t.Fatal("both conditions must still retain — paging would break otherwise")
	}
}
