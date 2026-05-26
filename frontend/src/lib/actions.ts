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

export type ActionParam = {
  name: string;
  label: string;
  // 'text': single-line free-form input. Sufficient for v1 across
  // all five planned actions (ids and view names). Future: 'id-suggest'
  // with autocomplete against /api/problems etc., 'duration' with
  // chip row (15m / 1h / 2h / 24h).
  kind: 'text';
  required: boolean;
  placeholder?: string;
};

export type Role = 'admin' | 'editor' | 'viewer';

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
  run: (params: Record<string, string>) => Promise<string>;
};

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
    run: async ({ problemId }) => {
      const id = problemId.trim();
      if (!id) throw new Error('Problem ID required');
      const res = await api.acknowledgeProblems([id]);
      const n = res.acknowledged;
      return `Acknowledged ${id} (${n} problem${n === 1 ? '' : 's'})`;
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
