package api

import "strings"

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
