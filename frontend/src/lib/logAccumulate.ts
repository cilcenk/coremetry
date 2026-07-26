// "Load more" accumulation for the static /logs list (v0.9.292).
//
// Pulled out of the effect that used to hold it inline so the two
// things that can go silently wrong here — losing a row to dedup, or
// losing one to the cap without saying so — are pinned by tests.
//
// The list is newest-first and "Load more" appends OLDER rows below, so
// the buffer slides forward: when it overflows, the front (the newest,
// already scrolled past) is what leaves. The caller surfaces the count;
// rows disappearing unannounced is the failure this exists to prevent.

export interface AccumulateResult<T> {
  rows: T[];
  /** How many rows this merge pushed out of the front of the buffer. */
  dropped: number;
}

/**
 * Append `page` to `prev`, dropping duplicates by id, then trim the
 * front so at most `cap` rows remain.
 *
 * Dedup is by id and not by position: the keyset cursor re-reads its
 * boundary row inclusively, so the first row of a page is routinely one
 * the previous page already carried (v0.7.22).
 */
export function accumulatePage<T extends { id: number | string }>(
  prev: T[],
  page: T[],
  cap: number,
): AccumulateResult<T> {
  const seen = new Set(prev.map(r => r.id));
  const merged = prev.concat(page.filter(r => !seen.has(r.id)));

  // A non-positive cap means "no ceiling" rather than "keep nothing" —
  // an accidental 0 must not silently empty the operator's page.
  if (cap <= 0 || merged.length <= cap) {
    return { rows: merged, dropped: 0 };
  }
  const drop = merged.length - cap;
  return { rows: merged.slice(drop), dropped: drop };
}

/** The row shape narrowLoaded reads. Structural, so it accepts LogRow. */
export interface NarrowableRow {
  body?: string;
  serviceName?: string;
  severityText?: string;
  traceId?: string;
}

/**
 * Narrow ALREADY-LOADED rows by a substring (v0.9.294).
 *
 * This is deliberately client-side and deliberately local: it filters
 * the buffer in the page, it does NOT re-query. That is the whole
 * point — today every narrowing costs a full round trip to
 * Elasticsearch, and at 10B docs/day the cheapest query is the one you
 * do not send. The caller MUST label it as filtering loaded rows, or
 * the operator reads a local subset as a window-wide answer.
 *
 * Matches case-insensitively across the fields the table actually
 * shows, so what the operator sees is what gets searched.
 */
export function narrowLoaded<T extends NarrowableRow>(rows: T[], needle: string): T[] {
  const n = needle.trim().toLowerCase();
  if (!n) return rows;
  return rows.filter(r =>
    (r.body ?? '').toLowerCase().includes(n) ||
    (r.serviceName ?? '').toLowerCase().includes(n) ||
    (r.severityText ?? '').toLowerCase().includes(n) ||
    (r.traceId ?? '').toLowerCase().includes(n));
}
