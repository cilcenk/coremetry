package chstore

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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

// v0.9.1287 — the Repeats list's top rows were blank. Measured live on
// the 2-shard local cluster at v0.9.1286,
// GET /api/spans/repeats?groupBy=db.statement&minRepeats=5 over 1h:
// 2837 candidate groups, 2721 of them (95.9%) the ALL-EMPTY tuple at
// 19-20 spans each — every non-DB span in a trace collapses into one
// empty bucket and that bucket then reads as a repeat of itself. They
// outnumbered the real N+1 badly enough to consume the LIMIT 200: 146
// blank rows against 54 real, with 62 of the 116 genuinely repeating
// statements pushed out of the response entirely.
//
// The fix excludes the tuple that is empty in EVERY position. It does
// NOT exclude partially-empty tuples: ["SELECT instrument_prices", ""]
// under groupBy=name,http.route is a real shape whose HTTP route is
// legitimately absent (116 such rows in the same window).
//
// Two things are pinned here and both are load-bearing:
//
//   - ANY-semantics, not ALL. arrayExists over a not-empty predicate
//     keeps a tuple with one non-empty element. The plausible wrong
//     spelling, arrayAll, would drop every partial-empty row and
//     silently delete the multi-key group-by's whole result set.
//   - The conjunct binds NO argument. repeatedSpansArgs binds
//     positionally in SQL TEXT order; v0.9.1286 was a 500 caused by one
//     group-key arg drifting a slot. A filter carrying a `?` in the
//     HAVING would re-open exactly that.
func TestRepeatedSpansSQLDropsAllEmptyGroupTuple(t *testing.T) {
	cases := []struct {
		name      string
		keysArray string
		whereSQL  string
	}{
		{
			name:      "single well-known column — the shape the operator hit",
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
			name:      "multi-key — partial-empty tuples live here",
			keysArray: "[toString(name), toString(attr_values[indexOf(attr_keys, ?)])]",
			whereSQL:  "WHERE time >= ? AND time <= ?",
		},
		{
			name:      "coalesce-chain key (peer) — no bind arg, quotes inside the expr",
			keysArray: "[toString(coalesce(nullIf(peer_service, ''), ''))]",
			whereSQL:  "WHERE time >= ? AND time <= ? AND service_name = ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := repeatedSpansSQL(tc.keysArray, tc.whereSQL)

			// Isolate the HAVING span first, then assert against THAT.
			// Asserting the finished literal against the whole query would
			// make the first check subsume every later one — a wrong-verb
			// mutation would trip the "exclusion is missing" branch and the
			// ANY-vs-ALL guard below would never run, so it could rot
			// without anyone noticing.
			having := strings.Index(sql, "HAVING")
			orderBy := strings.Index(sql, "ORDER BY cnt DESC")
			if having < 0 || orderBy < 0 || orderBy < having {
				t.Fatalf("cannot locate the inner HAVING…ORDER BY span\n--- SQL ---\n%s", sql)
			}
			havingSpan := sql[having:orderBy]

			// (1) An exclusion exists, it is expressed over the group tuple,
			// and it lives in the HAVING — after aggregation, where the
			// tuple is formed, and therefore before ORDER BY / LIMIT so real
			// rows reclaim the 200 slots.
			if !strings.Contains(havingSpan, "group_values") || !strings.Contains(havingSpan, "!= ''") {
				t.Errorf("the HAVING no longer excludes the all-empty group tuple: the "+
					"Repeats list refills with blank rows that are not span shapes "+
					"(v0.9.1287 — 2721 of 2837 groups measured on "+
					"groupBy=db.statement over 1h)\n--- HAVING ---\n%s", havingSpan)
			}

			// (2) ANY, never ALL — an independent property of the same
			// conjunct. arrayAll would drop ["SELECT instrument_prices", ""]
			// and with it every multi-key result where one key is
			// legitimately absent (116 such rows measured on
			// groupBy=name,http.route in the same window).
			if !strings.Contains(havingSpan, "arrayExists(") {
				t.Errorf("exclusion is not ANY-semantics: only arrayExists keeps a "+
					"partially-empty tuple, which IS a real shape\n--- HAVING ---\n%s",
					havingSpan)
			}
			for _, bad := range []string{"arrayAll(", "arrayCount(", "empty(", "hasAll("} {
				if strings.Contains(havingSpan, bad) {
					t.Errorf("exclusion uses %q — ALL-semantics deletes partially-empty "+
						"tuples wholesale\n--- HAVING ---\n%s", bad, havingSpan)
				}
			}

			// The conjunct must not have introduced a placeholder — the
			// v0.9.1286 arg-order defect is one slot away from here.
			gotPlaceholders := strings.Count(sql, "?")
			wantPlaceholders := strings.Count(tc.keysArray, "?") +
				strings.Count(tc.whereSQL, "?") +
				2 + // HAVING cnt >= ?, LIMIT ?
				2 //  the joined subquery's from/to
			if gotPlaceholders != wantPlaceholders {
				t.Errorf("placeholder count = %d, want %d — the empty-tuple exclusion "+
					"must bind NO argument or every arg after it shifts a slot "+
					"(v0.9.1286)\n--- SQL ---\n%s", gotPlaceholders, wantPlaceholders, sql)
			}

			// The count filter has to survive alongside it; an exclusion that
			// replaced `cnt >= ?` would pass the checks above.
			if !strings.Contains(sql, "cnt >= ?") {
				t.Errorf("MinRepeats filter lost from the HAVING\n--- SQL ---\n%s", sql)
			}
		})
	}
}

