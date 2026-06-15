package api

import "testing"

// Correlated Signals (task #6) — pins the cache-key digest invariants for the
// correlation bundle's SIX-input key. The bundle hashes kind + traceId +
// service + tsBucket + fromBucket + toBucket + metricKind into ONE cache key.
// If any input stops contributing (the v0.5.187 length-only anti-pattern, or a
// concatenation that drops a fragment), two distinct anchors collide and serve
// each other's cached bundle — the exact cross-poison the topology exclude-set
// incident taught us to test.
//
// correlateKeyDigest must satisfy:
//   • distinctness  — changing ANY one input changes the digest
//   • stability     — same inputs → same digest across calls
//   • permutation invariance — the order we list fragments at the call site
//     must not change the key (it's a set digest)
//   • slot integrity — an empty value occupies a distinct slot (a missing
//     service must not collide with a missing traceId)

// anchorKey mirrors the exact fragment construction in getCorrelationContext so
// the test exercises the real call shape, not a simplified one.
func anchorKey(kind, trace, svc, ts, from, to, mk string) string {
	return correlateKeyDigest(
		"kind="+kind,
		"trace="+trace,
		"svc="+svc,
		"ts="+ts,
		"from="+from,
		"to="+to,
		"mk="+mk,
	)
}

func TestCorrelateKey_Stable(t *testing.T) {
	a := anchorKey("trace", "c9ea", "checkout", "100", "0", "200", "")
	for i := 0; i < 10; i++ {
		if got := anchorKey("trace", "c9ea", "checkout", "100", "0", "200", ""); got != a {
			t.Fatalf("digest unstable on call %d: first=%q got=%q", i, a, got)
		}
	}
}

func TestCorrelateKey_EachInputDistinct(t *testing.T) {
	// Base anchor; flip exactly one input at a time and assert the digest moves.
	base := anchorKey("trace", "c9ea", "checkout", "100", "0", "200", "")
	cases := []struct {
		name string
		got  string
	}{
		{"kind", anchorKey("log", "c9ea", "checkout", "100", "0", "200", "")},
		{"traceId", anchorKey("trace", "deadbeef", "checkout", "100", "0", "200", "")},
		{"service", anchorKey("trace", "c9ea", "payments", "100", "0", "200", "")},
		{"tsBucket", anchorKey("trace", "c9ea", "checkout", "999", "0", "200", "")},
		{"fromBucket", anchorKey("trace", "c9ea", "checkout", "100", "1", "200", "")},
		{"toBucket", anchorKey("trace", "c9ea", "checkout", "100", "0", "999", "")},
		{"metricKind", anchorKey("trace", "c9ea", "checkout", "100", "0", "200", "error")},
	}
	for _, c := range cases {
		if c.got == base {
			t.Fatalf("v0.5.187 regression: changing %s did NOT change the digest — that input is not hashed", c.name)
		}
	}
}

func TestCorrelateKey_PermutationInvariant(t *testing.T) {
	// The digest is a SET digest — the order fragments are passed must not
	// change the key. (Guards against a future refactor that reorders the call
	// site assuming order doesn't matter — it genuinely doesn't, by design.)
	a := correlateKeyDigest("kind=trace", "trace=c9ea", "svc=checkout")
	b := correlateKeyDigest("svc=checkout", "kind=trace", "trace=c9ea")
	if a != b {
		t.Fatalf("permutation broke digest: %q != %q (sort step removed?)", a, b)
	}
}

func TestCorrelateKey_EmptySlotIntegrity(t *testing.T) {
	// A missing service and a missing traceId must NOT collide: each empty
	// value still occupies its own labelled slot. Two anchors that differ only
	// in WHICH field is empty must produce different digests.
	missingTrace := anchorKey("log", "", "checkout", "100", "0", "200", "")
	missingSvc := anchorKey("log", "checkout", "", "100", "0", "200", "")
	if missingTrace == missingSvc {
		t.Fatalf("empty-slot collision: trace='' svc='checkout' and trace='checkout' svc='' produced same digest %q", missingTrace)
	}
}
