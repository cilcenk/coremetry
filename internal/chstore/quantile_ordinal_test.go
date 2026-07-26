package chstore

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// v0.9.259 — cross-cutting guard for the quantile-ordinal bug class.
//
// Coremetry runs TWO incompatible TDigest layouts side by side:
//
//	3-wide "summary" family: quantilesTDigestState(0.5, 0.95, 0.99)
//	                         → arrayElement 1=p50, 2=p95, 3=p99
//	  service_summary_5m, operation_summary_5m, operation_group_summary_5m,
//	  db_summary_5m, db_caller_summary_5m, db_statement_summary_5m,
//	  messaging_summary_5m, messaging_caller_summary_5m
//
//	4-wide "doorway" family: quantilesTDigestState(0.5, 0.9, 0.95, 0.99)
//	                         → arrayElement 1=p50, 2=p90, 3=p95, 4=p99
//	  spanmetrics_1m, spanmetrics_10s, spanmetrics_1s
//
// Index 2 therefore means p95 in one family and p90 in the other. Copying an
// ordinal from a read site of one family to a read site of the other is the
// entire bug class, and its failure mode is the worst kind: a monotonic,
// plausible, silently-wrong number. On log-normal latency a p95 served as
// "P90" reads ~10-40% high and no test, type or CH error notices.
//
// ClickHouse arrayElement is 1-INDEXED. This test parses every
//
//	arrayElement(quantilesTDigestMerge(<args>)(<col>), <n>) ... AS <alias>
//
// in the package and asserts <n> selects the quantile that <alias> claims.
// A 23-agent audit (2026-07-25) verified all sites were correct at v0.9.258;
// this pins that state so the next edit can't quietly break it.
func TestQuantileOrdinalsMatchTheirAliases(t *testing.T) {
	// arrayElement( quantilesTDigestMerge( ARGS )( COL ), N )  ... AS  ALIAS
	// The alias may sit after arithmetic (`/ 1e6`), so scan to the next AS.
	re := regexp.MustCompile(
		`arrayElement\(\s*quantilesTDigestMerge\(([^)]*)\)\([^)]*\)\s*,\s*(\d+)\s*\)[^,\n]*?\bAS\s+(\w+)`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			args, idxStr, alias := m[1], m[2], m[3]

			quantiles, err := parseQuantileArgs(args)
			if err != nil {
				// A non-literal arg list (fmt.Sprintf placeholder) can't be
				// checked statically; those sites are covered by their own
				// tests. Skip rather than fail.
				continue
			}
			idx, err := strconv.Atoi(idxStr)
			if err != nil {
				t.Fatalf("%s: unparseable index %q", f, idxStr)
			}
			wantQ, ok := quantileForAlias(alias)
			if !ok {
				// Alias doesn't name a percentile (e.g. `dur_ms`); nothing to
				// cross-check.
				continue
			}
			checked++

			if idx < 1 || idx > len(quantiles) {
				t.Errorf("%s: arrayElement index %d is out of range for %d-wide state (%v), alias %q\n"+
					"  ClickHouse arrayElement is 1-indexed; an out-of-range constant returns the\n"+
					"  type default 0.0, so the column reads \"0 ms\" everywhere instead of erroring.",
					f, idx, len(quantiles), quantiles, alias)
				continue
			}
			if got := quantiles[idx-1]; got != wantQ {
				t.Errorf("%s: alias %q reads arrayElement index %d of %v = quantile %g, but the alias claims %g\n"+
					"  This is the family-crossing bug: index %d means %g in a %d-wide state.\n"+
					"  Wrong-but-plausible number, no error. Fix the INDEX, never the alias.",
					f, alias, idx, quantiles, got, wantQ, idx, got, len(quantiles))
			}
		}
	}

	// Guard the guard: if a refactor moves these reads out of this package or
	// changes the SQL shape, a silently-zero match count would make this test
	// pass while checking nothing.
	// 35 expressions matched at v0.9.259 (each query contributes one per
	// percentile it projects). The floor only has to be far enough above zero
	// to catch the regex silently ceasing to match — it is not a coverage
	// target, so consolidating queries should never trip it.
	if checked < 20 {
		t.Errorf("only %d quantile ordinal/alias pairs cross-checked — expected 20+ (35 at v0.9.259).\n"+
			"The regex likely stopped matching the SQL shape; this test is no longer guarding anything.", checked)
	}
}