// v0.9.1288 — the Repeats table's "root" column was blank on EVERY
// row. Measured live on the 2-shard local cluster at v0.9.1287,
// GET /api/spans/repeats?groupBy=db.statement&minRepeats=5 over 1h:
// 200 of 200 rows carried rootName = "". The operator could see THAT a
// statement ran 40× in one trace but not WHICH entry point produced
// the N+1, which is the column's entire job.
//
// The old projection was
//
//	any(if(s.parent_id = '', s.name, ''))
//
// `any` resolves FIRST and picks an arbitrary row from the joined set;
// the root-test runs afterwards on whatever it happened to grab. A
// trace with a 40× repeat holds ~45 spans and exactly one root, so the
// arbitrary pick is a non-root ~44 times in 45 and the `if` collapses
// to "". The predicate has to be inside the aggregate.
//
// Re-running the same window with the fix: 0 of 200 empty, and 35 of
// the 200 (17.5%) resolved through the SECOND layer — traces that
// began before `from`, whose root span is outside the bound the
// joined subquery carries by design (v0.9.1285 put it there to stop a
// full-table scan, and widening it back is not on the table). Those
// rows name the earliest still-visible span instead of nothing.
//
// Both layers are pinned because either one alone still ships a bug:
// drop layer 1 and every row is arbitrary again; drop layer 2 and
// 17.5% of rows go back to blank.
func TestRepeatedSpansSQLResolvesRealRootName(t *testing.T) {
	cases := []struct {
		name      string
		keysArray string
		whereSQL  string
	}{
		{
			name:      "default db.statement — the shape the operator hit",
			keysArray: "[toString(db_statement)]",
			whereSQL:  "WHERE time >= ? AND time <= ?",
		},
		{
			name:      "attr-array key (carries a bind arg)",
			keysArray: "[toString(attr_values[indexOf(attr_keys, ?)])]",
			whereSQL:  "WHERE time >= ? AND time <= ?",
		},
		{
			name:      "multi-key group-by",
			keysArray: "[toString(name), toString(peer_service)]",
			whereSQL:  "WHERE time >= ? AND time <= ? AND service_name = ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := repeatedSpansSQL(tc.keysArray, tc.whereSQL)

			// Isolate the outer SELECT projection and assert against
			// THAT. The inner CTE also aggregates over spans, so a
			// whole-query match would let an inner-scope coincidence
			// satisfy a check about the outer root-name column.
			outer := strings.Index(sql, "SELECT d.trace_id")
			fromDup := strings.Index(sql, "FROM dup_traces d")
			if outer < 0 || fromDup < 0 || fromDup < outer {
				t.Fatalf("cannot locate the outer SELECT projection\n--- SQL ---\n%s", sql)
			}
			projection := sql[outer:fromDup]

			// (1) PRIMARY LAYER — the root test must be an argument OF
			// the aggregate, not a filter applied to its result. This is
			// the exact regression: `any(if(...))` evaluates `any` first.
			if !strings.Contains(projection, "anyIf(s.name, s.parent_id = '')") {
				t.Errorf("root name is not resolved with anyIf over the root "+
					"predicate; an aggregate that picks a row BEFORE testing "+
					"parent_id returns a non-root ~44 times in 45 and the row "+
					"renders blank (v0.9.1288 — 200/200 rows empty)"+
					"\n--- PROJECTION ---\n%s", projection)
			}
			if strings.Contains(projection, "any(if(s.parent_id") {
				t.Errorf("the v0.9.1288 defect is back verbatim: `any` resolves "+
					"before the root predicate, so the predicate tests an "+
					"arbitrary span\n--- PROJECTION ---\n%s", projection)
			}

			// (2) FALLBACK LAYER — an independent property. The joined
			// subquery is time-bounded (v0.9.1285), so a trace older than
			// the window has no root inside it and layer 1 alone leaves
			// 17.5% of rows blank. argMin over the time column names the
			// earliest span that IS visible.
			if !strings.Contains(projection, "argMin(s.name, s.time)") {
				t.Errorf("no fallback for the window-overhang case: when a trace "+
					"started before `from` its root is outside the joined "+
					"subquery's bound and the primary layer yields the empty "+
					"string (v0.9.1288 — 35 of 200 rows measured)"+
					"\n--- PROJECTION ---\n%s", projection)
			}
			// The two layers have to be WIRED to each other. Both
			// expressions being present but only one projected would pass
			// the checks above.
			if !strings.Contains(projection, "if(anyIf(s.name, s.parent_id = '') != '',") {
				t.Errorf("the layers are not composed: the fallback must be the "+
					"else-branch of an emptiness test on the primary layer"+
					"\n--- PROJECTION ---\n%s", projection)
			}

			// argMin reads `time`, so the joined subquery has to project
			// it. Without this the fallback would not compile at all —
			// and a text-only pin would not have noticed.
			if !strings.Contains(sql, "SELECT trace_id, service_name, parent_id, name, time") {
				t.Errorf("joined subquery does not project `time`; argMin(s.name, "+
					"s.time) has nothing to order by\n--- SQL ---\n%s", sql)
			}

			// The Go scanner reads exactly seven columns
			// (QueryRepeatedSpans). Aliasing the intermediate layers would
			// project extra columns and break the scan, so the outer
			// projection must stay at seven top-level items.
			if n := strings.Count(projection, " AS "); n != 0 {
				t.Errorf("outer projection introduced %d alias(es); every SELECT "+
					"item is a scanned column and QueryRepeatedSpans reads "+
					"exactly 7\n--- PROJECTION ---\n%s", n, projection)
			}

			// Neither layer may bind an argument — the v0.9.1286
			// positional-drift defect sits one slot away.
			gotPlaceholders := strings.Count(sql, "?")
			wantPlaceholders := strings.Count(tc.keysArray, "?") +
				strings.Count(tc.whereSQL, "?") +
				2 + // HAVING cnt >= ?, LIMIT ?
				2 //  the joined subquery's from/to
			if gotPlaceholders != wantPlaceholders {
				t.Errorf("placeholder count = %d, want %d — the root-name layers "+
					"must bind NO argument or every arg after them shifts a "+
					"slot (v0.9.1286)\n--- SQL ---\n%s",
					gotPlaceholders, wantPlaceholders, sql)
			}
		})
	}
}

