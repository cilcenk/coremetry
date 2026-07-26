package chstore

import (
	"encoding/base64"
	"strings"
	"testing"
)

// v0.7.22 (SAFE-CORE) — regression guard for the log keyset-paging
// hardening, extended by v0.7.23 (SAFE-CORE) for the row-drop fix.
//
// History:
//
//  1. Pre-v0.7.22: GetLogs paged with `ORDER BY time DESC LIMIT ?
//     OFFSET ?` and surfaced an unstable `rowNumberInAllBlocks()` id.
//     No tiebreak + block-order-dependent id → boundary dup/drop.
//  2. v0.7.22: ORDER BY time DESC, span_id DESC + a STRICT keyset
//     cursor base64("ch|"+timeNs+"|"+spanId). This still dropped rows:
//     span_id is String DEFAULT '' and most log lines are emitted
//     OUTSIDE a span (span_id=''). (time, span_id) is therefore NOT a
//     total order — a page boundary inside a run of (t0,'') rows
//     dropped every remaining (t0,'') row, because `time = t0 AND
//     span_id < ''` matches nothing.
//  3. v0.7.23: tiebreak is now a deterministic query-time row hash
//     (logsRowKeyExpr = cityHash64 over the line's identifying
//     columns). ORDER BY time DESC, <rowKey> DESC + a STRICT keyset
//     base64("ch|"+timeNs+"|"+rowKey uint64). (time, rowKey) is a
//     provable total order, so no boundary drop/dup.
//
// These tests pin:
//   - encode/decode roundtrip of (timeNs, rowKey uint64), incl. the
//     "ch|" backend tag
//   - malformed / wrong-backend / empty / non-numeric tokens decode
//     to ok=false
//   - the keyset predicate is strict-less on BOTH legs over the
//     (time, rowKey) total order — same-time rows differ only by
//     rowKey, so none are skippable (the v0.7.23 fix)
//   - empty-cursor first-page read applies NO keyset
//   - last-page short read yields an empty NextCursor
func TestLogsCursorRoundtrip(t *testing.T) {
	cases := []struct {
		name   string
		timeNs int64
		rowKey uint64
	}{
		{"typical hash", 1717200000123456789, 0x0a1b2c3d4e5f6071},
		{"zero time", 0, 0xabcdef0123456789},
		{"zero rowkey (outside-span line)", 1717200000000000000, 0},
		{"max ns + max rowkey", 9223372036854775807, 18446744073709551615},
		{"small both", 42, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := EncodeLogsCursor(tc.timeNs, tc.rowKey, false)
			if tok == "" {
				t.Fatalf("EncodeLogsCursor returned empty token")
			}
			got, ok := DecodeLogsCursor(tok)
			if !ok {
				t.Fatalf("DecodeLogsCursor(%q) ok=false, want true", tok)
			}
			if got.TimeNs != tc.timeNs {
				t.Errorf("TimeNs = %d, want %d", got.TimeNs, tc.timeNs)
			}
			if got.RowKey != tc.rowKey {
				t.Errorf("RowKey = %d, want %d", got.RowKey, tc.rowKey)
			}
		})
	}
}

func TestDecodeLogsCursorRejects(t *testing.T) {
	cases := []struct {
		name string
		tok  string
	}{
		{"empty", ""},
		{"not base64", "!!!not base64!!!"},
		{"wrong backend tag", b64("es|123|456")},
		{"too few parts", b64("ch|123")},
		{"non-numeric ns", b64("ch|notanumber|456")},
		{"non-numeric rowkey", b64("ch|123|notahash")},
		{"negative rowkey (not uint64)", b64("ch|123|-1")},
		{"no separators", b64("garbage")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := DecodeLogsCursor(tc.tok); ok {
				t.Errorf("DecodeLogsCursor(%q) ok=true, want false", tc.tok)
			}
		})
	}
}

