// problemLink — URL param sync for the /problems triage drawer.
// v0.8.256 (operator-reported): a specific problem couldn't be
// shared as a link — the drawer READ ?problem=<id> on mount but
// never wrote it back on open/close, so the address bar stayed
// /problems. Pure helper so the both-ways contract is unit-tested.

// Returns a copy of `prev` with ?problem= set (drawer open) or
// removed (drawer closed). Every other param is preserved — the
// drawer must not clobber filters/range in the same URL.
export function withProblemParam(prev: URLSearchParams, id: string | null): URLSearchParams {
  const p = new URLSearchParams(prev);
  if (id) p.set('problem', id);
  else p.delete('problem');
  return p;
}

// withExcParam — same both-ways contract as withProblemParam, for the
// exception-group full detail view. Its open used to be local React
// state only (never written to the URL), so an exception-group
// problem couldn't be shared as a link the way an alert-rule problem
// already could — the exact v0.8.256 bug, left over on this sibling
// surface. Distinct param (`exc`, not `exception`) so it can't
// collide with the existing ?exception=<fp> inline-expand seed used
// by Inbox.tsx / DetailDrawer.tsx deep links.
export function withExcParam(prev: URLSearchParams, fingerprint: string | null): URLSearchParams {
  const p = new URLSearchParams(prev);
  if (fingerprint) p.set('exc', fingerprint);
  else p.delete('exc');
  return p;
}