// v0.9.1289 — the service column named an arbitrary participant in the
// trace. It read `any(s.service_name)` off the JOINED side, and the
// joined side is the WHOLE trace, so the pick ranged over every service
// the request touched. Measured live at v0.9.1288 on the 200 rows of
// groupBy=db.statement over 1h: 110 said forex-service, 64 said
// portfolio-service, for repeats whose root is GET /web/portfolio — a
// route served by web-bff. Every row was wrong, and v0.9.1288 made it
// visible by giving rootName a real value to be mismatched against.
//
// The fix is a scope change, not a "pick better from the join": the
// column is sourced from the INNER aggregate, which groups exactly the
// repeating rows. The product decision behind that is deliberate and is
// the thing this test protects — Service names the service DOING the
// repeating, NOT the root's service. Resolving it to the root would
// give "web-bff · GET /web/portfolio": self-consistent, and useless for
// triage, because it names the front door instead of the culprit.
//
// So the plausible-looking regressions are BOTH failures here:
// reverting to any(s.service_name), and "fixing" it to an
// anyIf(s.service_name, ...) on the root predicate. The joined alias
// must not supply service at all.
func TestRepeatedSpansSQLTakesServiceFromInnerGroup(t *testing.T) {
	cases := []struct {
		name      string
		keysArray string
		whereSQL  string
	}{
		{
			name:      "default db.statement — the shape the operator hit",
			keysArray: "[toString(db_statement)]",
			whereSQL:  "WHERE time >= ? AND time <= ?",
		},
		{
			name:      "attr-array key (carries a bind arg)",
			keysArray: "[toString(attr_values[indexOf(attr_keys, ?)])]",
			whereSQL:  "WHERE time >= ? AND time <= ? AND service_name = ?",
		},
		{
			name:      "multi-key group-by",
			keysArray: "[toString(name), toString(peer_service)]",
			whereSQL:  "WHERE time >= ? AND time <= ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := repeatedSpansSQL(tc.keysArray, tc.whereSQL)

			innerStart := strings.Index(sql, "WITH dup_traces AS (")
			outer := strings.Index(sql, "SELECT d.trace_id")
			fromDup := strings.Index(sql, "FROM dup_traces d")
			if innerStart < 0 || outer < 0 || fromDup < 0 || outer < innerStart || fromDup < outer {
				t.Fatalf("cannot locate the inner CTE / outer projection spans\n--- SQL ---\n%s", sql)
			}
			inner := sql[innerStart:outer]
			projection := sql[outer:fromDup]

			// (1) The value is computed in the INNER scope, over the rows
			// that actually repeat. Note the absence of an `s.` prefix:
			// inside the CTE there is no join, so this can only be the
			// repeating group's own column.
			if !strings.Contains(inner, "any(service_name)") {
				t.Errorf("inner aggregate no longer derives the repeating group's own "+
					"service; sourcing it from the join names an arbitrary "+
					"participant in the trace (v0.9.1289 — 174/200 rows misattributed)"+
					"\n--- INNER ---\n%s", inner)
			}

			// (2) The joined alias must not supply service in the outer
			// projection — in EITHER spelling. The bare revert and the
			// tempting "resolve it to the root's service" are both wrong,
			// the second one silently so.
			for _, bad := range []string{
				"any(s.service_name)",
				"anyIf(s.service_name",
				"argMin(s.service_name",
			} {
				if strings.Contains(projection, bad) {
					t.Errorf("service is sourced from the joined side via %q. The joined "+
						"side is the whole trace: `any` names a random participant, and "+
						"an `anyIf` on the root predicate names the front door (web-bff) "+
						"instead of the service doing the repeating (v0.9.1289)"+
						"\n--- PROJECTION ---\n%s", bad, projection)
				}
			}

			// (3) The layers are wired: the outer projection actually reads
			// the inner alias. Without this, (1) and (2) could both hold
			// while the column was dropped entirely.
			if !strings.Contains(projection, "any(d.svc)") {
				t.Errorf("outer projection does not read the inner service alias; "+
					"QueryRepeatedSpans scans it as column 6\n--- PROJECTION ---\n%s",
					projection)
			}

			// The Go scanner reads exactly 7 columns and the outer
			// projection carries no aliases — re-asserted because this
			// change moves a column's SOURCE, and the count is the thing
			// that must NOT move with it.
			if n := strings.Count(projection, " AS "); n != 0 {
				t.Errorf("outer projection introduced %d alias(es); every SELECT item "+
					"is a scanned column and QueryRepeatedSpans reads exactly 7"+
					"\n--- PROJECTION ---\n%s", n, projection)
			}

			// The inner alias must not have introduced a placeholder.
			gotPlaceholders := strings.Count(sql, "?")
			wantPlaceholders := strings.Count(tc.keysArray, "?") +
				strings.Count(tc.whereSQL, "?") +
				2 + // HAVING cnt >= ?, LIMIT ?
				2 //  the joined subquery's from/to
			if gotPlaceholders != wantPlaceholders {
				t.Errorf("placeholder count = %d, want %d — the inner service alias must "+
					"bind NO argument or every arg after it shifts a slot (v0.9.1286)"+
					"\n--- SQL ---\n%s", gotPlaceholders, wantPlaceholders, sql)
			}
		})
	}
}

