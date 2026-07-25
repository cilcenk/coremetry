package api

import (
	"strings"
	"testing"
)

// v0.9.219 — the sidebar triage badge ignored ?env= while the inbox LIST
// honoured it, so with an environment picked the badge read the global total
// and the page it linked to read a subset: 47 in the sidebar, 12 on the page.
//
// Fixing the filter without fixing the key would have been worse than the
// bug: an env-scoped count and the global one would have shared a cache entry
// inside the 15s TTL (the v0.5.187 class). The warm loop keys through the
// same helper so the pre-warmed payload can never land on a key the handler
// doesn't read.

func TestInboxCountKey_SeparatesEnvs(t *testing.T) {
	global := inboxCountKey("")
	if inboxCountKey("uat") == global {
		t.Fatal("an env-scoped badge must not share the global key")
	}
	if inboxCountKey("uat") == inboxCountKey("prep") {
		t.Fatal("two envs must not share a key")
	}
	if !strings.Contains(inboxCountKey("uat"), "uat") {
		t.Fatalf("key must carry the env value; got %q", inboxCountKey("uat"))
	}
}

// cacheInvalidatePrefix("inbox:count") is what a problem/exception mutation
// calls for read-your-writes. Every env variant must sit under that prefix or
// a stale env-scoped badge would survive the invalidation.
func TestInboxCountKey_StaysUnderInvalidationPrefix(t *testing.T) {
	for _, env := range []string{"", "uat", "prep", "prod-eu"} {
		if got := inboxCountKey(env); !strings.HasPrefix(got, "inbox:count") {
			t.Fatalf("env %q produced %q, which the invalidation prefix would miss", env, got)
		}
	}
}
