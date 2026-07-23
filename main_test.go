package main

import (
	"errors"
	"testing"
	"time"

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

// v0.8.206 — operator-reported: setting COREMETRY_INITIAL_ADMIN/PASSWORD via
// env did not let them log in — the DB stayed authoritative because
// seedInitialAdmin was a no-op once any user existed. shouldWriteBootstrapAdmin
// is the minimal pure gate the fix touches: seed when the table is empty, OR
// when COREMETRY_ADMIN_RESET makes the env creds authoritative (reconcile every
// boot). This pins both branches so a future tweak can't silently re-break the
// env-managed-creds / locked-out-admin recovery path.
func TestShouldWriteBootstrapAdmin(t *testing.T) {
	cases := []struct {
		name      string
		userCount int64
		reset     bool
		want      bool
	}{
		{"empty table seeds", 0, false, true},
		{"empty table seeds even without reset", 0, false, true},
		{"populated table is a no-op (seed-once)", 5, false, false},
		{"reset reconciles a populated table", 5, true, true},
		{"reset on empty table still writes", 0, true, true},
		{"one existing user, no reset, skip", 1, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldWriteBootstrapAdmin(c.userCount, c.reset); got != c.want {
				t.Fatalf("shouldWriteBootstrapAdmin(%d, %v) = %v, want %v",
					c.userCount, c.reset, got, c.want)
			}
		})
	}
}

// v0.8.218 — self-observability is ON by default in monolithic mode (no env),
// pointing at the pod's own OTLP receiver. selfObsDefaultEndpoint derives that
// localhost target from the gRPC listen address; pin the parse so a non-default
// COREMETRY_GRPC_ADDR still resolves to the right self-ingest port.
func TestSelfObsDefaultEndpoint(t *testing.T) {
	cases := map[string]string{
		":4317":          "localhost:4317",
		"0.0.0.0:4317":   "localhost:4317",
		":14317":         "localhost:14317",
		"4317":           "localhost:4317", // no colon → default port
		"":               "localhost:4317",
	}
	for in, want := range cases {
		if got := selfObsDefaultEndpoint(in); got != want {
			t.Errorf("selfObsDefaultEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

// v0.8.207 — operator-reported: COREMETRY_CH_RESET_SCHEMA=1 set on the
// long-running Deployment dropped the database, exited, and restarted into
// another drop forever (CrashLoopBackOff) — the DB was never recreated. The
// reset path was designed for a one-shot Helm Job (drop + exit), but the env
// alias lands on the Deployment. resetExitsAfterDrop is the minimal pure gate
// the fix touches: the --reset-schema flag (Job) exits, the env (Deployment)
// falls through to recreate + run. This pins both so the crash-loop can't
// silently return.
func TestResetExitsAfterDrop(t *testing.T) {
	if !resetExitsAfterDrop(false) {
		t.Fatal("--reset-schema flag (one-shot Job) must exit after the drop")
	}
	if resetExitsAfterDrop(true) {
		t.Fatal("COREMETRY_CH_RESET_SCHEMA env (Deployment) must NOT exit — it recreates + runs")
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

// v0.9.191 — regression pin for the v0.9.188 errors-inbox loop wedge.
// Operator-reported: prod'da yeni exception'lar hiç görünmüyordu.
// Root cause: the tick advanced `since` ONLY on success, so a
// persistently-failing 24h first-tick (prod-scale OOM, CH code 241)
// retried the SAME window forever and the inbox never reached the
// cheap 5-min steady state. The contract under test: the window
// advances to now-5m after failure exactly as after success.
func TestNextExceptionRefreshSinceAdvancesOnError(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	backfill := now.Add(-24 * time.Hour)
	want := now.Add(-5 * time.Minute)

	fail := func(s time.Time) (int, error) {
		if !s.Equal(backfill) {
			t.Fatalf("refresh got window %v, want the 24h backfill %v", s, backfill)
		}
		return 0, errors.New("code: 241, memory limit exceeded")
	}
	next, n, err := nextExceptionRefreshSince(fail, backfill, now)
	if err == nil || n != 0 {
		t.Fatalf("expected the refresh error to propagate, got n=%d err=%v", n, err)
	}
	if !next.Equal(want) {
		t.Fatalf("since after FAILED pass = %v, want %v (the v0.9.188 wedge: it must advance)", next, want)
	}

	okFn := func(s time.Time) (int, error) { return 7, nil }
	next2, n2, err2 := nextExceptionRefreshSince(okFn, next, now)
	if err2 != nil || n2 != 7 {
		t.Fatalf("success pass: n=%d err=%v", n2, err2)
	}
	if !next2.Equal(want) {
		t.Fatalf("since after success = %v, want %v", next2, want)
	}
}
