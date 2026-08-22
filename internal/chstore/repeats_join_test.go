package chstore

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// v0.9.1285 — Explore's "Repeats" mode (the N+1 finder) returned
// HTTP 500 on EVERY window against a Distributed ClickHouse.
// Reproduced live on the local 2-shard cluster before the fix:
// GET /api/spans/repeats?groupBy=db.statement&minRepeats=5&since=10m
// answered 500 "code 159, Timeout exceeded: elapsed 27.9s,
// maximum: 20". Two defects sat on one join.
//
// FIRST, the time bounds sat in the JOIN ON clause — the old line
// read `LEFT JOIN spans s ON s.trace_id = d.trace_id AND
// s.time >= ? AND s.time <= ?`. ON-clause predicates on the right
// table are join conditions, not scan filters, so nothing pruned
// and the join scanned the WHOLE spans table: query_log recorded
// 22.69M read_rows / 1.58 GiB / 20.5s for a 10-minute question,
// against 23.99M rows in the table. With the bound moved inside a
// subquery: 75.65K rows / 11.34 MiB / 0.056s median.
//
// SECOND, there was no GLOBAL prefix. `spans` is
// `Distributed(..., spans_local, rand())`, so one trace's spans
// scatter across shards and a shard-local join resolves each trace
// against a partial slice.
//
// The fix is BOTH halves — either half alone still ships a bug —
// so the test pins both.
func TestRepeatedSpansSQLJoinsGlobalAndBounded(t *testing.T) {
	// The group-key projection and the inner WHERE both vary with
	// operator input (well-known column vs. attr-array lookup vs.
	// several keys, with and without extra filters). The join shape
	// must not depend on either.
	cases := []struct {
		name      string
		keysArray string
		whereSQL  string
	}{
		{
			name:      "default db.statement (well-known column, no bind arg)",
			keysArray: "[toString(db_statement)]",
			whereSQL:  "WHERE time >= ? AND time <= ?",
		},
		{
			name:      "attr-array key (carries a bind arg)",
			keysArray: "[toString(attr_values[indexOf(attr_keys, ?)])]",
			whereSQL:  "WHERE time >= ? AND time <= ?",
		},
		{
			name:      "resource key (carries a bind arg)",
			keysArray: "[toString(res_values[indexOf(res_keys, ?)])]",
			whereSQL:  "WHERE time >= ? AND time <= ?",
		},
		{
			name:      "multi-key group-by",
			keysArray: "[toString(name), toString(peer_service)]",
			whereSQL:  "WHERE time >= ? AND time <= ?",
		},
		{
			name:      "with operator filters appended",
			keysArray: "[toString(db_statement)]",
			whereSQL:  "WHERE time >= ? AND time <= ? AND service_name = ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := repeatedSpansSQL(tc.keysArray, tc.whereSQL)

			// (2) GLOBAL — the shard-scatter half.
			if !strings.Contains(sql, "GLOBAL LEFT JOIN") {
				t.Errorf("join lost its GLOBAL prefix; on a rand()-sharded "+
					"Distributed `spans` each shard would join only its own "+
					"slice of the trace\n--- SQL ---\n%s", sql)
			}

			// (1) The bound must be a scan filter inside the joined
			// subquery, never an ON-clause predicate. Both spellings of
			// the old form are rejected: an alias-qualified bound on the
			// right table is the ON-clause shape by construction.
			for _, bad := range []string{"s.time >=", "s.time <=", "JOIN spans s ", "JOIN spans "} {
				if strings.Contains(sql, bad) {
					t.Errorf("query reintroduces the unbounded right side (%q): "+
						"the right side must be a time-bounded subquery, not the "+
						"bare table with the bound in ON\n--- SQL ---\n%s", bad, sql)
				}
			}
			if !strings.Contains(sql, "FROM spans\n\t\t\tWHERE time >= ? AND time <= ?") {
				t.Errorf("joined subquery is not time-bounded on its own WHERE; "+
					"without it the join reads the entire spans table\n--- SQL ---\n%s", sql)
			}

			// The subquery's two placeholders have to stand exactly where
			// the ON-clause bounds used to, because QueryRepeatedSpans
			// binds positionally in text order (…, MinRepeats, limit,
			// From, To). Anything else silently swaps arguments.
			gotPlaceholders := strings.Count(sql, "?")
			wantPlaceholders := strings.Count(tc.keysArray, "?") +
				strings.Count(tc.whereSQL, "?") +
				2 + // HAVING cnt >= ?, LIMIT ?
				2 //  the joined subquery's from/to
			if gotPlaceholders != wantPlaceholders {
				t.Errorf("placeholder count = %d, want %d — positional binding "+
					"in QueryRepeatedSpans depends on this\n--- SQL ---\n%s",
					gotPlaceholders, wantPlaceholders, sql)
			}
			if idxLimit, idxJoin := strings.Index(sql, "LIMIT ?"), strings.Index(sql, "WHERE time >= ? AND time <= ?\n\t\t) AS s"); idxLimit < 0 || idxJoin < 0 || idxJoin < idxLimit {
				t.Errorf("the joined subquery's bounds must come AFTER `LIMIT ?` "+
					"in the SQL text (bind order From/To is last)\n--- SQL ---\n%s", sql)
			}

			// CLAUDE.md hard constraint: any raw-spans read is bounded.
			for _, want := range []string{"LIMIT ?", "max_execution_time = 20"} {
				if !strings.Contains(sql, want) {
					t.Errorf("raw-spans read lost its bound %q\n--- SQL ---\n%s", want, sql)
				}
			}
		})
	}
}

// nonGlobalTelemetryJoin matches a JOIN whose right side is a bare
// telemetry table on the same line — the wrong shape. The correct
// form puts the table inside a time-bounded subquery, so its name
// lands on its own line and cannot match.
var nonGlobalTelemetryJoin = regexp.MustCompile(
	`(?i)(LEFT|INNER|ANY|SEMI|ANTI|CROSS|FULL|OUTER|ASOF)?\s*JOIN\s+(spans|metric_points|logs)\b`)

// v0.9.1285 — the class pin. `make audit` CHECK 5 only ever grepped
// `IN (SELECT`, so the JOIN twin of the same shard-local trap went
// unflagged in FIVE places (repeats, backtrace, three in topology)
// while the audit reported clean. CHECK 5b now covers it; this is
// the `go test` half so the gap cannot reopen between audit runs.
//
// Scope is deliberately the telemetry tables only. Joining an MV, a
// state table, or a small dimension table is a different (and fine)
// thing — those are not sharded across the fleet.
func TestNoNonGlobalJoinOnTelemetryTables(t *testing.T) {
	for _, dir := range []string{".", filepath.Join("..", "api")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				// Test files quote the anti-pattern on purpose (this
				// file included) — scanning them would self-trigger.
				continue
			}
			path := filepath.Join(dir, name)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for i, line := range strings.Split(string(src), "\n") {
				trimmed := strings.TrimSpace(line)
				// Incident notes name the anti-pattern in prose.
				if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
					continue
				}
				if strings.Contains(line, "GLOBAL") {
					continue
				}
				if nonGlobalTelemetryJoin.MatchString(line) {
					t.Errorf("%s:%d — JOIN on a telemetry table without GLOBAL "+
						"and without a time-bounded subquery right side; on a "+
						"rand()-sharded Distributed table this joins each shard "+
						"against its own slice only (v0.9.1285)\n\t%s",
						path, i+1, trimmed)
				}
			}
		}
	}
}