func TestLogsKeysetPredicate(t *testing.T) {
	// First page — no cursor — must apply NO keyset (Offset path).
	sql, args := logsKeysetPredicate(LogsCursor{}, false)
	if sql != "" || args != nil {
		t.Fatalf("no-cursor case: got sql=%q args=%v, want empty", sql, args)
	}

	// With a cursor, the predicate must be strict-less on BOTH legs
	// over the (time, rowKey) TOTAL order:
	//   ts < t  OR  (ts = t AND <rowKeyExpr> < h)
	// Strict on both legs over a total order means the boundary row is
	// neither re-returned (dup) nor skipped (drop). Crucially, the
	// same-time leg compares the deterministic row hash, NOT span_id —
	// so a run of same-time outside-a-span rows (the v0.7.23 row-drop
	// bug) is fully ordered by rowKey and none collapse out.
	//
	// v0.7.80 regression guard: the time legs MUST bind the exact
	// nanosecond as an Int64 via toUnixTimestamp64Nano, NOT a bare
	// time.Time. clickhouse-go/v2 formats a positional time.Time at
	// SECONDS scale, so a bare `time = ?` on the DateTime64(9) column
	// matched nothing and `time < ?` dropped every same-second row on
	// the next page. Pin both the toUnixTimestamp64Nano SQL and the
	// int64 (not time.Time) arg types so the truncation can't return.
	const ns = int64(1717200000123456789)
	const rk = uint64(0x0a1b2c3d4e5f6071)
	c := LogsCursor{TimeNs: ns, RowKey: rk}
	sql, args = logsKeysetPredicate(c, true)
	wantSQL := "(toUnixTimestamp64Nano(time) < ? OR (toUnixTimestamp64Nano(time) = ? AND " + logsRowKeyExpr + " < ?))"
	if sql != wantSQL {
		t.Fatalf("predicate sql = %q, want %q", sql, wantSQL)
	}
	if len(args) != 3 {
		t.Fatalf("predicate args len = %d, want 3", len(args))
	}
	if got, ok := args[0].(int64); !ok || got != ns {
		t.Errorf("args[0] = %v (%T), want int64 ns %d — bare time.Time truncates to seconds (v0.7.80)", args[0], args[0], ns)
	}
	if got, ok := args[1].(int64); !ok || got != ns {
		t.Errorf("args[1] = %v (%T), want int64 ns %d — bare time.Time truncates to seconds (v0.7.80)", args[1], args[1], ns)
	}
	if h, ok := args[2].(uint64); !ok || h != rk {
		t.Errorf("args[2] = %v, want rowKey %d", args[2], rk)
	}

	// The same-time leg must compare at ns precision and reference the
	// SAME hash expression used by the SELECT projection + ORDER BY. If
	// they ever drift the total-order proof breaks, so pin both.
	if !containsSub(sql, "toUnixTimestamp64Nano(time)") {
		t.Errorf("predicate %q must compare toUnixTimestamp64Nano(time), not a truncating time.Time bind", sql)
	}
	if !containsSub(sql, logsRowKeyExpr) {
		t.Errorf("predicate %q does not embed logsRowKeyExpr %q", sql, logsRowKeyExpr)
	}
	if containsSub(sql, "span_id <") {
		t.Errorf("predicate %q still uses the legacy span_id tiebreak", sql)
	}
}

// TestLogsCursorLastPageContract documents the NextCursor emission
// rule at the boundary: a full page (len == limit) yields a non-empty
// cursor; a short page (the last page) must yield empty so the UI
// stops paging. This pins the helper contract the GetLogs loop
// depends on (the loop itself needs a live CH conn, so we assert the
// pure rule here).
func TestLogsCursorLastPageContract(t *testing.T) {
	limit := 100
	// Simulate "full page": last row -> non-empty cursor.
	full := nextCursorFor(limit, limit, 1717200000000000000, 0xdeadbeef)
	if full == "" {
		t.Errorf("full page (len==limit) must produce a non-empty NextCursor")
	}
	// Simulate "short page": last page -> empty cursor.
	short := nextCursorFor(limit-1, limit, 1717200000000000000, 0xdeadbeef)
	if short != "" {
		t.Errorf("short page (len<limit) must produce an empty NextCursor, got %q", short)
	}
	// Round-trip the full-page cursor to confirm it decodes.
	if _, ok := DecodeLogsCursor(full); !ok {
		t.Errorf("full-page cursor %q failed to decode", full)
	}
}

// nextCursorFor mirrors the GetLogs NextCursor decision so the rule
// is testable without a CH connection.
func nextCursorFor(rowsLen, limit int, lastTimeNs int64, lastRowKey uint64) string {
	if rowsLen == limit {
		return EncodeLogsCursor(lastTimeNs, lastRowKey, false)
	}
	return ""
}

