package chstore

import (
	"reflect"
	"testing"
)

// v0.7.0 — Runbooks. The two pure functions behind the runbook CRUD path:
//
//   - normalizeRunbookSteps renumbers Order to match slice position (the
//     array is the source of truth for ordering; the editor reorders by
//     moving array elements). If this silently broke, a saved runbook
//     would render its steps in stale/duplicated order.
//   - ValidRunbookStepKind guards the kind enum at the API boundary so an
//     unknown kind (typo, future client) can't be persisted and later
//     dispatched to the agent as garbage.
//
// Table-driven per CLAUDE.md #11 so a refactor can't regress either.

func TestNormalizeRunbookSteps(t *testing.T) {
	cases := []struct {
		name string
		in   []RunbookStep
		want []int // expected Order, per resulting position
	}{
		{"empty", nil, nil},
		{"single", []RunbookStep{{Title: "a"}}, []int{0}},
		{
			"renumbers out-of-order input",
			[]RunbookStep{{Order: 5, Title: "a"}, {Order: 2, Title: "b"}, {Order: 9, Title: "c"}},
			[]int{0, 1, 2},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeRunbookSteps(c.in)
			var orders []int
			for _, st := range got {
				orders = append(orders, st.Order)
			}
			if !reflect.DeepEqual(orders, c.want) {
				t.Fatalf("orders = %v, want %v", orders, c.want)
			}
		})
	}

	// title is trimmed
	got := normalizeRunbookSteps([]RunbookStep{{Title: "  spaced  "}})
	if got[0].Title != "spaced" {
		t.Fatalf("title not trimmed: %q", got[0].Title)
	}
}

func TestValidRunbookStepKind(t *testing.T) {
	for _, k := range []string{"manual", "query", "http", "javascript", "bash"} {
		if !ValidRunbookStepKind(k) {
			t.Errorf("%q should be a valid step kind", k)
		}
	}
	for _, k := range []string{"", "Manual", "shell", "js", "python", "HTTP"} {
		if ValidRunbookStepKind(k) {
			t.Errorf("%q should be an invalid step kind", k)
		}
	}
}
