// inboxUrl — pure codec for the /inbox multi-select facets (priority + kind)
// as URL params (v0.8.291, Option B Slice 2). The inbox moves off localStorage
// onto the URL so a triage view is shareable (Copy link reproduces the exact
// filter), per the house "URL = source of truth" rule. Kept pure so the
// default handling + round-trip is unit-tested away from React.

// decodeCsvSet parses a comma-separated URL param into an ordered, de-duped
// list restricted to `allowed`. A null/empty/all-invalid value falls back to
// `dflt` (so a fresh /inbox lands on the intended default, e.g. P1+P2), which
// is what makes an absent param mean "the default view", not "nothing".
export function decodeCsvSet(raw: string | null, allowed: readonly string[], dflt: readonly string[]): string[] {
  const allow = new Set(allowed);
  const seen = new Set<string>();
  const out: string[] = [];
  for (const tok of (raw ?? '').split(',')) {
    const t = tok.trim();
    if (t && allow.has(t) && !seen.has(t)) {
      seen.add(t);
      out.push(t);
    }
  }
  return out.length > 0 ? out : [...dflt];
}

// encodeCsvSet serializes a selected set back to a canonical comma string in
// `allowed` order (stable, so the URL doesn't churn on selection order).
// Returns null when the selection equals the default — the caller then DELETES
// the param, keeping a default view's link clean (no ?prio=P1,P2,P3 noise).
export function encodeCsvSet(values: Iterable<string>, allowed: readonly string[], dflt: readonly string[]): string | null {
  const have = new Set(values);
  const ordered = allowed.filter(a => have.has(a));
  const dfltOrdered = allowed.filter(a => new Set(dflt).has(a));
  if (ordered.length === dfltOrdered.length && ordered.every((v, i) => v === dfltOrdered[i])) {
    return null;
  }
  return ordered.join(',');
}
