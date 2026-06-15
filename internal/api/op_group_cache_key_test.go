package api

import "testing"

// group_id rel B — the /api/services/{name}/operations endpoint gained a
// `normalized` mode (group operations by op_group shape vs raw name). raw and
// normalized return DIFFERENT row sets for the same (service, window), so the
// serveCached key MUST hash `normalized` too. This is the v0.5.187 class of bug
// (a key that doesn't hash every response-changing input → cross-poisoning):
// if a future edit drops :norm= from the key, opening the normalized table
// would serve the cached raw-name result (or vice-versa) until TTL expiry.
//
// These pin: (a) flipping normalized changes the key, (b) everything else held
// equal the key is stable, (c) the window inputs still participate.

func TestSvcOpsCacheKey_NormalizedDistinct(t *testing.T) {
	raw := svcOpsCacheKey("billing", "1h", "", "", false)
	norm := svcOpsCacheKey("billing", "1h", "", "", true)
	if raw == norm {
		t.Fatalf("normalized not in cache key: raw and normalized collided on %q — raw/normalized cross-poisoning (v0.5.187 class)", raw)
	}
}

func TestSvcOpsCacheKey_Stable(t *testing.T) {
	a := svcOpsCacheKey("billing", "1h", "", "", true)
	b := svcOpsCacheKey("billing", "1h", "", "", true)
	if a != b {
		t.Fatalf("cache key unstable for identical inputs: %q != %q", a, b)
	}
}

func TestSvcOpsCacheKey_WindowInputsParticipate(t *testing.T) {
	// Every response-changing input must move the key. Hold normalized
	// constant and vary each window field in turn.
	base := svcOpsCacheKey("billing", "1h", "", "", true)
	cases := map[string]string{
		"service": svcOpsCacheKey("orders", "1h", "", "", true),
		"since":   svcOpsCacheKey("billing", "6h", "", "", true),
		"from":    svcOpsCacheKey("billing", "1h", "2026-06-15T00:00:00Z", "", true),
		"to":      svcOpsCacheKey("billing", "1h", "", "2026-06-15T01:00:00Z", true),
	}
	for field, got := range cases {
		if got == base {
			t.Errorf("%s does not participate in cache key — key unchanged when %s varied", field, field)
		}
	}
}
