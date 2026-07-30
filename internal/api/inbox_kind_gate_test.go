package api

import (
	"strings"
	"testing"
)

// inbox_kind_gate_test.go — v0.9.354, the second half of the operator's
// report ("exceptions/problems filtrelerini seçince çok yavaş geliyor ya da
// gelmiyor").
//
// Selecting a kind chip used to make the request SLOWER, not narrower: any
// non-default kind selection set `narrowed`, which widened the scan to 2000
// problems, ran all four enrichers over them — and then applyInboxFacets
// threw every problem row away because the operator had asked for exceptions
// only. The most expensive work in the handler produced rows whose only fate
// was the bin.
//
// And the DEFAULT landing view (kind=exception since v0.9.328) was paying the
// same tax on every poll.
func TestKindFacetGatesExpensiveSources(t *testing.T) {
	src := readSrc(t, "inbox.go")

	if !strings.Contains(src, `if statusFilter != "ignored" && kindOn["problem"] {`) {
		t.Error("problems fetch is not gated on the kind facet — a kind-narrowed request still pays fetch + four enrichers for rows it will discard")
	}
	// v0.9.443 — the exception store serves TWO kinds (exception +
	// httperror); the fetch gate is their union, the class split rides
	// ExceptionGroupFilter.HTTPErrors.
	if !strings.Contains(src, `if !teamIsEmpty && (excOn || httpOn) {`) {
		t.Error("exception-family fetch is not gated on the kind facet")
	}
	// Deselected kinds keep an EXACT chip via COUNT queries — the v0.9.330
	// contract ("chips report what exists") without the fetch.
	if !strings.Contains(src, `skippedCounts["problem"] = int(n)`) ||
		!strings.Contains(src, `skippedCounts["exception"] = int(n)`) ||
		!strings.Contains(src, `skippedCounts["httperror"] = int(n)`) {
		t.Error("skipped kinds must still get chip counts from the cheap COUNT path — a zero chip would reintroduce the v0.9.330 'Exceptions 0' lie")
	}
	for _, frag := range []string{
		`for k, n := range skippedCounts {`,
		`counts[k] = n`,
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("skipped-kind counts are not merged into the response counts (missing %q)", frag)
		}
	}
	// Anomalies and incidents stay ALWAYS-fetched by design: small FINAL
	// state tables with no enrichment, and fetching keeps their chip counts
	// exact from rows. If someone gates them later, their counts need the
	// same COUNT-query treatment.
	if strings.Contains(src, `kindOn["anomaly"]`) || strings.Contains(src, `kindOn["incident"]`) {
		t.Error("anomalies/incidents were gated without moving their chip counts to the COUNT path")
	}
}
