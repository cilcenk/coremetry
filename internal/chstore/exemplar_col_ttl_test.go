// exemplar_col_ttl_test.go — v0.8.541.
//
// The spanmetrics MVs copy a trace_id into argMax states. Their ROW TTL
// outlives spans on purpose (spanmetrics_1m keeps 30d of aggregates against
// a 7d spans retention), which left the exemplars pointing at traces that
// had already aged out — a dead click from day 8. The fix expires the
// exemplar COLUMNS at the spans retention and leaves the row TTL alone, so
// the aggregate history survives.
//
// Verified on CH 24.8 before writing this: a row past the column TTL but
// inside the row TTL keeps countMerge/quantilesTDigestMerge and returns ''
// for the exemplar.
//
// These tests pin the statement shape. Two properties are load-bearing and
// neither is obvious from reading the SQL:
//
//   - the type MUST be echoed back — CH 24.8 rejects a type-less
//     `MODIFY COLUMN <c> TTL <expr>` with a syntax error;
//   - the target MUST be the inner table — a combined MV answers
//     "Engine MaterializedView doesn't support TTL clause".
package chstore

import (
	"strings"
	"testing"
)

func TestExemplarColTTLStmt(t *testing.T) {
	const (
		inner = ".inner_id.9876fca4-a6c7-4bda-99d4-10467c684017"
		typ   = "AggregateFunction(argMax, String, Int64)"
	)

	t.Run("single node", func(t *testing.T) {
		got := exemplarColTTLStmt(inner, "", "slow_exemplar_state", typ,
			"toDate(time_bucket) + INTERVAL 7 DAY")

		// The inner name starts with a dot and carries a uuid — unquoted it
		// is a parse error, so the backticks are part of the contract.
		if !strings.Contains(got, "ALTER TABLE `"+inner+"` MODIFY COLUMN") {
			t.Errorf("inner table must be backtick-quoted and directly ALTERed:\n%s", got)
		}
		if !strings.Contains(got, "`slow_exemplar_state` "+typ+" TTL ") {
			t.Errorf("type must be echoed between column and TTL (CH rejects type-less MODIFY COLUMN):\n%s", got)
		}
		// materialize_ttl_after_modify=0 keeps the ALTER metadata-only; at
		// billions of rows the synchronous re-evaluation would hang the
		// operator's PUT past the gateway timeout (the v0.8.x spans lesson).
		if !strings.Contains(got, "materialize_ttl_after_modify = 0") {
			t.Errorf("ALTER must stay metadata-only:\n%s", got)
		}
		if !strings.Contains(got, "alter_sync = 0") {
			t.Errorf("ALTER must not block on replica ack:\n%s", got)
		}
		if strings.Contains(got, "ON CLUSTER") {
			t.Errorf("single-node must not emit ON CLUSTER:\n%s", got)
		}
	})

	t.Run("cluster mode", func(t *testing.T) {
		got := exemplarColTTLStmt(inner, " ON CLUSTER `coremetry`", "error_exemplar_state",
			"AggregateFunction(argMaxIf, String, Int64, UInt8)",
			"toDate(time_bucket) + INTERVAL 7 DAY")

		// ON CLUSTER must land between the table and MODIFY COLUMN. The
		// inner name is uuid-based and identical on every shard (Atomic DB,
		// one uuid propagated by the ON CLUSTER create) — that is what makes
		// fanning this statement out valid at all.
		if !strings.Contains(got, "`"+inner+"` ON CLUSTER `coremetry` MODIFY COLUMN") {
			t.Errorf("ON CLUSTER must sit between table and MODIFY COLUMN:\n%s", got)
		}
	})

	// A literal ? would be eaten by clickhouse-go as a positional bind and
	// the ALTER would fail at runtime, far from here. Same pin as the
	// db_stmt_hash expression carries.
	t.Run("no bind placeholder", func(t *testing.T) {
		got := exemplarColTTLStmt(inner, "", "slow_exemplar_state", typ,
			"toDateTime(time_bucket) + INTERVAL 48 HOUR")
		if strings.Contains(got, "?") {
			t.Errorf("statement must not contain a bind placeholder:\n%s", got)
		}
	})
}

// The TTL rides buildRetentionTTL, so BOTH unit branches have to survive the
// trip. Hours vs days is not cosmetic here: hours needs toDateTime (a
// toDate() wrapper would floor a sub-day retention to midnight and expire
// exemplars early), days needs toDate. Every value+unit template in this
// repo has to prove both branches at ship time.
func TestExemplarColTTL_BothUnits(t *testing.T) {
	cases := []struct {
		name, retention, wantTTL string
	}{
		{"days", "7d", "toDate(time_bucket) + INTERVAL 7 DAY"},
		{"hours", "48h", "toDateTime(time_bucket) + INTERVAL 48 HOUR"},
		{"sub-day hours", "6h", "toDateTime(time_bucket) + INTERVAL 6 HOUR"},
		{"single day", "1d", "toDate(time_bucket) + INTERVAL 1 DAY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ttl, err := buildRetentionTTL(c.retention, "time_bucket")
			if err != nil {
				t.Fatalf("buildRetentionTTL(%q): %v", c.retention, err)
			}
			if ttl != c.wantTTL {
				t.Fatalf("TTL = %q, want %q", ttl, c.wantTTL)
			}
			got := exemplarColTTLStmt(".inner_id.x", "", "slow_exemplar_state",
				"AggregateFunction(argMax, String, Int64)", ttl)
			if !strings.Contains(got, "TTL "+c.wantTTL+" SETTINGS") {
				t.Errorf("TTL not threaded verbatim into the ALTER:\n%s", got)
			}
		})
	}
}

// The MV list is the whole blast radius; a typo here silently leaves a tier
// unprotected, and the failure only shows up as a dead link weeks later.
func TestExemplarStateMVs_MatchSpanmetricsTiers(t *testing.T) {
	for _, mv := range exemplarStateMVs {
		if !highVolumeTables[mv] {
			t.Errorf("%s is not in highVolumeTables — cluster mode would ALTER the "+
				"Distributed wrapper instead of the per-shard MV", mv)
		}
	}
	if len(exemplarStateCols) != 2 {
		t.Errorf("exemplarStateCols = %v, want the slow + error pair", exemplarStateCols)
	}
}
