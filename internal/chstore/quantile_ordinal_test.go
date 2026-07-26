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
	withP95 := strings.Count(s, "&bAvg, &bP95, &bP99")
	assigns := strings.Count(s, "b.P95Ms = *bP95")
	projections := strings.Count(s, "AS p95_ms")

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
	// Each caller query needs its own projection, and the two detail
	// aggregates plus the two overview queries have theirs as well.
	if projections < producers {
		t.Errorf("only %d `AS p95_ms` projections for %d producers — a Scan without a matching\n"+
			"SELECT column shifts EVERY later column by one position", projections, producers)
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
