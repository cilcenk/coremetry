package api

import (
	"strings"
	"testing"
)

// inbox_team_scope_test.go — v0.9.353, operator-reported.
//
// "Problems sayfasında owner ve SRE seçince filtreler çalışmıyor. Ayrıca
// exceptions/problems filtrelerini seçince çok yavaş geliyor ya da gelmiyor."
//
// Both symptoms were one defect. The team filter ran in Go over the merged,
// already-capped scan: an owner pick pulled up to 2000 problems + 1000
// incidents + 1000 exception rows for the WHOLE estate, enriched all of them
// (four CH round-trips over the 2000 problems), and only then dropped other
// teams' rows. On prod that request was slow enough that the page kept
// showing the previous, unfiltered response (keepPreviousData) — which reads
// exactly as "the filter does not work": rows from other teams on screen
// while the dropdown says LordOfCRM.
//
// The fix resolves the team to a service allowlist ONCE (the same
// servicesForTeam the /api/problems handler has used since v0.9.342) and
// pushes it into ALL FOUR source queries, so the LIMIT and the enrichers see
// one team's rows, not the estate's newest N.
func TestInboxTeamFilterReachesEverySource(t *testing.T) {
	src := readSrc(t, "inbox.go")

	for _, want := range []struct{ name, frag string }{
		// One catalog read, hoisted — it used to be fetched twice.
		{"allowlist resolution", "teamServices = servicesForTeam(s.teamAliasesCtx(ctx), mdMap, ownerTeam, sreTeam)"},
		{"problems", "Services: teamServices,\n\t\t\t\tLimit:    srcLimit,"},
		{"exceptions (above floor)", "MinOccurrences: minOcc,\n\t\t\t\tServices:       teamServices,"},
		{"exceptions (below floor)", "MaxOccurrences: minOcc,\n\t\t\t\t\tServices:       teamServices,"},
		{"anomalies", "ActiveOnly: statusFilter == \"open\",\n\t\t\t\t// v0.9.353 — nil = no constraint; empty = match nothing.\n\t\t\t\tServices: teamServices,"},
		{"incidents", "NotStatuses: pickExcludedStatuses(statusFilter), Limit: incLimit,\n\t\t\t\t// v0.9.353 — nil = no constraint; empty = match nothing.\n\t\t\t\tServices: teamServices,"},
	} {
		if !strings.Contains(src, want.frag) {
			t.Errorf("%s: the team allowlist does not reach this source — its rows would be fetched estate-wide and filtered after the LIMIT", want.name)
		}
	}

	// THE TRAP THIS ENCODES: ExceptionGroupFilter.Services treats an EMPTY
	// slice as "no constraint" (v0.8.310), the exact opposite of
	// ProblemFilter.Services (empty = 1=0, v0.9.342). A team resolving to no
	// services must therefore SKIP the exception fetches entirely — passing
	// the empty slice through would return an UNFILTERED page under a filter
	// that should match nothing.
	// v0.9.354 added the kind gate on the same line — the invariant is the
	// teamIsEmpty guard, whatever else joins the condition.
	if !strings.Contains(src, `if !teamIsEmpty && kindOn["exception"] {`) {
		t.Error("empty-team short-circuit missing — an empty team allowlist passed to ExceptionGroupFilter.Services means NO constraint, not 'match nothing'")
	}

	// The Go-side pass stays as the second guard (catalog blip → SQL narrow
	// skipped → old behaviour), same posture as v0.9.342. It must not be the
	// ONLY place the filter applies, but it must still exist.
	if !strings.Contains(src, `if ownerTeam != "" || sreTeam != "" {`) {
		t.Error("the Go-side team pass was removed — a catalog error would now disable the filter entirely instead of degrading")
	}
}

// The two new store filters must agree on the strict contract: nil = no
// constraint, EMPTY = match nothing, and a service-less row does NOT match a
// team (unlike the env narrow's `service='' OR …` escape — env keeps global
// rows, teams do not own them).
func TestTeamAllowlistContractOnStores(t *testing.T) {
	anom := readSrc(t, "../chstore/anomaly_event.go")
	inc := readSrc(t, "../chstore/incident.go")
	for name, src := range map[string]string{"anomaly_event.go": anom, "incident.go": inc} {
		if !strings.Contains(src, `"1 = 0"`) && !strings.Contains(src, `" AND 1 = 0"`) {
			t.Errorf("%s: empty Services must render a match-nothing conjunct", name)
		}
		if !strings.Contains(src, `service IN (`) {
			t.Errorf("%s: Services must render a strict IN — no service='' escape on a team filter", name)
		}
	}
}
