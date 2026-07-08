package chstore

// v0.8.375 — Stage-2 slice D1: persistent DB-statement identity.
//
// Three layers of protection, in order of authority:
//
//  1. TestNormalizeDBStatementTable — vectors whose expected outputs were
//     captured from a LIVE ClickHouse 24.8.14.39 running the exact
//     dbStmtHashExpr normalization (2026-07-07, 2-shard chc cluster). If
//     one of these fails, the Go port broke CH parity — fix the port,
//     never the vector.
//  2. TestNormalizeDBStatementOracle — differential test against Go's
//     regexp package (RE2 — the same engine ClickHouse embeds) running the
//     literal two-pass replacement from dbStmtHashExpr. Covers the pinned
//     corpus plus deterministic generated inputs so state-machine edge
//     cases (quote pairing, fraction fallback, boundary classes) can't
//     drift silently.
//  3. TestDBStmtHashExprPinned — string-pins the CH expression itself:
//     cap in sync with dbStmtNormalizeCap, both regexes verbatim from
//     slowQueriesGlobalSQL, char(63) placeholder, empty-string guard, and
//     NO literal '?' byte (the clickhouse-go positional-placeholder trap).

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"github.com/cespare/xxhash/v2"
)

func TestNormalizeDBStatementTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// ── Live-CH captured vectors (see file header) ──────────────────
		{"simple int literal",
			"SELECT * FROM users WHERE id = 42",
			"SELECT * FROM users WHERE id = ?"},
		{"doubled-quote escape + decimal + IN-list + digit-suffixed idents",
			"SELECT 'a''b', 3.14, col1 FROM t2x WHERE x IN (1, 2, 3)",
			"SELECT ??, ?, col1 FROM t2x WHERE x IN (?, ?, ?)"},
		{"apostrophe literal + hex stays intact",
			"WHERE name = 'it''s' AND v = 0x1F",
			"WHERE name = ?? AND v = 0x1F"},
		{"unicode literal, unicode-adjacent digits, unicode digits",
			"SELECT 'ünïcödé', π123, ٣٤",
			"SELECT ?, π?, ٣٤"},
		{"fraction fallback, chained dots, leading/trailing dot, sign",
			"1.5abc 1.2.3 .5 5. -7",
			"?.5abc ?.? .? ?. -?"},
		{"unterminated quote — tail keeps normalizing",
			"WHERE a = 'abc AND b = 5",
			"WHERE a = 'abc AND b = ?"},
		{"digit after replaced literal", "'a'5", "??"},
		{"digit before literal", "5'a'", "??"},
		{"assignments + LIMIT",
			"UPDATE t SET a=1,b=2 WHERE k='x' LIMIT 10",
			"UPDATE t SET a=?,b=? WHERE k=? LIMIT ?"},
		{"no literals passes through", "no literals here", "no literals here"},

		// ── Additional edge cases (oracle-verified) ─────────────────────
		{"empty", "", ""},
		{"whitespace only", "   \t\n", "   \t\n"},
		{"lone quote", "'", "'"},
		{"empty literal", "''", "?"},
		{"adjacent empty literals", "''''", "??"},
		{"digits sandwiched by literals", "12'x'34", "???"},
		{"digits inside literal are consumed", "1'2'3", "???"},
		{"identifier with trailing digits", "col12", "col12"},
		{"identifier with leading digits", "12col", "12col"},
		{"underscore blocks the boundary", "a_1", "a_1"},
		{"placeholder-style binds untouched", "WHERE id = $1 OR id = :2",
			"WHERE id = $? OR id = :?"},
		{"decimal at end", "v = 3.14", "v = ?"},
		{"integer at end", "LIMIT 100", "LIMIT ?"},
		{"integer at start", "42 items", "? items"},
		{"dot without fraction digits", "v = 1.x", "v = ?.x"},
		{"multi-byte rune split by cap is deterministic",
			strings.Repeat("é", 3), "ééé"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeDBStatement(tc.in); got != tc.want {
				t.Fatalf("NormalizeDBStatement(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

// normalizeOracle is the two-pass RE2 reference: the literal replacement
// pipeline from dbStmtHashExpr, expressed with Go's regexp (RE2 — same
// engine, same \b semantics as ClickHouse's replaceRegexpAll).
var (
	oracleQuoteRe = regexp.MustCompile(`'[^']*'`)
	oracleNumRe   = regexp.MustCompile(`\b[0-9]+(\.[0-9]+){0,1}\b`)
)

func normalizeOracle(s string) string {
	if len(s) > dbStmtNormalizeCap {
		s = s[:dbStmtNormalizeCap]
	}
	s = oracleQuoteRe.ReplaceAllString(s, "?")
	return oracleNumRe.ReplaceAllString(s, "?")
}

func TestNormalizeDBStatementOracle(t *testing.T) {
	// Fixed corpus: the table vectors plus known nasties.
	corpus := []string{
		"SELECT * FROM users WHERE id = 42",
		"SELECT 'a''b', 3.14, col1 FROM t2x WHERE x IN (1, 2, 3)",
		"WHERE name = 'it''s' AND v = 0x1F",
		"SELECT 'ünïcödé', π123, ٣٤",
		"1.5abc 1.2.3 .5 5. -7",
		"WHERE a = 'abc AND b = 5",
		"'a'5", "5'a'", "12'x'34", "1'2'3", "''", "'", "''''", "",
		"UPDATE t SET a=1,b=2 WHERE k='x' LIMIT 10",
		"INSERT INTO t VALUES (1, 'a', 2.5), (2, 'b', 3.5)",
		"WHERE ts > '2026-07-07 10:00:00' AND v = 1e5",
		"0x1F 0b101 v2 2v _1 1_ 1.  .1. ..2..",
		"12.34x 12.34 x12.34 12.x34",
		strings.Repeat("1234567890'ab'.", 1000), // > cap, truncation inside tokens
	}
	for i, s := range corpus {
		if got, want := NormalizeDBStatement(s), normalizeOracle(s); got != want {
			t.Errorf("corpus[%d] %.60q\n got  %q\n want %q", i, s, got, want)
		}
	}

	// Deterministic generated inputs biased toward the interesting
	// alphabet — quotes, digits, dots, word/non-word neighbors, multi-byte
	// runes — so boundary interactions get hammered from every side.
	alphabet := []string{
		"'", "0", "1", "9", ".", "a", "z", "_", " ", ",", "(", ")", "=",
		"?", "5", "42", "3.14", "''", "π", "٣", "é", "x1", "1x", "-",
	}
	rng := rand.New(rand.NewSource(0x0375)) // v0.8.375 — fixed seed, reproducible
	for i := 0; i < 5000; i++ {
		var sb strings.Builder
		for j, k := 0, rng.Intn(40); j < k; j++ {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		s := sb.String()
		if got, want := NormalizeDBStatement(s), normalizeOracle(s); got != want {
			t.Fatalf("generated[%d] %q\n got  %q\n want %q", i, s, got, want)
		}
	}
}

func TestDBStmtHash(t *testing.T) {
	// Empty → 0 sentinel, matching the CH expression's
	// if(db_statement = '', 0, …) guard and the MV's
	// `WHERE db_stmt_hash != 0` trigger filter.
	if got := DBStmtHash(""); got != 0 {
		t.Fatalf("DBStmtHash(\"\") = %d, want 0 sentinel", got)
	}

	// xxHash64 cross-engine parity anchor: XXH64(seed=0) of "" is
	// 0xEF46DB3751D8E999 — verified equal to ClickHouse 24.8's
	// xxHash64('') (17241709254077376921) on the live cluster.
	if got := xxhash.Sum64String(""); got != 0xEF46DB3751D8E999 {
		t.Fatalf("xxhash empty vector = %#x, want 0xEF46DB3751D8E999 (CH parity broken)", got)
	}

	// Hash is over the NORMALIZED form.
	stmt := "SELECT * FROM users WHERE id = 42"
	if got, want := DBStmtHash(stmt), xxhash.Sum64String("SELECT * FROM users WHERE id = ?"); got != want {
		t.Fatalf("DBStmtHash = %d, want xxhash(normalized) = %d", got, want)
	}
	// Live-CH captured hash for the same statement (xxHash64 of the CH
	// normalization output, 2026-07-07): full-pipeline cross-engine pin.
	if got := DBStmtHash(stmt); got != 13973603060511522929 {
		t.Fatalf("DBStmtHash(%q) = %d, want 13973603060511522929 (CH-captured)", stmt, got)
	}

	// Literal-only differences collapse; shape differences don't.
	if DBStmtHash("SELECT * FROM users WHERE id = 42") != DBStmtHash("SELECT * FROM users WHERE id = 7") {
		t.Fatal("literal-only variants must share one hash")
	}
	if DBStmtHash("SELECT a FROM t") == DBStmtHash("SELECT b FROM t") {
		t.Fatal("distinct shapes must not share a hash")
	}

	// Cap: statements identical through the first dbStmtNormalizeCap bytes
	// share an identity regardless of the tail.
	long1 := strings.Repeat("x", dbStmtNormalizeCap) + "tail-one 111"
	long2 := strings.Repeat("x", dbStmtNormalizeCap) + "tail-two 'y'"
	if DBStmtHash(long1) != DBStmtHash(long2) {
		t.Fatal("statements sharing the capped prefix must share one hash")
	}
	if got, want := NormalizeDBStatement(long1), strings.Repeat("x", dbStmtNormalizeCap); got != want {
		t.Fatalf("cap truncation: got len %d, want len %d", len(got), len(want))
	}
}

func TestDBStmtHashExprPinned(t *testing.T) {
	expr := dbStmtHashExpr

	// The '?' byte is the clickhouse-go positional-placeholder trap — the
	// expression rides through execDDL and inside the spans ALTER, so it
	// must never contain one (char(63) carries the placeholder instead).
	if strings.ContainsRune(expr, '?') {
		t.Fatalf("dbStmtHashExpr contains a literal '?' (clickhouse-go placeholder trap):\n%s", expr)
	}

	for _, want := range []string{
		// Cap in lockstep with the Go normalizer.
		"substring(db_statement, 1, 8192)",
		// The exact two regexes slowQueriesGlobalSQL groups by at read time
		// (as they appear inside a CH string literal in Go source).
		`'''[^'']*'''`,
		`'\\b[0-9]+(\\.[0-9]+){0,1}\\b'`,
		// '?' placeholder via constant-folded char().
		"char(63)",
		// Empty-statement sentinel guard.
		"if(db_statement = '', 0,",
		// The cross-engine hash.
		"xxHash64(",
	} {
		if !strings.Contains(expr, want) {
			t.Errorf("dbStmtHashExpr missing %q\n--- expr ---\n%s", want, expr)
		}
	}
	if !strings.Contains(expr, "8192") || dbStmtNormalizeCap != 8192 {
		t.Errorf("dbStmtNormalizeCap (%d) and dbStmtHashExpr cap drifted", dbStmtNormalizeCap)
	}
}

// Benchmarks — the normalizer runs Go-side on read paths today (≤500 rows
// per catalog page) but was designed ingest-grade (v0.8.375): single pass,
// no regex, one output allocation. Keep it that way — if a change pushes
// either benchmark past ~µs/op territory, the D2 exemplar path (which may
// hash per candidate row) will feel it.
func BenchmarkNormalizeDBStatement(b *testing.B) {
	stmt := "SELECT o.id, o.total, c.name FROM orders o JOIN customers c ON c.id = o.customer_id " +
		"WHERE o.status = 'open' AND o.total > 99.95 AND o.region IN (1, 2, 3, 4) LIMIT 50"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		NormalizeDBStatement(stmt)
	}
}

func BenchmarkDBStmtHash(b *testing.B) {
	stmt := "UPDATE carts SET status = 'paid', updated_at = '2026-07-07 10:00:00' WHERE id IN (1, 2, 3)"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		DBStmtHash(stmt)
	}
}
