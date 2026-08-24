import { windowRangeParam } from '@/lib/urlState';
import type { TimeRange } from '@/lib/types';

// traceHref — v0.9.1347. The single builder for a `/trace?id=…` link.
//
// 38 hand-written `/trace?id=` strings across 22 files, each re-spelling a
// param contract that lives in exactly one place (pages/Trace.tsx:42-81):
// `id`, `span`, `tab=logs`, `xn=1`. Nothing else is read. A param that is
// not on that list is not merely ignored — see the DEAD PARAM note below.
//
// ── WHY THE WINDOW IS OPTIONAL HERE, UNLIKE THE REST OF THE FAMILY ────────
//
// tracesPivotHref and logsHref make the window a REQUIRED argument because
// their destinations QUERY by it: drop the range and /traces or /logs answers
// over the sticky window and renders an empty list, which reads as "there is
// nothing here" rather than "you are looking at the wrong hour". That bug has
// shipped six times (v0.9.208, v0.9.213 ×3, v0.9.853, v0.9.1324).
//
// /trace is NOT that shape, and the difference was measured before this file
// was written rather than assumed:
//
//   • The trace is fetched by ID (Trace.tsx:42). The window does not select,
//     filter or bound it. A windowless /trace link renders the SAME page.
//   • The Logs tab derives its own window from the trace's span timestamps
//     (Trace.tsx:418, logsRangeParam) — not from `?range=`.
//   • `?range=` reaches exactly one consumer: the Topbar picker
//     (Trace.tsx:44 useUrlRange('30m'), rendered at :339/:360). From there it
//     rides onward to the next page.
//   • And the common cases already work WITHOUT this builder. useUrlRange
//     resolves `?range=` → sticky → default, and since v0.9.937 the sticky
//     session channel holds ABSOLUTE windows too. So an operator who brushed
//     a chart and clicked through to a trace already keeps that window — the
//     hook even writes it back into the /trace URL (useUrlRange.ts:159-165).
//
// What is left is cosmetic: a page-DEFAULT preset (never an explicit pick, so
// never in the sticky channel) is not inherited, and /trace shows its own
// 30m. Forcing 38 call sites to thread a window through props to correct a
// Topbar label would be churn — and worse, it would invite the regression in
// the next paragraph.
//
// ── THE RULE THAT IS ENFORCED AT THE TYPE LEVEL ──────────────────────────
//
// `pageRange` accepts `TimeRange | string`, and DELIBERATELY NOT the
// `{ fromNs, toNs }` event-window shape the rest of the href family takes.
//
// The mistake this forbids: reaching for the pivotHref habit and passing the
// EXEMPLAR'S OWN window — the trace's duration. A trace lasts milliseconds to
// seconds. Pinning `range=custom:<2 seconds>` onto /trace is not a harmless
// label: navHref then carries that absolute window to the NEXT page
// (navHref.ts:47 carries `custom:` and only `custom:`), so the operator's
// following hop opens on a two-second window and shows nothing. The window
// that belongs on /trace is the one the operator is standing in — the PAGE's
// range — and a page range is always a TimeRange (from useUrlRange) or an
// already-encoded string (from `params.get('range')`). The event-window shape
// simply does not compile here.
//
// A caller that genuinely holds the page window only as ns bounds can still
// pass `windowRangeParam({ fromNs, toNs })` through the string branch. That is
// deliberate: the escape hatch is explicit and greppable, not accidental.
//
// ── DEAD PARAM (the failure a producer + gate actually prevents) ──────────
//
// /trace mirrors its state to the URL with a RAW history.replaceState over
// `new URL(window.location.href)` (Trace.tsx:161-171). It touches only
// span/tab/xn, so foreign params survive — unlike /traces, whose whitelist
// rebuild DELETED `?operation=` on the first state write and turned a wrong
// link into a clean-looking one (v0.9.855). The /trace equivalent is quieter
// but the same class: `?traceId=`, `?trace=`, `?from=/&to=` all ride along
// looking authoritative while the page reads none of them. This builder plus
// its source gate make the readable set the only spellable set.
export interface TraceHrefOpts {
  /**
   * The window to carry onward, as the Topbar picker's value.
   *
   * This is the PAGE's range, never the trace's own duration — see the file
   * comment. `{ fromNs, toNs }` is excluded from the type on purpose.
   * Omit (or pass null) when the surface has no page range: ⌘K's typed trace
   * id, a chat-bubble linkification, a permalink. The destination then
   * self-resolves through useUrlRange's sticky chain, which is the honest
   * answer rather than a fabricated window.
   */
  pageRange?: TimeRange | string | null;
  /** Deep-link to one span (`?span=`). */
  span?: string | null;
  /** Open on the Logs tab rather than the waterfall. */
  tab?: 'logs' | null;
  /** Group similar sibling spans — /trace's `?xn=1` (v0.9.1277). */
  groupSimilar?: boolean;
}

/**
 * Encode a PAGE range as the `range=` token /trace's Topbar understands.
 *
 * Returns '' for anything decodeRange would not honour, so the caller emits
 * no param at all. A bare `custom` (preset 'custom' with no bounds) is the
 * one such value that reaches here in practice: encodeRange emits the literal
 * 'custom', decodeRange hands back `{ preset: 'custom' }` with no bounds, and
 * timeRangeToNs then silently resolves it to its 86400s fallback — a
 * confident-looking 24h window nobody asked for (utils.ts:17-25).
 */
function pageRangeParam(r: NonNullable<TraceHrefOpts['pageRange']>): string {
  if (typeof r === 'string') return r === 'custom' ? '' : r;
  // Runtime belt for the type-level rule above. The type already rejects an
  // { fromNs, toNs } event window, but a cast — or a caller reached from
  // untyped JS — would otherwise sail straight into windowRangeParam's ns
  // branch and pin the trace's own two-second duration as the page range.
  // A shape with no `preset` is not a page range, so it emits nothing.
  if (!('preset' in r)) return '';
  if (r.preset === 'custom' && !(r.fromMs && r.toMs)) return '';
  // windowRangeParam stays in the path even though only its preset branch is
  // reachable here: it is this repo's single window→`range=` producer
  // (urlState.ts:28), and a builder that bypasses it is how the floor/ceil
  // and acceptance rules drift out of one file again.
  return windowRangeParam(r);
}

export function traceHref(id: string, opts: TraceHrefOpts = {}): string {
  const q = new URLSearchParams();
  // `id` first: every existing hand-written site spells `/trace?id=…`, so
  // keeping the order means converting a site does not change the URL an
  // operator has bookmarked or a test has pinned.
  q.set('id', id);
  if (opts.span) q.set('span', opts.span);
  if (opts.tab === 'logs') q.set('tab', 'logs');
  if (opts.groupSimilar) q.set('xn', '1');
  if (opts.pageRange) {
    const enc = pageRangeParam(opts.pageRange);
    if (enc) q.set('range', enc);
  }
  return `/trace?${q.toString()}`;
}

// NO `env` OPTION, deliberately. /trace has no env consumer at all (no
// useUrlEnv, no reader) — env reaches the page only as a foreign param that
// Trace.tsx's replaceState writer happens to preserve. navHref is this repo's
// single owner of env carry (navHref.ts:8-10 says so), and a second writer is
// how two spellings of one contract drift apart. A call site that needs env
// carried wraps this builder: navHref(traceHref(id, …), location.search).
