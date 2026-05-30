package chstore

import "testing"

// v0.7.35 — Operator-reported: Database page "top statements" showed locally but
// went blank at scale (1000s of services / 100+ DBs). Root cause: the statement
// scan filtered by db_system + peer_service but NOT service_name, so it couldn't
// use the spans (service_name, time) primary key and timed out at billion-span
// scale. The fix scopes the scan to the services that actually call the DB (from
// the caller breakdown). distinctCallerServices builds that list — deduped,
// non-empty, first-seen order. CLAUDE.md #11.
func TestDistinctCallerServices(t *testing.T) {
	in := []DBCallerBreakdown{
		{Service: "orders", Pod: "p1"},
		{Service: "orders", Pod: "p2"}, // same service, different pod → one entry
		{Service: "billing", Pod: "p1"},
		{Service: "", Pod: "p3"}, // empty service skipped
		{Service: "orders", Pod: "p3"},
	}
	got := distinctCallerServices(in)
	want := []string{"orders", "billing"} // first-seen order, deduped, no empties
	if len(got) != len(want) {
		t.Fatalf("distinctCallerServices = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if n := len(distinctCallerServices(nil)); n != 0 {
		t.Errorf("nil input → %d entries, want 0", n)
	}
}
