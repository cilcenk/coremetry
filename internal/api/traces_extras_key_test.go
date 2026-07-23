package api

import (
	"testing"
	"time"
)

// FAZ 2 (docs/audit/traces-attribute-columns.md §6) — cache-key discipline
// for the /api/traces?traceIds= phase-2 path, per the v0.5.187 rule: the id
// set folds in via a sorted FNV digest, NEVER a length-only summary. Two
// different sets of equal size must produce different keys; the same set in
// any order must produce the same key; every other input (attrs, from, to)
// must move the key too.
func TestTracesExtrasKey_HashesAllInputs(t *testing.T) {
	idsA := []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	idsB := []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "cccccccccccccccccccccccccccccccc"}
	attrs := []string{"CHANNEL_CODE", "FUNCTION_CODE"}

	base := tracesExtrasKey(idsA, attrs, "1", "2")

	// v0.5.187 class: same length, different membership → MUST differ.
	if got := tracesExtrasKey(idsB, attrs, "1", "2"); got == base {
		t.Fatalf("length-only collision: %q == %q for different id sets", got, base)
	}
	// Order-insensitive: the digest sorts, so a reshuffled set is a HIT.
	if got := tracesExtrasKey([]string{idsA[1], idsA[0]}, attrs, "1", "2"); got != base {
		t.Fatalf("id order changed the key: %q != %q", got, base)
	}
	// Attr set and window both ride the key.
	if got := tracesExtrasKey(idsA, []string{"CHANNEL_CODE"}, "1", "2"); got == base {
		t.Fatalf("attr set not in key")
	}
	if got := tracesExtrasKey(idsA, attrs, "1", "3"); got == base {
		t.Fatalf("time window not in key")
	}
}

func TestIsTraceIDHex(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"0123456789abcdef0123456789abcdef", true},
		{"0123456789ABCDEF0123456789ABCDEF", false}, // caller lowercases first
		{"0123456789abcdef0123456789abcde", false},  // 31 chars
		{"0123456789abcdef0123456789abcdeg", false}, // non-hex
		{"", false},
	}
	for _, c := range cases {
		if got := isTraceIDHex(c.in); got != c.ok {
			t.Fatalf("isTraceIDHex(%q) = %v, want %v", c.in, got, c.ok)
		}
	}
}

// v0.9.195 review-fix — pencere clampi: elle yazılmış from=0 retention-genişliği
// taramasına dönemez; meşru (35g altı) pencereler dokunulmadan geçer.
func TestClampExtrasFrom(t *testing.T) {
	to := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	legit := to.Add(-6 * 24 * time.Hour)
	if got := clampExtrasFrom(legit, to); !got.Equal(legit) {
		t.Fatalf("legit 6d window clamped: %v", got)
	}
	if got := clampExtrasFrom(time.Unix(0, 0), to); !got.Equal(to.Add(-35 * 24 * time.Hour)) {
		t.Fatalf("from=0 must clamp to to-35d, got %v", got)
	}
}