// parseQuantileArgs turns "0.5, 0.95, 0.99" into [0.5 0.95 0.99].
func parseQuantileArgs(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no args")
	}
	return out, nil
}

// quantileForAlias maps a SQL alias to the quantile its NAME promises.
// Matches p50_ms / p95Ms / P99 / median_ms etc.
func quantileForAlias(alias string) (float64, bool) {
	a := strings.ToLower(alias)
	switch {
	case strings.Contains(a, "p50") || strings.Contains(a, "median"):
		return 0.5, true
	case strings.Contains(a, "p90"):
		return 0.9, true
	case strings.Contains(a, "p95"):
		return 0.95, true
	case strings.Contains(a, "p99"):
		return 0.99, true
	}
	return 0, false
}

// DBCallerBreakdown is filled by TWO queries — the /databases caller
// breakdown and the /messaging one — and its percentile fields are plain
// float64, not pointers. A producer that forgets a projection therefore does
// not blank the cell: it marshals 0 and the drawer prints "0.0ms", a
// plausible wrong number on exactly the panel an SRE opens to find a slow
// caller. Both queries must project every percentile the struct exposes.
//
// v0.9.263 — pinned as an EQUALITY between the number of scan targets and the
// number of scans, so adding a third producer without the projection fails
// here rather than in prod.
func TestSharedCallerBreakdownProducersAllProjectP95(t *testing.T) {
	src, err := os.ReadFile("dependencies.go")
	if err != nil {
		t.Fatalf("read dependencies.go: %v", err)
	}
	s := string(src)

	producers := strings.Count(s, "var b DBCallerBreakdown")
	withP95 := strings.Count(s, "&bAvg, &bP50, &bP95, &bP99")
	assigns := strings.Count(s, "b.P95Ms = *bP95")
	projections := strings.Count(s, "AS p95_ms")
	// v0.9.273 — p50 is checked too. The v0.9.263 guard watched p95 ONLY, so
	// when the shared struct gained P95 but not P50 the drawer ended up showing
	// three percentiles in its aggregate strip and two in every caller row, and
	// nothing here objected. A guard that covers one column of a grid is a
	// guard with a hole in it.
	withP50 := strings.Count(s, "&bAvg, &bP50,")
	assignsP50 := strings.Count(s, "b.P50Ms = *bP50")

	if producers < 2 {
		t.Fatalf("expected at least 2 DBCallerBreakdown producers, found %d — "+
			"the struct or its call sites moved and this guard is no longer aimed at anything", producers)
	}
	if withP95 != producers {
		t.Errorf("%d DBCallerBreakdown producers but only %d scan p95 — the ones that don't\n"+
			"will render a hard 0.0ms in the drawer (non-pointer float64, so it is a WRONG\n"+
			"NUMBER, not a blank). Every producer must SELECT and Scan p95_ms.", producers, withP95)
	}
	if assigns != producers {
		t.Errorf("%d producers but %d assign b.P95Ms — a scanned-but-unassigned p95 silently stays 0",
			producers, assigns)
	}
	if withP50 != producers || assignsP50 != producers {
		t.Errorf("%d producers but %d scan p50 and %d assign it — the percentile grid must be\n"+
			"complete in EVERY producer, or the drawer shows three values in its aggregate strip\n"+
			"and two in each caller row (the v0.9.263 miss, caught by live verification not by this test).",
			producers, withP50, assignsP50)
	}
	// Each caller query needs its own projection, and the two detail
	// aggregates plus the two overview queries have theirs as well.
	if projections < producers {
		t.Errorf("only %d `AS p95_ms` projections for %d producers — a Scan without a matching\n"+
			"SELECT column shifts EVERY later column by one position", projections, producers)
	}
}

