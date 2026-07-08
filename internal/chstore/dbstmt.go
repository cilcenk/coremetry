package chstore

// Persistent DB-statement identity — v0.8.375, Stage-2 slice D1
// (docs/pages-enhancement-audit.md §2 + Faz D, approved default:
// INGEST-TIME fingerprint).
//
// The /slow-queries catalog and the coming D2 statement-detail view need a
// stable identity for "one query class" that survives across windows, pages
// and pivots. Read-time regex normalization (slowQueriesGlobalSQL) can GROUP
// rows but produces no join key — nothing an exemplar lookup or a trend read
// can be keyed on. This file introduces that identity:
//
//	stmt_hash = xxHash64( normalize(db.statement) )
//
// where normalize() collapses literal-only differences (quoted strings and
// numeric literals → '?') so "WHERE id = 1" and "WHERE id = 2" share one
// hash, exactly like the read-time regex the catalog already groups by.
//
// THE PARITY CONTRACT (load-bearing — read before touching anything here):
// the hash is computed in TWO places that MUST agree byte-for-byte on the
// normalized form:
//
//  1. ClickHouse, at INSERT, via the spans.db_stmt_hash MATERIALIZED
//     expression (dbStmtHashExpr below) — two re2 replaceRegexpAll passes +
//     xxHash64, the same regexes slowQueriesGlobalSQL has used since
//     v0.5.165.
//  2. Go, on read paths, via NormalizeDBStatement/DBStmtHash — a single-pass
//     byte state machine that replicates the two-pass re2 semantics
//     (dbstmt_test.go pins vectors captured from a live CH 24.8, plus a
//     differential test against Go's regexp — which IS RE2, the same engine
//     ClickHouse embeds).
//
// CH xxHash64(s) == cespare/xxhash Sum64String(s) (both XXH64, seed 0 —
// the empty-string vector 0xEF46DB3751D8E999 is pinned in the tests), so
// equal normalized strings ⇒ equal hashes across the two engines.
//
// Why the ingest-side hash is CH-computed (MATERIALIZED) instead of the
// op_group/series_fingerprint explicit-INSERT shape: see the migration
// block in store.go — MATERIALIZED columns are never part of the INSERT
// projection and are ERASED when a Distributed wrapper forwards blocks
// (CH PR #7377), so the v0.8.185/186 wrapper/local-mismatch class cannot
// break ingest, and old pods keep producing correctly-hashed rows through
// the whole rolling-deploy window (the server computes the column no
// matter which binary inserted).

import (
	"fmt"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// dbStmtNormalizeCap bounds how many BYTES of a db.statement take part in
// normalization + hashing. ORM-generated bulk INSERTs can reach megabytes;
// hashing them whole would make the ingest expression (and every Go-side
// recompute) pay unbounded work for identity that the first 8 KiB already
// establishes. Statements sharing an 8 KiB prefix intentionally collapse
// into one class. MUST stay equal to the substring() length embedded in
// dbStmtHashExpr — pinned by TestDBStmtHashExprPinned (v0.8.375).
const dbStmtNormalizeCap = 8192

// dbStmtHashExpr is the ClickHouse expression behind the
// spans.db_stmt_hash MATERIALIZED column (store.go migration). Shape:
//
//	if(db_statement = '', 0, xxHash64(<normalize>))
//
// with <normalize> = the exact two regex passes slowQueriesGlobalSQL runs at
// read time — quoted string literals first, then numeric literals — over the
// first dbStmtNormalizeCap bytes. 0 is the "no statement" sentinel (matches
// the column's implicit default for non-DB spans; DBStmtHash mirrors it).
//
// char(63) is '?': a literal '?' anywhere in SQL text is the clickhouse-go
// positional-placeholder trap the read path dodges with the __P__ sentinel
// (see GetTopDBQueries) — in DDL we dodge it with the constant-folded
// char() call instead so the STORED string (and therefore the hash input)
// is the real display form. TestDBStmtHashExprPinned asserts the expression
// stays '?'-free.
var dbStmtHashExpr = fmt.Sprintf(
	`if(db_statement = '', 0, xxHash64(replaceRegexpAll(replaceRegexpAll(substring(db_statement, 1, %d), '''[^'']*''', char(63)), '\\b[0-9]+(\\.[0-9]+){0,1}\\b', char(63))))`,
	dbStmtNormalizeCap)

// isDBStmtWordByte reports RE2 \b word-class membership ([0-9A-Za-z_]) for
// one byte. RE2's \b is ASCII-defined, so byte-wise classification is exact
// even inside multi-byte UTF-8 sequences (every continuation byte is
// ≥ 0x80 → non-word → boundary), which is precisely why 'π123' normalizes
// to 'π?' while Arabic-Indic digits like '٣٤' pass through untouched.
func isDBStmtWordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}

