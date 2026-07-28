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

// v0.9.251 — the /api/inbox free-text search (`q`) must be part of the
// cache key.
//
// This is the v0.5.187 collision class with a new vector. There, a Set
// was summarised by its LENGTH, so two different sets shared a key and
// cross-poisoned. Here the input is a search box: if `q` changed the
// rows but not the key, the first operator to search "timeout" would
// poison the shared entry and the next operator's unfiltered request
// would be served their filtered page — with the badge counting one
// number and the list showing another.
func TestInboxListKeyIncludesSearch(t *testing.T) {
	base := inboxListKey("open", "", "", "", "", "", 200, "priority", "desc", 5, nil, nil)

	t.Run("search term changes the key", func(t *testing.T) {
		if got := inboxListKey("open", "", "timeout", "", "", "", 200, "priority", "desc", 5, nil, nil); got == base {
			t.Errorf("q=timeout produced the same key as an empty search: %q", got)
		}
	})

	t.Run("different terms never collide", func(t *testing.T) {
		seen := map[string]string{}
		for _, term := range []string{"", "timeout", "OOMKilled", "504", "time", "out"} {
			k := inboxListKey("open", "", term, "", "", "", 200, "priority", "desc", 5, nil, nil)
			if prev, dup := seen[k]; dup {
				t.Errorf("q=%q collides with q=%q on key %q", term, prev, k)
			}
			seen[k] = term
		}
	})

	t.Run("search is independent of the service filter", func(t *testing.T) {
		// `service` and `q` are separate inputs that AND together
		// server-side; swapping which one carries a value must not
		// produce the same key.
		a := inboxListKey("open", "api", "", "", "", "", 200, "priority", "desc", 5, nil, nil)
		b := inboxListKey("open", "", "api", "", "", "", 200, "priority", "desc", 5, nil, nil)
		if a == b {
			t.Errorf("service=api and q=api share a key %q — they filter different fields", a)
		}
	})

	t.Run("every other input still separates", func(t *testing.T) {
		seen := map[string]string{}
		for name, k := range map[string]string{
			"base":   base,
			"status": inboxListKey("all", "", "", "", "", "", 200, "priority", "desc", 5, nil, nil),
			"svc":    inboxListKey("open", "api", "", "", "", "", 200, "priority", "desc", 5, nil, nil),
			"owner":  inboxListKey("open", "", "", "team-a", "", "", 200, "priority", "desc", 5, nil, nil),
			"sre":    inboxListKey("open", "", "", "", "sre-a", "", 200, "priority", "desc", 5, nil, nil),
			"env":    inboxListKey("open", "", "", "", "", "prod", 200, "priority", "desc", 5, nil, nil),
			"limit":  inboxListKey("open", "", "", "", "", "", 50, "priority", "desc", 5, nil, nil),
			// v0.9.319 — sort joins the key. Two operators on the same
			// filters but different sorts must not share a cached page: the
			// server ranks BEFORE the cap, so the sort changes which rows
			// come back, not just their order.
			"sortCol": inboxListKey("open", "", "", "", "", "", 200, "lastSeen", "desc", 5, nil, nil),
			"sortDir": inboxListKey("open", "", "", "", "", "", 200, "priority", "asc", 5, nil, nil),
			// v0.9.320 — the occurrence floor changes WHICH rows come back.
			// "show all" (0) sharing a cached page with the default floor
			// would hand one operator the other's filtered queue.
			"minOcc0":  inboxListKey("open", "", "", "", "", "", 200, "priority", "desc", 0, nil, nil),
			"minOcc10": inboxListKey("open", "", "", "", "", "", 200, "priority", "desc", 10, nil, nil),
		} {
			if prev, dup := seen[k]; dup {
				t.Errorf("%s collides with %s on key %q", name, prev, k)
			}
			seen[k] = name
		}
	})
}
