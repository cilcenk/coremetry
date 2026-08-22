import type { TimeRange } from './types';
import { PRESET_SECONDS } from './utils';
import { decodeRange, encodeRange } from './urlState';
import { resolveRangeMs } from './rangePicker';

// ─────────────────────────────────────────────────────────────────────────────
// shareUrl — v0.9.1280. What a shared link MEANS a day later.
//
// The bug: ShareButton copied `window.location.href` verbatim, so an incident
// link carrying a RELATIVE preset (`?range=1h`) shows a different hour to
// whoever opens it tomorrow. The evidence the sender pointed at is gone, and
// nothing on the receiving page says so — the worst kind of quiet wrong.
//
// The fix: freeze the window at copy time. `?range=1h` becomes
// `?range=custom:<fromMs>-<toMs>` resolved against the sender's clock. The
// preset contract is NOT re-derived here: resolveRangeMs (rangePicker.ts) is
// the one resolver, PRESET_SECONDS the one preset list, encodeRange
// (urlState.ts) the one token speller. `custom:` is MILLISECONDS — same unit
// logsRangeParam and navHref emit.
//
// Deliberately narrow:
//   • No `range` param → the href comes back byte-identical. A page that does
//     not read a window (Trace detail) must not grow one; injecting a param a
//     page ignores is how dead params get cargo-culted into the next builder.
//   • Already `custom:` → untouched, ORIGINAL bytes. Re-emitting it would
//     normalise `custom%3A…` (what navHref writes) into `custom:…` for no gain.
//   • Unrecognised preset → untouched. resolveRangeMs falls back to 24h for
//     anything it doesn't know, so blindly resolving would mint a confident
//     absolute window out of a token we never understood — the same principle
//     logsRangeParam applies when it refuses to emit a rejectable token.
//
// Out of scope (operator call): no "copy relative" second option. The reported
// class is the opposite one; anyone who wants the live window copies the
// address bar.
// ─────────────────────────────────────────────────────────────────────────────

/** Fallback that no preset and no valid `custom:` token can produce, so the
 *  "decodeRange gave up" case is distinguishable from a real range. */
const UNRESOLVED: TimeRange = { preset: '' };

/** Decode one raw query token the way URLSearchParams would, without throwing
 *  on a malformed `%` escape (a hand-edited URL is still copyable). */
function decodeToken(raw: string): string {
  try {
    return decodeURIComponent(raw.replace(/\+/g, ' '));
  } catch {
    return raw;
  }
}

/** The absolute replacement for a `range=` value, or null to leave it alone. */
function frozenRangeValue(value: string, nowMs: number): string | null {
  const r = decodeRange(value, UNRESOLVED);
  // The resolvable set is "the 11 presets ∪ custom" — decodeRange already
  // turned a well-formed `custom:` token into absolute bounds resolveRangeMs
  // returns verbatim. Anything else (unknown preset, malformed custom, an
  // Object.prototype key like `constructor`) is not ours to interpret.
  const resolvable = r.preset === 'custom' ||
    Object.prototype.hasOwnProperty.call(PRESET_SECONDS, r.preset);
  if (!resolvable) return null;
  // Already absolute → nothing to freeze, and rewriting would only re-spell it.
  if (r.preset === 'custom') return null;
  const { fromMs, toMs } = resolveRangeMs(r, nowMs);
  const from = Math.floor(fromMs);
  const to = Math.ceil(toMs);
  // Mirror decodeRange's acceptance test: never emit a token the reader would
  // reject and silently replace with the sticky window.
  if (!(from > 0) || !(to > from)) return null;
  return encodeRange({ preset: 'custom', fromMs: from, toMs: to });
}

/**
 * Pin the time window of a shareable href to absolute milliseconds.
 *
 * Rewrites ONLY the `range` param and only when it carries a relative preset;
 * every other param keeps its exact original bytes and position, and the
 * fragment rides along untouched. Returns `href` itself when there is nothing
 * to freeze.
 */
export function absoluteShareHref(href: string, nowMs: number): string {
  if (!Number.isFinite(nowMs)) return href;
  const hashAt = href.indexOf('#');
  const head = hashAt < 0 ? href : href.slice(0, hashAt);
  const hash = hashAt < 0 ? '' : href.slice(hashAt);
  const qAt = head.indexOf('?');
  if (qAt < 0) return href;
  let changed = false;
  const pairs = head.slice(qAt + 1).split('&').map(pair => {
    const eq = pair.indexOf('=');
    if (eq < 0) return pair;
    const rawKey = pair.slice(0, eq);
    if (decodeToken(rawKey) !== 'range') return pair;
    const token = frozenRangeValue(decodeToken(pair.slice(eq + 1)), nowMs);
    if (token === null) return pair;
    changed = true;
    return `${rawKey}=${token}`;
  });
  return changed ? `${head.slice(0, qAt)}?${pairs.join('&')}${hash}` : href;
}