// v0.9.1287 — the arg-order contract, re-asserted against the SQL that
// now carries the empty-tuple exclusion. TestRepeatedSpansArgsMatchPlaceholderOrder
// already pins the builder, but it pins repeatedSpansArgs against
// whatever repeatedSpansSQL happens to emit; this states the specific
// invariant the v0.9.1287 edit could have broken — that the new HAVING
// conjunct sits BETWEEN `cnt >= ?` and `LIMIT ?` without displacing
// either, so the tail bind order (minRepeats, limit, from, to) holds.
func TestEmptyTupleExclusionKeepsBindOrder(t *testing.T) {
	from := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	sql := repeatedSpansSQL(
		"[toString(attr_values[indexOf(attr_keys, ?)])]",
		"WHERE time >= ? AND time <= ?",
	)
	args := repeatedSpansArgs([]any{"db.operation"}, []any{from, to}, 5, 200, from, to)

	if n := strings.Count(sql, "?"); n != len(args) {
		t.Fatalf("SQL has %d placeholders but %d args\n--- SQL ---\n%s", n, len(args), sql)
	}

	// Placeholder order in text: group key → WHERE from/to → HAVING
	// minRepeats → LIMIT → subquery from/to. The exclusion inserts
	// itself into the HAVING between the third and fourth.
	idxHavingArg := strings.Index(sql, "cnt >= ?")
	idxLimitArg := strings.Index(sql, "LIMIT ?")
	idxExcl := strings.Index(sql, "arrayExists(")
	if idxExcl < idxHavingArg || idxExcl > idxLimitArg {
		t.Fatalf("exclusion moved outside the (cnt >= ?, LIMIT ?) span; bind order "+
			"is positional and this reorders the tail\n--- SQL ---\n%s", sql)
	}

	want := []any{"db.operation", from, to, 5, 200, from, to}
	for i, w := range want {
		if args[i] != w {
			t.Errorf("arg[%d] = %#v, want %#v", i, args[i], w)
		}
	}
}
