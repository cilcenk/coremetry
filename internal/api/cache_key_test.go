package api

import "testing"

// v0.5.187 — locks the cache-key digest invariants for set-shaped
// inputs. The original bug: cache keys for the topology exclude
// list summarised by LENGTH only (`exN=%d`). Two distinct
// 1-element sets {"foo"} and {"bar"} collided on the same key
// `exN=1`, so opening Topology with exclude={foo} served the
// cached result computed for exclude={bar}.
//
// excludeKeyDigest is the sorted + FNV-64a fix. These tests guard
// the three invariants any digest helper used as a cache-key
// fragment must satisfy:
//   • distinctness  — different sets → different digests
//   • stability     — same set called twice → same digest
//   • permutation invariance — set semantics, order shouldn't matter
//
// Re-introducing the bug (e.g. switching back to `len(m)` or
// dropping the sort) breaks one of these and fails the suite.

func TestExcludeKeyDigest_DistinctSetsSameLength(t *testing.T) {
	// The historical bug: same length collapsed to same key. Any
	// re-introduction of length-only keying fails here first.
	a := excludeKeyDigest(map[string]bool{"foo": true})
	b := excludeKeyDigest(map[string]bool{"bar": true})
	if a == b {
		t.Fatalf("v0.5.187 regression: {foo} and {bar} produced same digest %q — length-only collapse re-introduced", a)
	}
}

func TestExcludeKeyDigest_Stable(t *testing.T) {
	// Same input → same output across calls. Catches accidental
	// hash-state leakage or non-deterministic ordering.
	m := map[string]bool{"alpha": true, "beta": true, "gamma": true}
	first := excludeKeyDigest(m)
	for i := 0; i < 10; i++ {
		got := excludeKeyDigest(m)
		if got != first {
			t.Fatalf("digest unstable on call %d: first=%q got=%q", i, first, got)
		}
	}
}

func TestExcludeKeyDigest_PermutationInvariant(t *testing.T) {
	// Set semantics: order of insertion / map iteration shouldn't
	// change the digest. Go's map iteration is randomised so we
	// rely on the sort step inside excludeKeyDigest.
	a := excludeKeyDigest(map[string]bool{"a": true, "b": true, "c": true})
	b := excludeKeyDigest(map[string]bool{"c": true, "b": true, "a": true})
	if a != b {
		t.Fatalf("permutation broke digest: %q != %q (sort step removed?)", a, b)
	}
}

func TestExcludeKeyDigest_EmptyDeterministic(t *testing.T) {
	// Empty set → fixed sentinel "0". Lets callers form a
	// stable cache key without conditionally omitting the
	// fragment.
	if got := excludeKeyDigest(nil); got != "0" {
		t.Fatalf("empty digest want %q got %q", "0", got)
	}
	if got := excludeKeyDigest(map[string]bool{}); got != "0" {
		t.Fatalf("zero-len map digest want %q got %q", "0", got)
	}
}

func TestExcludeKeyDigest_LargerSetDistinctness(t *testing.T) {
	// Beyond the 1-element case: two 5-element sets that differ
	// by exactly one member still must not collide.
	common := []string{"a", "b", "c", "d"}
	a := map[string]bool{"x": true}
	b := map[string]bool{"y": true}
	for _, k := range common {
		a[k] = true
		b[k] = true
	}
	if excludeKeyDigest(a) == excludeKeyDigest(b) {
		t.Fatalf("v0.5.187 regression: 5-element sets differing in one entry collided")
	}
}
