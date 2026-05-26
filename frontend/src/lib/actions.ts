// Action launcher registry (v0.5.457). Cmd-K palette ranks actions
// alongside pages and services; selecting one enters a per-param
// prompt sub-mode in the same palette. Backend mutation handlers
// already exist (problem.acknowledge, anomaly_silence.create,
// alert_rule.{enable,disable}, saved_view.create) — this module
// is the frontend abstraction that maps typed query → action +
// orchestrates the param-collection + execution flow.
//
// Design: actions are flat records, not classes. Each declares
// keywords (for ranking), allowed roles (frontend visibility
// gate; backend enforces the real gate anyway), params, and a
// run() that calls the API. No registration ceremony — to add
// an action, append to ACTIONS.

import { api } from './api';

// SuggestItem — one row returned by a suggest() lookup. The
// palette renders `label` (with optional `hint`); the action's
// run() reads `id` and any auxiliary fields via `payload`.
export interface SuggestItem {
  id: string;
  label: string;
  hint?: string;
  payload?: Record<string, string>;
}

// DurationOption — one chip for the 'duration' param kind. Value
// is the duration in seconds (silence backend's body.durationSec).
export interface DurationOption {
  label: string;
  seconds: number;
}

export const DEFAULT_DURATIONS: DurationOption[] = [
  { label: '15m', seconds: 15 * 60 },
  { label: '1h',  seconds: 60 * 60 },
  { label: '2h',  seconds: 2 * 60 * 60 },
  { label: '6h',  seconds: 6 * 60 * 60 },
  { label: '24h', seconds: 24 * 60 * 60 },
];

export type ActionParam = {
  name: string;
  label: string;
  // 'text'        — single-line free-form input.
  // 'id-suggest'  — autocomplete dropdown; operator picks from
  //                 dynamic results returned by suggest(q).
  // 'duration'    — chip row; operator picks a preset second-count.
  kind: 'text' | 'id-suggest' | 'duration';
  required: boolean;
  placeholder?: string;
  // id-suggest only — fetches matching options as the operator types.
  suggest?: (q: string) => Promise<SuggestItem[]>;
  // duration only — overrides DEFAULT_DURATIONS for actions that
  // want a different set of chips.
  durations?: DurationOption[];
};

export type Role = 'admin' | 'editor' | 'viewer';

// ParamValue — what each kind contributes to the action's
// params bag:
//   'text'       → string
//   'id-suggest' → SuggestItem (id + label + payload)
//   'duration'   → number (seconds)
// Action.run() type-narrows based on the param kinds it declared.
export type ParamValue = string | SuggestItem | number;
export type ParamValues = Record<string, ParamValue>;

export type Action = {
  id: string;
  label: string;
  hint: string;
  // Substring keywords used by the palette's ranking. "ack" should
  // surface Acknowledge first; the label / id are also matched.
  keywords: string[];
  // Roles allowed to see this action. The backend route is the
  // real auth gate; this hides the action from a viewer's palette
  // so they don't see a 403-only chip.
  allowedRoles: Role[];
  params: ActionParam[];
  // Returns a user-readable success message; throws an Error
  // (with a message) on failure. The caller wraps in toast.
  run: (params: ParamValues) => Promise<string>;
};

// Helper to narrow text-kind param values; each text param is
// guaranteed string by the palette UI, so the cast is safe.
const txt = (v: ParamValue): string => typeof v === 'string' ? v : '';

