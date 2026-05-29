package main

import "testing"

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
