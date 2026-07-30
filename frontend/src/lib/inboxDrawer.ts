// Pure seams for the /inbox in-place triage drawer (v0.8.292, Option B slice
// 3). The drawer JSX has no unit seam; these helpers do, so the action matrix,
// the RootCauseRibbon anchor, the ?item= resolution and the silence-body build
// are pinned by lib/inboxDrawer.test.ts.

import type { InboxItem, InboxKind } from './types';

// InboxActionMatrix — which inline triage affordances a kind exposes. Problems
// acknowledge/assign (api.acknowledgeProblems / api.setProblemAssignee),
// anomalies mute (api.createAnomalySilence). Only problem + anomaly have a
// root-cause fan-out endpoint. Every kind keeps the deep-link.
//
// v0.9.254 — `setState` added for exceptions. The old comment here claimed
// exceptions had "neither an rc endpoint nor a mutation"; the rc half is still
// true, the mutation half was never true — POST /api/exception-groups/{fp}/state
// has existed all along (api.go:704). Because the drawer never exposed it, the
// ONLY place to un-ignore a silenced group was the /problems page's Ignored
// tab, which made ignoring effectively irreversible from the unified inbox.
export interface InboxActionMatrix {
  acknowledge: boolean;
  assign: boolean;
  mute: boolean;
  rootCause: boolean;
  openSource: boolean;
  /** Exception groups: resolve / ignore / un-ignore / reopen. */
  setState: boolean;
}

export function inboxActionsForKind(kind: InboxKind): InboxActionMatrix {
  return {
    acknowledge: kind === 'problem',
    assign: kind === 'problem',
    mute: kind === 'anomaly',
    rootCause: kind === 'problem' || kind === 'anomaly',
    openSource: true,
    setState: kind === 'exception' || kind === 'httperror',
  };
}

/**
 * exceptionStateActions — which state transitions to offer for a group in
 * `state`. Deliberately NOT a free-form dropdown: the transitions an operator
 * actually performs are few, and a select listing the current state as an
 * option invites a no-op write plus an audit row that says nothing happened.
 *
 * An unknown/absent state falls back to the full set rather than to none —
 * being unable to act on a row is worse than offering one redundant button,
 * and this is the path a silenced group reaches when the pivot is wrong.
 */
export function exceptionStateActions(state?: string): Array<{ to: string; label: string }> {
  const RESOLVE = { to: 'resolved', label: 'Resolve' };
  const IGNORE = { to: 'ignored', label: 'Ignore' };
  const REOPEN = { to: 'new', label: 'Reopen' };
  const UNIGNORE = { to: 'new', label: 'Un-ignore' };
  const ACK = { to: 'acknowledged', label: 'Acknowledge' };
  switch (state) {
    case 'ignored':    return [UNIGNORE];
    case 'resolved':   return [REOPEN];
    case 'acknowledged': return [RESOLVE, IGNORE];
    case 'new':
    case 'regressed':  return [ACK, RESOLVE, IGNORE];
    default:           return [ACK, RESOLVE, IGNORE, REOPEN];
  }
}

// rootCauseAnchor — the anchor RootCauseRibbon needs, keyed on the NATIVE
// problem/anomaly id (it.problem.id / it.anomaly.id), never the composite inbox
// id ("<kind>:<nativeId>"). null when the kind has no fan-out or the sub-object
// is absent (→ the drawer shows the exception detail / a soft state instead).
export function rootCauseAnchor(it: InboxItem): { anchor: 'problem' | 'anomaly'; id: string } | null {
  if (it.kind === 'problem' && it.problem) return { anchor: 'problem', id: it.problem.id };
  if (it.kind === 'anomaly' && it.anomaly) return { anchor: 'anomaly', id: it.anomaly.id };
  return null;
}

// resolveSelectedItem — derive the drawer's selection from ?item=<id> against
// the loaded inbox list. undefined when the id is absent, the list hasn't
// loaded (undefined) / errored (null), or the id isn't in the current view —
// the caller renders a soft fallback in that last case so a stale deep-link
// doesn't blank the drawer.
export function resolveSelectedItem(
  items: InboxItem[] | null | undefined,
  id: string | null,
): InboxItem | undefined {
  if (!id || !items) return undefined;
  return items.find(it => it.id === id);
}

// AnomalySilenceBody — the createAnomalySilence request shape (mirrors
// api.createAnomalySilence minus the reason). Kept local so the pure builder
// doesn't import the api module.
export interface AnomalySilenceBody {
  fingerprint: string;
  kind: string;
  pattern: string;
  service: string;
  durationSec: number;
}

// buildAnomalySilenceBody — the mute body from an anomaly inbox item, mirroring
// lib/actions.ts' silence-anomaly action: fingerprint = the anomaly's native
// id, (kind, pattern) from the anomaly sub-object, service from the item. null
// for any non-anomaly kind or a missing sub-object (guards the mute button).
export function buildAnomalySilenceBody(it: InboxItem, durationSec: number): AnomalySilenceBody | null {
  if (it.kind !== 'anomaly' || !it.anomaly) return null;
  return {
    fingerprint: it.anomaly.id,
    kind: it.anomaly.kind,
    pattern: it.anomaly.pattern,
    service: it.service,
    durationSec,
  };
}
