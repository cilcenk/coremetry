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

// INBOX_TEAM_PARAM (v0.9.1246) — the single-axis team filter's param NAME,
// written here once because two independent producers have to agree on it:
// this page (reader) and the server-side chat bridge (guidedAnswerLinks,
// copilot_followup.go) that emits /inbox?kind=exception&team=<team>. A link
// that promises a narrowed view while the page reads a DIFFERENT param opens
// the unfiltered queue instead — the K4 dead-param class (v0.9.1130).
export const INBOX_TEAM_PARAM = 'team';

// readInboxTeam — the ?team= facet as the page consumes it.
//
// NO LENGTH OR SHAPE ASSUMPTION (v0.9.1246, operator): real team names are
// short codes like "SY" / "UG", so anything that required 3+ characters or a
// particular casing would silently drop the operator's actual team. Case is
// NOT normalised here on purpose — the server folds it (chstore.NormTeamName
// via TeamEqual) and echoes the catalogue's own spelling into the chip, so
// lowercasing client-side would only make the chip disagree with the catalogue.
// Whitespace-only means "no filter" (a stray "?team=%20" must not narrow to a
// team that cannot exist).
export function readInboxTeam(sp: URLSearchParams): string {
  return (sp.get(INBOX_TEAM_PARAM) ?? '').trim();
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
