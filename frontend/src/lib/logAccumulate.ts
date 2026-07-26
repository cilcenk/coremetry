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