// b64 mirrors EncodeLogsCursor's transport (base64 RawURL) without
// the "ch|" tagging, so we can hand-craft malformed-but-decodable
// payloads for the reject table above.
func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// containsSub is a tiny substring helper kept local to the test so we
// don't pull strings into the assertion surface area.
func containsSub(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// v0.9.295 — the cursor carries the DIRECTION it was minted in.
//
// Before, direction was simply ignored while a cursor was present
// (`f.Ascending && !hasCursor`), so the list had no way to offer
// oldest-first at all. Honouring it naively would have been worse: the
// keyset is a STRICT inequality, so replaying a newest-first position
// while paging oldest-first walks away from the rows the operator is
// reading and returns a plausible, wrong slice — the silent-wrong class
// this codebase keeps paying for.
func TestLogsCursorCarriesDirection(t *testing.T) {
	for _, asc := range []bool{false, true} {
		tok := EncodeLogsCursor(1700000000123456789, 42, asc)
		got, ok := DecodeLogsCursor(tok)
		if !ok {
			t.Fatalf("asc=%v: token did not round-trip", asc)
		}
		if got.Ascending != asc {
			t.Fatalf("asc=%v: decoded Ascending = %v", asc, got.Ascending)
		}
		if got.TimeNs != 1700000000123456789 || got.RowKey != 42 {
			t.Fatalf("asc=%v: position corrupted: %+v", asc, got)
		}
	}
	// The two directions must not produce the same token, or the
	// mismatch check upstream has nothing to detect.
	if EncodeLogsCursor(1, 2, false) == EncodeLogsCursor(1, 2, true) {
		t.Fatal("direction must change the token")
	}
}

// Tokens minted before v0.9.295 have no direction field and MUST keep
// decoding as what they were — newest-first. A bookmarked link or an
// in-flight page from the previous build must not start paging backwards.
func TestLogsCursorBackCompatDefaultsToDescending(t *testing.T) {
	legacy := base64.RawURLEncoding.EncodeToString([]byte("ch|1700000000123456789|42"))
	got, ok := DecodeLogsCursor(legacy)
	if !ok {
		t.Fatal("a pre-v0.9.295 token must still decode")
	}
	if got.Ascending {
		t.Fatal("a token without a direction field is newest-first, not oldest-first")
	}
}

// An unknown 4th field is a token from some other encoder. Rejecting it
// costs a first-page read; guessing its meaning costs a wrong page.
func TestLogsCursorRejectsUnknownDirectionFlag(t *testing.T) {
	for _, bad := range []string{"ch|1|2|z", "ch|1|2|", "ch|1|2|a|b"} {
		tok := base64.RawURLEncoding.EncodeToString([]byte(bad))
		if _, ok := DecodeLogsCursor(tok); ok {
			t.Fatalf("%q must not decode", bad)
		}
	}
}

// The keyset comparison flips wholesale — BOTH legs. Flipping only the
// time leg would re-return or skip the boundary row whenever a run of
// rows shares one nanosecond, which is exactly what the rowKey leg
// exists to prevent (v0.7.23).
func TestLogsKeysetPredicateFlipsBothLegs(t *testing.T) {
	desc, dArgs := logsKeysetPredicate(LogsCursor{TimeNs: 7, RowKey: 9}, true)
	asc, aArgs := logsKeysetPredicate(LogsCursor{TimeNs: 7, RowKey: 9, Ascending: true}, true)

	if strings.Count(desc, "<") != 2 {
		t.Fatalf("newest-first must compare strictly less on both legs: %q", desc)
	}
	if strings.Count(asc, ">") != 2 {
		t.Fatalf("oldest-first must compare strictly greater on both legs: %q", asc)
	}
	if strings.Contains(asc, "<") {
		t.Fatalf("oldest-first predicate still carries a descending comparison: %q", asc)
	}
	// Only the comparison changes; the bound values are the same position.
	if len(dArgs) != len(aArgs) {
		t.Fatalf("arg count diverged: %d vs %d", len(dArgs), len(aArgs))
	}
	for i := range dArgs {
		if dArgs[i] != aArgs[i] {
			t.Fatalf("arg %d diverged: %v vs %v", i, dArgs[i], aArgs[i])
		}
	}
	// And no cursor still means no predicate, in either direction.
	if sql, _ := logsKeysetPredicate(LogsCursor{Ascending: true}, false); sql != "" {
		t.Fatalf("first page must apply no keyset, got %q", sql)
	}
}
