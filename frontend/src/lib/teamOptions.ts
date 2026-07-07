// teamOptions — canonical, case-insensitive team option building
// (v0.8.330, operator-reported). ug-team / sy-team resource attrs arrive
// with whatever casing each service's deploy pipeline used ("avengerSY" vs
// "Avengersy"), and the auto-derived catalog stores them verbatim — a
// case-sensitive Set then lists the same team twice in every filter
// dropdown. The backend filter match is already EqualFold
// (servicesForTeam / matchesTeamFilter), so collapsing the OPTIONS is the
// only missing half: one entry per lowercase key, displayed as the most
// frequent original casing (tie → lexicographically first, deterministic),
// sorted case-insensitively.
export function teamOptionsCI(values: Array<string | undefined>): string[] {
  const byKey = new Map<string, Map<string, number>>(); // lower → casing → count
  for (const raw of values) {
    const v = (raw ?? '').trim();
    if (!v) continue;
    const key = v.toLowerCase();
    const casings = byKey.get(key) ?? new Map<string, number>();
    casings.set(v, (casings.get(v) ?? 0) + 1);
    byKey.set(key, casings);
  }
  const out: string[] = [];
  for (const casings of byKey.values()) {
    let best = '';
    let bestN = -1;
    for (const [form, n] of casings) {
      if (n > bestN || (n === bestN && form < best)) {
        best = form;
        bestN = n;
      }
    }
    out.push(best);
  }
  return out.sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
}
