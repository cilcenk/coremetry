package api

import (
	"sort"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// matchesTeamFilter reports whether a problem row — identified by
// its owning team (rowOwner) + SRE/reliability team (rowSRE),
// read-time enriched from the service catalog — survives the
// owner/SRE team filter the operator selected on /problems.
//
// Semantics are copied verbatim from the inbox filter
// (internal/api/inbox.go): an empty filter value means "all" (that
// axis does not narrow); a set value keeps only rows whose
// corresponding team matches case-insensitively (strings.EqualFold),
// so a URL / link paste between dashboards or chat doesn't
// false-negative on a capitalisation mismatch. The two axes AND
// together — "owned by X AND on-call'd by Y".
//
// v0.8.290 — extracted as a pure predicate so every branch (empty,
// match, mismatch, case-fold, both axes) is table-tested. Backs the
// operator-reported "filter problems by owner/SRE team like the
// Services page" request; MUST behave identically to the inbox
// filter it mirrors.
func matchesTeamFilter(rowOwner, rowSRE, wantOwner, wantSRE string) bool {
	if wantOwner != "" && !strings.EqualFold(rowOwner, wantOwner) {
		return false
	}
	if wantSRE != "" && !strings.EqualFold(rowSRE, wantSRE) {
		return false
	}
	return true
}

// servicesForTeam resolves an owner/SRE team pick to the sorted set of
// services that belong to it, per the operator-curated catalog `mds`
// (keyed by service). It reuses matchesTeamFilter so the resolution is
// bit-identical to the /problems and inbox team filters.
//
// Returns nil when NEITHER axis is set — caller treats nil as "no team
// constraint". Returns a non-nil, possibly-empty slice when a team IS
// set but no service matches — caller renders that as an empty result,
// NOT an unfiltered one (the distinction nil vs empty is load-bearing).
//
// v0.8.310 — backs the owner/SRE team filter on the Problems inbox.
// The inbox is server-paginated, so team-filtering must happen in SQL
// (service IN (…)); resolving team→services here keeps that query a
// simple set membership instead of a catalog JOIN with its own FINAL /
// distributed concerns.
func servicesForTeam(mds map[string]chstore.ServiceMetadata, wantOwner, wantSRE string) []string {
	if wantOwner == "" && wantSRE == "" {
		return nil
	}
	out := make([]string, 0, len(mds))
	for svc, md := range mds {
		if matchesTeamFilter(md.OwnerTeam, md.SRETeam, wantOwner, wantSRE) {
			out = append(out, svc)
		}
	}
	sort.Strings(out)
	return out
}