export const ACTIONS: Action[] = [
  {
    id: 'ack-problem',
    label: 'Acknowledge problem',
    hint: 'Mark an open problem as acknowledged',
    keywords: ['ack', 'acknowledge'],
    allowedRoles: ['admin', 'editor'],
    params: [{
      name: 'problemId',
      label: 'Problem ID',
      kind: 'text',
      required: true,
      placeholder: 'problem-1234',
    }],
    run: async (params) => {
      const id = txt(params.problemId).trim();
      if (!id) throw new Error('Problem ID required');
      const res = await api.acknowledgeProblems([id]);
      const n = res.acknowledged;
      return `Acknowledged ${id} (${n} problem${n === 1 ? '' : 's'})`;
    },
  },
  {
    id: 'disable-rule',
    label: 'Disable alert rule',
    hint: 'Stop an alert rule from firing without deleting it',
    keywords: ['disable', 'mute rule', 'pause rule'],
    allowedRoles: ['admin', 'editor'],
    params: [{
      name: 'ruleId',
      label: 'Rule ID',
      kind: 'text',
      required: true,
      placeholder: 'rule-uuid',
    }],
    run: async (params) => {
      const id = txt(params.ruleId).trim();
      if (!id) throw new Error('Rule ID required');
      await api.disableAlertRule(id);
      return `Disabled rule ${id}`;
    },
  },
  {
    id: 'enable-rule',
    label: 'Enable alert rule',
    hint: 'Re-enable a previously disabled rule',
    keywords: ['enable', 'unmute rule', 'resume rule'],
    allowedRoles: ['admin', 'editor'],
    params: [{
      name: 'ruleId',
      label: 'Rule ID',
      kind: 'text',
      required: true,
      placeholder: 'rule-uuid',
    }],
    run: async (params) => {
      const id = txt(params.ruleId).trim();
      if (!id) throw new Error('Rule ID required');
      await api.enableAlertRule(id);
      return `Enabled rule ${id}`;
    },
  },
  {
    id: 'save-view',
    label: 'Save current view',
    hint: 'Stash the current filter combo as a named saved view',
    keywords: ['save', 'pin', 'bookmark'],
    allowedRoles: ['admin', 'editor', 'viewer'],
    params: [{
      name: 'name',
      label: 'View name',
      kind: 'text',
      required: true,
      placeholder: 'e.g. Payments errors',
    }],
    run: async (params) => {
      const trimmed = txt(params.name).trim();
      if (!trimmed) throw new Error('Name required');
      // Derive page + queryString from the current URL so the
      // operator gets "save THIS slice" without needing extra
      // params. saved_views table is per-(page,owner).
      const page = window.location.pathname.replace(/^\//, '') || 'home';
      const queryString = window.location.search.replace(/^\?/, '');
      await api.createSavedView({ name: trimmed, page, queryString });
      return `Saved view "${trimmed}" on /${page}`;
    },
  },
  {
    id: 'mark-event',
    label: 'Mark event',
    hint: 'Drop a vertical marker on every time-series chart (deploy, config change, incident, …)',
    keywords: ['mark', 'event', 'deploy', 'annotate'],
    allowedRoles: ['admin', 'editor'],
    params: [{
      name: 'label',
      label: 'Label',
      kind: 'text',
      required: true,
      placeholder: 'e.g. payments v1.2.3 deployed',
    }],
    run: async (params) => {
      const label = txt(params.label).trim();
      if (!label) throw new Error('Label required');
      // v0.5.476 — single-param flow for now; service / kind /
      // link can be expressed by typing them into label as
      // human-readable text. A richer multi-param flow can come
      // later once operators tell us what they actually need.
      await api.createEvent({ label, kind: 'custom' });
      return `Marked "${label}"`;
    },
  },
  {
    id: 'silence-anomaly',
    label: 'Silence anomaly',
    hint: 'Mute a (kind, pattern, service) anomaly tuple for a duration',
    keywords: ['silence', 'mute anomaly', 'snooze anomaly'],
    allowedRoles: ['admin', 'editor'],
    params: [
      {
        name: 'anomaly',
        label: 'Anomaly',
        kind: 'id-suggest',
        required: true,
        placeholder: 'Type a service or pattern…',
        suggest: async (q: string) => {
          const rows = await api.activeAnomalies(q);
          // Each row carries the silence-create body's required
          // fields under `payload`. The palette stashes the whole
          // SuggestItem; run() reads them back.
          return (rows ?? []).map(r => ({
            id: r.id,
            label: r.label,
            hint: r.status === 'active' ? '● active' : '○ cleared',
            payload: {
              kind: r.kind,
              pattern: r.pattern,
              service: r.service,
            },
          }));
        },
      },
      {
        name: 'duration',
        label: 'Duration',
        kind: 'duration',
        required: true,
      },
    ],
    run: async (params) => {
      const picked = params.anomaly as SuggestItem | undefined;
      const seconds = typeof params.duration === 'number' ? params.duration : 0;
      if (!picked) throw new Error('Anomaly required');
      if (seconds <= 0) throw new Error('Duration required');
      const p = picked.payload ?? {};
      await api.createAnomalySilence({
        fingerprint: picked.id,
        kind: p.kind ?? '',
        pattern: p.pattern ?? '',
        service: p.service ?? '',
        durationSec: seconds,
      });
      const niceDur = (DEFAULT_DURATIONS.find(d => d.seconds === seconds)?.label
                    ?? `${Math.round(seconds / 60)}m`);
      return `Silenced "${picked.label}" for ${niceDur}`;
    },
  },
];

// filterActions ranks the registry against the typed query, gated
// by role. Returns sorted by best match first. Empty query returns
// nothing — actions only appear when the operator types a verb,
// so they don't bury page navigation under "all 5 actions".
export function filterActions(role: string | undefined, query: string): Action[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  return ACTIONS
    .filter(a => role ? a.allowedRoles.includes(role as Role) : false)
    .map(a => {
      let score = 0;
      if (a.id.toLowerCase().startsWith(q)) score = 500;
      else if (a.label.toLowerCase().startsWith(q)) score = 400;
      else if (a.keywords.some(k => k.startsWith(q))) score = 300;
      else if (a.id.toLowerCase().includes(q)) score = 100;
      else if (a.label.toLowerCase().includes(q)) score = 100;
      else if (a.keywords.some(k => k.includes(q))) score = 80;
      return { a, score };
    })
    .filter(({ score }) => score > 0)
    .sort((x, y) => y.score - x.score)
    .map(({ a }) => a);
}
