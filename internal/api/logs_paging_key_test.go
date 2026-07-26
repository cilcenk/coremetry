package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.9.286 — WantCursor (the `paging=1` declaration) changes the
// RESPONSE: with it the payload carries a nextCursor, without it the
// field is empty. Two callers issuing the same filter — the interactive
// /logs list and a trace drawer — must therefore not share a cache
// entry, or whichever lands first decides whether the OTHER can page.
// The drawer winning means the list silently loses its Load-more.
//
// Same hash-ALL-inputs rule as v0.5.187 / v0.8.406.
func TestLogsSearchKey_CarriesWantCursor(t *testing.T) {
	key := func(paging bool) string {
		return logsSearchKey(logstore.Filter{
			Service: "mobile-bff", Limit: 100, WantCursor: paging,
		}, "1", "2")
	}
	if key(true) == key(false) {
		t.Fatal("paging intent on/off must produce distinct search keys — " +
			"otherwise a cursorless drawer response is served to the paging list")
	}
	if !strings.Contains(key(true), "pg=true") {
		t.Fatalf("key must carry the paging value; got %q", key(true))
	}
	if key(true) != key(true) {
		t.Fatal("logsSearchKey must be deterministic")
	}
}

// The declaration is derived from two inputs in getLogs: an explicit
// `paging` param, OR an `after` cursor already in hand. This pins the
// second half — a client on page 2 wants page 3, and must not lose
// paging just because it never sent the explicit flag.
func TestPagingIntentFromRequestShape(t *testing.T) {
	// Mirrors getLogs: WantCursor = parseBoolParam(paging) || after != ""
	intent := func(paging, after string) bool {
		return parseBoolParam(paging) || after != ""
	}
	cases := []struct {
		name          string
		paging, after string
		want          bool
	}{
		{"first page, interactive list declares it", "1", "", true},
		{"first page, drawer declares nothing", "", "", false},
		{"page 2 — the cursor itself is intent", "", "eyJwaXQiOiJ4In0", true},
		{"page 2 with the flag too", "true", "eyJwaXQiOiJ4In0", true},
		{"explicit false with no cursor stays false", "0", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := intent(tc.paging, tc.after); got != tc.want {
				t.Fatalf("intent(paging=%q, after=%q) = %v, want %v",
					tc.paging, tc.after, got, tc.want)
			}
		})
	}
}
