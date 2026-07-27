package chstore

import (
	"regexp"
	"strings"
	"testing"
)

// v0.9.310 (brief N3) — the /endpoints list carries the slowest and
// worst-error trace id per row, so the operator jumps straight to the
// trace instead of landing on a filtered list and re-scanning it.
//
// The whole justification is that it costs NOTHING: the same MV rows
// are already being read for the RED counts, so these are two more
// aggregate states over them. That claim is only true while the
// exemplars ride the EXISTING scan — the moment someone resolves them
// with a second query the slice stops being free, and at 1000s of
// services × routes that is a per-row round trip.
func TestEndpointExemplarsRideTheExistingScan(t *testing.T) {
	src := mustReadSource(t, "endpoints.go")

	// The list read is one statement: a per_bucket CTE feeding one outer
	// aggregate. Both exemplar levels must live inside it.
	if n := strings.Count(src, "spanmetricsSourceFor(sourceMV)"); n != 1 {
		t.Fatalf("the list read resolves the spanmetrics tier %d times — the exemplars are only "+
			"free while it stays ONE scan; a second read would be a per-row round trip at "+
			"1000s of services x routes", n)
	}
	for _, frag := range []string{
		"argMaxMergeState(slow_exemplar_state)",
		"argMaxIfMergeState(error_exemplar_state)",
		"argMaxMerge(slow_ex_state)",
		"argMaxIfMerge(err_ex_state)",
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("missing %q — the two-level merge is what lets the CTE carry exemplars out", frag)
		}
	}
}

// The -If combinator must stay paired with the error state and only
// with it. v0.8.51 was exactly this class of catch: argMaxMerge over a
// state built by argMaxIfState (or the reverse) reads a value that
// looks plausible and is not the one asked for.
func TestEndpointExemplarCombinatorsArePaired(t *testing.T) {
	src := mustReadSource(t, "endpoints.go")

	re := regexp.MustCompile(`argMax(If)?Merge(State)?\((\w+)\)`)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		hasIf, state := m[1] == "If", m[3]
		isError := strings.Contains(state, "error") || strings.Contains(state, "err_")
		if isError != hasIf {
			t.Errorf("%s: the -If combinator must pair with the ERROR state and nothing else "+
				"(error=%v, -If=%v) — mismatched combinators return a plausible wrong trace",
				m[0], isError, hasIf)
		}
	}
}
