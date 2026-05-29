package main

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.7.3 — operator-reported: the /users page showed four
// "admin@coremetry.local" rows. Root cause: seedInitialAdmin gave each
// seeded admin a RANDOM id, so concurrent multi-pod boots (distributed mode)
// and re-seeds each inserted a fresh row that ReplacingMergeTree (ORDER BY id)
// could not dedup. The fix derives a STABLE id from the email; this pins that
// it is deterministic, fixed-width, and collision-distinct so every seed of
// the same admin email converges onto one row.
func TestBootstrapAdminID(t *testing.T) {
	const email = "admin@coremetry.local"
	a := bootstrapAdminID(email)
	b := bootstrapAdminID(email)
	if a != b {
		t.Fatalf("bootstrapAdminID not deterministic: %q != %q", a, b)
	}
	if len(a) != 16 {
		t.Fatalf("id width = %d hex chars, want 16 (8 bytes)", len(a))
	}
	if other := bootstrapAdminID("other@coremetry.local"); other == a {
		t.Fatalf("distinct emails produced the same id %q", a)
	}
}

// v0.7.5 — the seeded example runbooks. They must be well-formed (unique
// runbook + step ids, valid step kinds, order = position) and collectively
// demonstrate all five step kinds, since they're a fresh install's first
// impression of the feature.
func TestExampleRunbooks(t *testing.T) {
	rbs := exampleRunbooks()
	if len(rbs) < 3 {
		t.Fatalf("want >= 3 example runbooks, got %d", len(rbs))
	}
	ids := map[string]bool{}
	kinds := map[string]bool{}
	for _, rb := range rbs {
		if rb.ID == "" || rb.Title == "" || len(rb.Steps) == 0 {
			t.Errorf("incomplete example runbook %q", rb.ID)
		}
		if ids[rb.ID] {
			t.Errorf("duplicate runbook id %q", rb.ID)
		}
		ids[rb.ID] = true
		stepIDs := map[string]bool{}
		for i, s := range rb.Steps {
			if !chstore.ValidRunbookStepKind(s.Kind) {
				t.Errorf("%s step %d: invalid kind %q", rb.ID, i, s.Kind)
			}
			if s.ID == "" || stepIDs[s.ID] {
				t.Errorf("%s step %d: empty/duplicate id %q", rb.ID, i, s.ID)
			}
			stepIDs[s.ID] = true
			if s.Order != i {
				t.Errorf("%s step %d: order=%d, want %d", rb.ID, i, s.Order, i)
			}
			kinds[s.Kind] = true
		}
	}
	for _, k := range []string{"manual", "query", "http", "javascript", "bash"} {
		if !kinds[k] {
			t.Errorf("example runbooks don't demonstrate step kind %q", k)
		}
	}
}
