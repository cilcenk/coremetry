// recentServices (v0.7.89) — client-side recently-viewed + pinned
// service tracking, the foundation of the cross-signal "pivot mesh":
// an operator works a rotation of ~5-6 services during an incident and
// re-navigates to them constantly. Surfaced in the Cmd-K empty state
// (and pickers) so that rotation is one keystroke, not a re-search.
//
// localStorage-only, no server round-trip — this is per-browser
// convenience state, not shared/team state (those live in saved_views).
// Both lists are plain string[] of service names; the consumer builds
// the /service?name= deep-link.

const RECENT_KEY = 'coremetry.recentServices';
const PINNED_KEY = 'coremetry.pinnedServices';
const RECENT_CAP = 12;

function read(key: string): string[] {
  try {
    const s = localStorage.getItem(key);
    const v = s ? JSON.parse(s) : [];
    return Array.isArray(v) ? v.filter(x => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

function write(key: string, v: string[]): void {
  try {
    localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* quota / disabled storage — recents are best-effort */
  }
}

// recordServiceVisit pushes a service to the front of the MRU list
// (deduped), capped at RECENT_CAP. Call it when a service detail view
// loads. No-op for empty names.
export function recordServiceVisit(name: string): void {
  if (!name) return;
  const next = [name, ...read(RECENT_KEY).filter(n => n !== name)].slice(0, RECENT_CAP);
  write(RECENT_KEY, next);
}

export function getRecentServices(): string[] {
  return read(RECENT_KEY);
}

export function getPinnedServices(): string[] {
  return read(PINNED_KEY);
}

export function isServicePinned(name: string): boolean {
  return read(PINNED_KEY).includes(name);
}

// toggleServicePin flips a service's pinned state and returns the new
// state (true = now pinned). Pinned services are an unbounded ordered
// set (operators pin a handful); newest pin goes to the front.
export function toggleServicePin(name: string): boolean {
  if (!name) return false;
  const cur = read(PINNED_KEY);
  const i = cur.indexOf(name);
  if (i >= 0) {
    cur.splice(i, 1);
    write(PINNED_KEY, cur);
    return false;
  }
  write(PINNED_KEY, [name, ...cur]);
  return true;
}
