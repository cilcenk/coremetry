package api

import "testing"

// problems_scan_test.go — v0.9.342.
//
// Found by the v1.0 readiness audit: /api/problems applied THREE narrowing
// filters in Go after the SQL LIMIT — priority, owner/SRE team, and cluster.
// It is the last surface carrying the defect family swept out of the triage
// queue in v0.9.322 / 330 / 335 / 336: a filter applied after a LIMIT answers
// over a slice, and the rows the operator asked for may never have entered
// the window.
//
// Team and cluster both resolve to a SERVICE SET, so they moved into SQL.
// Priority genuinely cannot: Problem.Priority is computed at read time by
// EnrichProblemsWithPriority from the enriched value/threshold/deploy/status
// and is not a column. For that one, the candidate scan widens instead.

func TestProblemScanLimit(t *testing.T) {
	cases := []struct {
		name     string
		page     int
		narrowed bool
		want     int
	}{
		// No priority narrow → the scan IS the page. The common poll must not
		// get more expensive for a filter nobody selected.
		{"unfiltered default", 100, false, 100},
		{"unfiltered large page", 500, false, 500},

		// Narrowed → more candidates, because the narrow runs after the read.
		{"P1 only on the default page", 100, true, 500},
		{"narrowed small page", 20, true, 100},

		// Bounded: a large page size must not turn into an unbounded read.
		// The enrichment chain (runbooks/teams/clusters/deploys/root-cause)
		// runs over whatever comes back.
		{"ceiling applies", 1000, true, problemScanCeiling},

		// Defensive: a missing/zero limit takes the same default the store
		// uses, not a zero-row scan.
		{"zero page size", 0, false, 100},
		{"zero page size, narrowed", 0, true, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := problemScanLimit(tc.page, tc.narrowed); got != tc.want {
				t.Errorf("problemScanLimit(%d, %v) = %d, want %d",
					tc.page, tc.narrowed, got, tc.want)
			}
		})
	}

	// The property that makes the widening honest: a narrowed scan is never
	// SMALLER than an unnarrowed one, and always at least the page.
	for _, page := range []int{1, 20, 100, 500, 1000} {
		wide, narrow := problemScanLimit(page, true), problemScanLimit(page, false)
		if wide < narrow {
			t.Errorf("page=%d: narrowed scan %d < unnarrowed %d", page, wide, narrow)
		}
		if wide < page && wide != problemScanCeiling {
			t.Errorf("page=%d: scan %d cannot fill the requested page", page, wide)
		}
	}
}

// nil and empty mean DIFFERENT things here, and conflating them is how a
// filter either stops working or empties the page.
func TestIntersectServices(t *testing.T) {
	// nil = "this axis places no constraint" — it must not zero the other.
	if got := intersectServices(nil, []string{"a", "b"}); len(got) != 2 {
		t.Errorf("nil ∩ {a,b} = %v, want {a,b} — nil is 'no constraint', not 'nothing'", got)
	}
	if got := intersectServices([]string{"a"}, nil); len(got) != 1 {
		t.Errorf("{a} ∩ nil = %v, want {a}", got)
	}
	if got := intersectServices(nil, nil); got != nil {
		t.Errorf("nil ∩ nil = %v, want nil (still unconstrained)", got)
	}

	got := intersectServices([]string{"a", "b", "c"}, []string{"b", "c", "d"})
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("intersection = %v, want {b,c}", got)
	}

	// An EMPTY intersection is meaningful and must survive as empty-not-nil:
	// the team and the cluster share no service, so the page is empty. Losing
	// the distinction here would return an UNFILTERED page instead.
	empty := intersectServices([]string{"a"}, []string{"z"})
	if empty == nil {
		t.Error("an empty intersection must stay non-nil — nil would drop the constraint and show everything")
	}
	if len(empty) != 0 {
		t.Errorf("intersection = %v, want empty", empty)
	}
}