// NormalizeDBStatement returns the canonical literal-normalized form of a
// db.statement: single-quoted string literals and standalone numeric
// literals become '?'. It is the Go twin of the CH normalization inside
// dbStmtHashExpr (see the parity contract at the top of this file) — a
// single O(n) pass, no regex, no allocations beyond the output buffer —
// replicating these re2 semantics:
//
//   - '[^']*'                — a quote pairs with the NEXT quote ("'a''b'" is
//     two adjacent literals → "??"; SQL's doubled-quote escaping is
//     deliberately not special-cased, matching the read-time regex). An
//     unterminated quote matches nothing: the quote stays and literals
//     inside the tail keep normalizing ("= 'abc AND b = 5" → "= 'abc AND
//     b = ?").
//   - \b[0-9]+(\.[0-9]+){0,1}\b — numbers replace only between word
//     boundaries ("col1", "0x1F" stay intact), one optional decimal point
//     ("3.14" → "?", "1.2.3" → "?.?"), with re2's leftmost-first fallback
//     when the fraction breaks the trailing boundary ("1.5abc" → "?.5abc").
//
// Boundary checks run against the ORIGINAL bytes; that is equivalent to the
// CH two-pass form because a replaced literal's neighborhood chars (' and ?)
// are both non-word — TestNormalizeDBStatementOracle proves the equivalence
// against the real RE2 engine over the pinned corpus plus generated inputs.
//
// Input is capped at dbStmtNormalizeCap bytes (byte-truncated, same as CH
// substring on String — both operate on bytes, so a mid-rune cut is still
// deterministic and hash-consistent).
func NormalizeDBStatement(stmt string) string {
	if stmt == "" {
		return ""
	}
	if len(stmt) > dbStmtNormalizeCap {
		stmt = stmt[:dbStmtNormalizeCap]
	}
	var out strings.Builder
	out.Grow(len(stmt))
	n := len(stmt)
	for i := 0; i < n; {
		c := stmt[i]

		// Quoted string literal: pair with the next quote.
		if c == '\'' {
			if j := strings.IndexByte(stmt[i+1:], '\''); j >= 0 {
				out.WriteByte('?')
				i += j + 2
				continue
			}
			// Unterminated — the quote is a plain character.
			out.WriteByte('\'')
			i++
			continue
		}

		// Numeric literal candidate: ASCII digit at a word boundary.
		if c >= '0' && c <= '9' && (i == 0 || !isDBStmtWordByte(stmt[i-1])) {
			d := i
			for d < n && stmt[d] >= '0' && stmt[d] <= '9' {
				d++
			}
			// Greedy optional fraction, exactly one '.' + digits.
			f := -1
			if d+1 < n && stmt[d] == '.' && stmt[d+1] >= '0' && stmt[d+1] <= '9' {
				f = d + 1
				for f < n && stmt[f] >= '0' && stmt[f] <= '9' {
					f++
				}
			}
			switch {
			case f >= 0 && (f == n || !isDBStmtWordByte(stmt[f])):
				// Fraction taken, trailing \b holds.
				out.WriteByte('?')
				i = f
			case d == n || !isDBStmtWordByte(stmt[d]):
				// re2 leftmost-first fallback: drop the fraction,
				// match the integer part alone ("1.5abc" → "?.5abc").
				out.WriteByte('?')
				i = d
			default:
				// No boundary after the digit run ("0x1F", "12ab") —
				// no match anywhere inside the run either (every inner
				// position is digit-preceded). Emit raw, move on.
				out.WriteString(stmt[i:d])
				i = d
			}
			continue
		}

		out.WriteByte(c)
		i++
	}
	return out.String()
}

// DBStmtHash returns the persistent statement identity: xxHash64 of the
// normalized statement, 0 for "no statement" — the exact value the
// spans.db_stmt_hash MATERIALIZED column stores for the same input
// (parity contract above). Read paths use it to key raw-scan rows with
// the same identity the MV rows carry (GetSlowQueriesGlobal), and D2
// will use it to resolve a catalog row back to raw spans.
func DBStmtHash(stmt string) uint64 {
	if stmt == "" {
		return 0
	}
	return xxhash.Sum64String(NormalizeDBStatement(stmt))
}
