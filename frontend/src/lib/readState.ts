// readState — v0.9.858 (UX denetimi K6). The tri-state read contract, as one
// function instead of a hand-written conditional per surface.
//
// Coremetry's convention for a fetched collection is:
//
//   undefined → still loading
//   null      → the read FAILED
//   value     → the read succeeded (possibly with zero rows)
//
// The convention was already documented and honoured on ~30 surfaces. The bug
// the audit found is that ~12 others wrote the empty guard as
//
//   {data !== undefined && (!data || data.length === 0) && <Empty …/>}
//
// which is a NULL-SWALLOWING shape: `!data` is true for the failure case too,
// so a failed read renders the empty state. On /services that empty state said
// "No services yet — point your OTLP exporter at the collector", i.e. a
// backend outage was reported to the operator as an instrumentation gap. On
// /problems the analogous collapse read as "no exceptions, we're clean".
//
// Writing the guard by hand is what made the mistake available; the four
// states are mutually exclusive and mechanical, so they belong in one place.
export type ReadState = 'loading' | 'error' | 'empty' | 'ready';

export function readState(v: { length: number } | null | undefined): ReadState {
  if (v === undefined) return 'loading';
  // Order matters: null must be tested BEFORE any truthiness/length check.
  // Every instance of this bug was a guard that reached for `.length` (or
  // `!v`) first and let null fall into the empty branch.
  if (v === null) return 'error';
  return v.length === 0 ? 'empty' : 'ready';
}
