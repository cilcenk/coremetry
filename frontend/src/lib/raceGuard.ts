// raceGuard — v0.9.857 (UX denetimi K7). The two-part stale-response guard
// that every manual `useEffect` + `useState` fetch island in this codebase
// needs, in one testable piece.
//
// The bug it kills: Trace detail's fetch (`useEffect([id])` → `api.trace(id)
// .then(setSpans)`) had NO cancelled flag, no cleanup and no abort. Open a
// big/slow trace A, don't wait, open a small trace B: B resolves first and
// renders, then A's late response overwrites it. The operator is looking at
// B's URL, B's id in the header — and A's waterfall. That is a data-CORRECTNESS
// failure, not a cosmetic one: nothing on screen says the spans belong to
// another trace.
//
// Why BOTH halves (the pairing v0.9.603 documents):
//
//   • `ok()` — the flag. Discards the RESPONSE. Always required: an abort may
//     lose the race with a response already in flight through the microtask
//     queue, and some paths cannot pass a signal at all.
//   • `signal` — real cancellation. Discards the REQUEST. Without it the
//     superseded ClickHouse query keeps running to max_execution_time, and
//     rapid navigation stacks queries that slow down the one the operator is
//     actually waiting for.
//
// Usage mirrors the inline shape it replaces, so the effect still reads like
// the rest of the codebase:
//
//   useEffect(() => {
//     const g = raceGuard();
//     api.trace(id, g.signal)
//       .then(d => { if (g.ok()) setSpans(d.spans); })
//       .catch(() => { if (g.ok()) setSpans(null); });
//     return g.cancel;
//   }, [id]);
//
// The `.catch` guard matters as much as the `.then`: aborting rejects, so an
// unguarded catch turns the operator's own navigation into an error state
// (the CanceledError-vs-timeout distinction, v0.9.603).
export interface RaceGuard {
  /** Pass to the api call so the superseded request is actually cancelled. */
  readonly signal: AbortSignal;
  /** True only while this run is still the newest one. */
  ok(): boolean;
  /** Effect cleanup: marks this run stale AND aborts its request. */
  cancel(): void;
}

export function raceGuard(): RaceGuard {
  const ctl = new AbortController();
  let live = true;
  return {
    signal: ctl.signal,
    ok: () => live,
    cancel: () => {
      // Flag FIRST: cancel() must make ok() false even if abort() throws or
      // the environment lacks AbortController semantics we expect.
      live = false;
      ctl.abort();
    },
  };
}
