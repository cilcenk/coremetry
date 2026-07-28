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

// envKeepsRow reports whether a triage row — identified by its
// service — survives the global env filter (v0.8.387, env-separation
// Phase 3). Semantics mirror chstore.applyEnvServiceScope exactly, so
// the /inbox merged-list filter and the /problems SQL conjunct cannot
// drift:
//
//   - service == "" always survives: global (log-query) monitors are
//     env-unattributable; hiding a firing global alert because the
//     operator narrowed to uat is a triage hazard.
//   - otherwise the service must be a member of the selected env per
//     the 60s-cached 1h service→env map (multi-env services are
//     members of every env they run in, so their rows still show).
//
// Pure + table-tested. Callers only invoke it when an env IS selected
// and the member set resolved successfully (error path = unfiltered).
func envKeepsRow(service string, members map[string]bool) bool {
	return service == "" || members[service]
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

// problemScanLimit decides how many problem rows to pull from ClickHouse
// before the read-time narrows run.
//
// v0.9.342 — priority is the one /api/problems filter that genuinely cannot
// move into SQL: Problem.Priority is computed by EnrichProblemsWithPriority
// from the enriched value/threshold/deploy/status, not stored on the row. So
// the narrow has to run in Go, and the only honest way to keep the LIMIT
// meaningful is to give it more candidates than the page needs.
//
// Without this, picking P1 returned "the P1s among the newest 100 problems"
// rather than "the newest 100 P1s". On an install with ~800 open problems the
// two are very different answers, and nothing on the page distinguished them.
//
// The multiple is deliberately modest: problems is a small ReplacingMergeTree
// state table read with FINAL, but the enrichment chain that follows
// (runbooks, teams, clusters, deploys, root-cause) runs over whatever comes
// back, so an unbounded widening would move the cost into those batch reads.
// 5× covers a page whose selected priorities are a fifth of the population —
// well past the observed skew, where P1+P2 dominate.
func problemScanLimit(pageLimit int, narrowed bool) int {
	if pageLimit <= 0 {
		pageLimit = 100
	}
	if !narrowed {
		return pageLimit
	}
	n := pageLimit * 5
	if n > problemScanCeiling {
		n = problemScanCeiling
	}
	return n
}

// problemScanCeiling bounds the widened scan so a large page size cannot turn
// into an unbounded read of the problems table.
const problemScanCeiling = 2000

// intersectServices ANDs two service-set constraints.
//
// nil means "no constraint from this axis", which is why it cannot be treated
// as an empty set: intersecting with nil must leave the other side untouched.
// An EMPTY (non-nil) result is meaningful and preserved — it means the axes
// have no service in common, and the page must then be empty rather than
// unfiltered.
func intersectServices(a, b []string) []string {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	inB := make(map[string]bool, len(b))
	for _, s := range b {
		inB[s] = true
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if inB[s] {
			out = append(out, s)
		}
	}
	return out
}
