package chstore

import (
	"context"
	"time"
)

// service_seen.go — v0.9.1317, entity-model slice A2
// (docs/audit/entity-model-audit-2026-08-23.md §7.2).
//
// Read side of the service_seen MV: the first_seen / last_seen pair that
// gives a service a lifecycle instead of a presence check.
//
// ── The honesty problem this file exists to solve ────────────────────
//
// A materialized view only populates from inserts made AFTER it exists.
// On a live install every service therefore shows first_seen ≈ the moment
// this feature was deployed — a date that is precise, plausible, and
// WRONG. An operator reading "first seen: today" for a service that has
// run for two years would act on it.
//
// This is not a new problem here. v0.9.249 hit exactly it with
// service_version_5m and solved it the same way: probe the MV's OWN
// earliest datum and refuse to answer for windows it cannot cover
// (deployMVCovers, deploys.go). The reasoning transfers verbatim, so the
// mechanism does too.
//
// Why no backfill instead. Both available sources would relabel the lie
// rather than remove it:
//   - `spans` reaches back only to the spans retention (30 days by
//     default), so first_seen would mean "first seen in the last 30
//     days"; and the backfill itself is a GROUP BY over the full spans
//     history, at boot, on EVERY pod of a rolling deploy — slice A2
//     explicitly buys no new worker and no new leader lock, so there is
//     nowhere to run it once.
//   - `service_summary_5m` is cheap to scan but has a 90-day TTL, so it
//     moves the censoring boundary from "deploy time" to "90 days" and
//     hides it behind a bucket label.
//
// There is no source that knows when a service was first seen before we
// started watching. So the read side says so: a first_seen that sits at
// the MV's own left edge is reported as UNKNOWN, and the wire format
// carries no date at all in that case (see the api layer) — a value the
// UI cannot render is a value the UI cannot fabricate.
//
// last_seen has no such problem and is correct from the first insert.
// That is the half the operator actually sees on /services.

// ServiceSeen is one service's lifecycle pair, merged from the MV states.
type ServiceSeen struct {
	FirstSeen time.Time
	LastSeen  time.Time
}

// ServiceSeenGrace — how far past the MV's own earliest datum a first_seen
// must sit before we are willing to call it a birth.
//
// The floor is the earliest observation the MV holds at all, but inserts
// arrive in async_insert batches and a rolling deploy creates the MV on
// several pods within a minute or two, so the services that were ALREADY
// running do not all land on the exact same nanosecond — they land in a
// short smear after it. Anything inside that smear is indistinguishable
// from a service that happened to start seconds after we began watching,
// so it is reported unknown. The error direction is deliberate: this
// window trades a few genuinely-new services being called "unknown" for
// never printing a fabricated birth date.
const ServiceSeenGrace = 5 * time.Minute

// serviceSeenSQL — hoisted as a const so service_seen_test.go can pin the
// bounds and WHICH states the read trusts, the way deployMVCoverageProbeSQL
// does for the deploy gate (v0.9.250).
//
// No time-bounded WHERE, and that is the point of the table rather than an
// oversight: service_seen has no time dimension to bound on, and a
// disappeared service is exactly the row a time filter would drop. The
// scan is safe without one because the MV holds one row per service — the
// LIMIT is a cardinality circuit-breaker, not a page size.
const serviceSeenSQL = `
	SELECT service_name,
	       minMerge(first_seen_state) AS first_seen,
	       maxMerge(last_seen_state)  AS last_seen
	FROM service_seen
	GROUP BY service_name
	LIMIT 100000
	SETTINGS max_execution_time = 5, `

// GetServiceSeen returns the lifecycle pair for every service the MV has
// ever observed, keyed by service name.
//
// Whole-table on purpose: the result is identical for every caller, page,
// filter and env, which is what lets the api layer cache one snapshot
// process-wide (the openProblemCountsCached shape). It is also what makes
// the floor meaningful — a filtered read would compute the earliest datum
// of a SUBSET and censor the wrong services.
func (s *Store) GetServiceSeen(ctx context.Context) (map[string]ServiceSeen, error) {
	rows, err := s.telemetryReadConn().Query(ctx, serviceSeenSQL+s.shardSkipSetting())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ServiceSeen{}
	for rows.Next() {
		var name string
		var first, last time.Time
		if err := rows.Scan(&name, &first, &last); err != nil {
			return nil, err
		}
		out[name] = ServiceSeen{FirstSeen: first, LastSeen: last}
	}
	return out, rows.Err()
}

// ServiceSeenFloor returns the earliest first_seen in the snapshot — in
// practice the moment the MV itself started observing.
//
// Zero when the map is empty or holds nothing but zero timestamps, which
// FirstSeenIsKnown then reads as "we cannot prove anything about any
// service", closing the gate rather than opening it.
func ServiceSeenFloor(m map[string]ServiceSeen) time.Time {
	var floor time.Time
	for _, v := range m {
		if v.FirstSeen.IsZero() {
			continue
		}
		if floor.IsZero() || v.FirstSeen.Before(floor) {
			floor = v.FirstSeen
		}
	}
	return floor
}

// FirstSeenIsKnown reports whether firstSeen is a real observation of a
// service's birth, rather than the left edge of the MV's own history.
//
// Every uncertain case answers false. An unknown floor cannot license a
// claim; a zero first_seen is not a date; and a first_seen at or before
// the floor (clock skew across shards can produce one) is by definition
// not something we watched happen.
func FirstSeenIsKnown(firstSeen, floor time.Time, grace time.Duration) bool {
	if firstSeen.IsZero() || floor.IsZero() {
		return false
	}
	return firstSeen.After(floor.Add(grace))
}
