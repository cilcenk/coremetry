package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.9.305 — /endpoints gained a P90 column, and the trap the brief
// flagged is the one this pins.
//
// The MV path already merged the 4-wide family (0.5, 0.9, 0.95, 0.99);
// the raw path used the 3-wide (0.5, 0.95, 0.99), which has no 0.9 at
// all. Adding the column without widening that family would have given
// either an empty cell or — far worse — a SHIFTED ordinal: index 2 in
// the 3-wide family is p95, so "P90" would have silently rendered p95.
// That is the exact bug class quantile_ordinal_test.go exists for, and
// widening the family also moved p95/p99 from ordinals 2/3 to 3/4.
//
// Here we pin the property that makes the column trustworthy: on this
// surface, BOTH paths merge the same family.
func TestEndpointsQuantileFamiliesAgree(t *testing.T) {
	src := mustReadSource(t, "endpoints.go")

	// Every quantile family this file constructs or merges.
	fam := regexp.MustCompile(`quantilesTDigest(?:Merge|MergeState|State)\(([^)]*)\)`)
	found := map[string]int{}
	for _, m := range fam.FindAllStringSubmatch(src, -1) {
		args := strings.Join(strings.Fields(strings.ReplaceAll(m[1], " ", "")), "")
		found[args]++
	}
	if len(found) == 0 {
		t.Fatal("no quantile families found — the scan pattern has drifted from the source")
	}
	if len(found) != 1 {
		t.Fatalf("endpoints.go merges %d DIFFERENT quantile families %v — "+
			"the MV and raw paths must agree, or the same column means different "+
			"percentiles depending on which path answered", len(found), keysOf(found))
	}
	for args := range found {
		if !strings.Contains(args, "0.9,") {
			t.Fatalf("family %q has no 0.9 — the P90 column would read a shifted ordinal", args)
		}
	}
}

// And the ordinals themselves: in the 4-wide family P90 is index 2.
// Reading index 2 of the OLD 3-wide family would have been p95.
func TestEndpointsP90ReadsTheRightOrdinal(t *testing.T) {
	src := mustReadSource(t, "endpoints.go")

	re := regexp.MustCompile(`arrayElement\(quantilesTDigestMerge\([^)]*\)\([^)]*\),\s*(\d+)\)[^\n]*AS\s+(p\d+)_ms`)
	seen := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		seen[m[2]] = m[1]
	}
	// (0.5, 0.9, 0.95, 0.99) → 1-indexed
	want := map[string]string{"p50": "1", "p90": "2", "p95": "3", "p99": "4"}
	for alias, idx := range want {
		got, ok := seen[alias]
		if !ok {
			t.Errorf("%s_ms is not projected — the column would render empty", alias)
			continue
		}
		if got != idx {
			t.Errorf("%s_ms reads ordinal %s, want %s — that ordinal is a DIFFERENT percentile",
				alias, got, idx)
		}
	}
}

func mustReadSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func keysOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