// DBQueryStat is filled by THREE queries in dbqueries.go — the raw
// slow-queries builder, the MV builder, and GetTopDBQueries (raw-only, behind
// /service top statements). It exposes p50/p95/p99 as plain float64, so a
// producer missing one projection renders "0.00ms" rather than a blank.
//
// v0.9.265 — the pre-existing builder tests (dbqueries_sql_test.go,
// dbqueries_mv_test.go) pin finalisers, filters and bounds but say nothing
// about which percentiles each query projects, which is why adding p50 did
// not disturb them. This asserts the alignment directly: every query that
// reports p95 and p99 must also report p50.
func TestDBQueryStatProducersProjectEveryPercentile(t *testing.T) {
	src, err := os.ReadFile("dbqueries.go")
	if err != nil {
		t.Fatalf("read dbqueries.go: %v", err)
	}
	s := string(src)

	p50 := strings.Count(s, "AS p50_ms")
	p95 := strings.Count(s, "AS p95_ms")
	p99 := strings.Count(s, "AS p99_ms")

	if p95 < 3 {
		t.Fatalf("expected 3+ DBQueryStat producers projecting p95, found %d — the queries moved "+
			"and this guard is no longer aimed at anything", p95)
	}
	if p50 != p95 || p50 != p99 {
		t.Errorf("percentile projections are misaligned: p50=%d p95=%d p99=%d.\n"+
			"All three fill the same DBQueryStat, whose fields are plain float64 — the query\n"+
			"missing a projection does not blank the cell, it renders 0.00ms. Every producer\n"+
			"(raw slow-queries, MV, GetTopDBQueries) must project all three.", p50, p95, p99)
	}
}

// The two families must stay distinguishable. If someone "harmonizes"
// spanmetrics down to 3-wide, every 4-wide read site (endpoints p95 at index 3,
// p99 at index 4) silently shifts by one — p95 becomes p90, p99 becomes p95.
// Conversely widening a summary MV shifts its p95/p99 reads the other way.
func TestQuantileFamilyWidthsAreStable(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	s := string(src)

	const threeWide = "quantilesTDigestState(0.5, 0.95, 0.99)"
	const fourWide = "quantilesTDigestState(0.5, 0.9, 0.95, 0.99)"

	if n := strings.Count(s, threeWide); n < 8 {
		t.Errorf("expected 8+ three-wide %s definitions, found %d — a summary MV changed width,\n"+
			"which shifts every arrayElement read against it.", threeWide, n)
	}
	if n := strings.Count(s, fourWide); n < 3 {
		t.Errorf("expected 3+ four-wide %s definitions (spanmetrics 1m/10s/1s), found %d —\n"+
			"the doorway tiers must agree with each other or the same index means different\n"+
			"quantiles depending on which tier the query picked.", fourWide, n)
	}
}

// v0.9.274 — the drawer's raw scans must resolve `instance` the SAME way
// db_summary_5m does, or the drawer disagrees with the row it was opened from.
//
// Measured live before the fix: for the clickhouse row, whose instance is named
// from the service_name rung, the old `peer_service = ?` predicate matched 0
// spans while the real identity matched 4659 — so the drawer rendered a span
// count and a caller list (both MV-sourced) beside an EMPTY Top statements
// table. This is the backend twin of the dead row link fixed in v0.9.268.
func TestDBInstanceExprMatchesTheMVIdentity(t *testing.T) {
	ddl, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	// Every rung the MV coalesces, in order. If the MV gains or reorders one,
	// this test fails until the raw expression is brought back in step.
	rungs := []string{
		"nullIf(peer_service, '')",
		"nullIf(attr_values[indexOf(attr_keys, 'server.address')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'db.host')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'db.name')], '')",
		"nullIf(service_name, '')",
	}
	for _, r := range rungs {
		if !strings.Contains(string(ddl), r) {
			t.Errorf("db_summary_5m no longer coalesces %s — the MV moved and dbInstanceExpr\n"+
				"is now resolving a DIFFERENT identity than the rows it must match", r)
		}
		if !strings.Contains(dbInstanceExpr, r) {
			t.Errorf("dbInstanceExpr is missing rung %s — any instance named from it will match\n"+
				"zero spans in the raw scans, leaving the drawer half-populated", r)
		}
	}
	// Order matters: coalesce returns the FIRST non-empty rung, so a permuted
	// chain silently renames instances rather than failing.
	prev := -1
	for _, r := range rungs {
		i := strings.Index(dbInstanceExpr, r)
		if i <= prev {
			t.Fatalf("dbInstanceExpr rungs are out of order at %s — coalesce takes the FIRST\n"+
				"non-empty value, so reordering renames instances silently", r)
		}
		prev = i
	}
	if !strings.HasSuffix(strings.TrimSpace(dbInstanceExpr), "'unknown'\n)") &&
		!strings.Contains(dbInstanceExpr, "'unknown'") {
		t.Error("dbInstanceExpr must end in the 'unknown' sentinel — it is what makes the " +
			"instance=\"unknown\" case work without a special branch")
	}
}
