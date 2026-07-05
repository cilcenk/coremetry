// Pure seams for the /inbox in-place triage drawer (v0.8.292, Option B slice
// 3). The drawer JSX has no unit seam; these helpers do, so the action matrix,
// the RootCauseRibbon anchor, the ?item= resolution and the silence-body build
// are pinned by lib/inboxDrawer.test.ts.

import type { InboxItem, InboxKind } from './types';

// InboxActionMatrix — which inline triage affordances a kind exposes. Problems
// acknowledge/assign (api.acknowledgeProblems / api.setProblemAssignee),
// anomalies mute (api.createAnomalySilence). Only problem + anomaly have a
// root-cause fan-out endpoint; exceptions have neither an rc endpoint nor a
// mutation, so they get only the "Open source →" escape hatch. Every kind keeps
// the deep-link.
export interface InboxActionMatrix {
  acknowledge: boolean;
  assign: boolean;
  mute: boolean;
  rootCause: boolean;
  openSource: boolean;
}

export function inboxActionsForKind(kind: InboxKind): InboxActionMatrix {
  return {
    acknowledge: kind === 'problem',
    assign: kind === 'problem',
    mute: kind === 'anomaly',
    rootCause: kind === 'problem' || kind === 'anomaly',
    openSource: true,
  };
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
