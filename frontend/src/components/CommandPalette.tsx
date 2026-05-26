import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { useShortcuts } from '@/lib/keyboard';
import { useAuth } from '@/components/AuthProvider';
import { filterActions, type Action } from '@/lib/actions';
import { toast } from '@/lib/toast';

// CommandPalette — global Cmd-K / Ctrl-K spotlight (v0.5.162).
// Mounted once at AppShell level; listens for the hotkey and pops
// the modal. Three result kinds in v1:
//   • Pages — hardcoded route catalog (every internal SPA page)
//   • Services — fetched on first open from /api/services, cached
//                in-memory for the session
//   • Trace — when the query looks like a trace id (32 hex chars)
//             a "Go to trace" suggestion appears
//
// Designed to feel like Linear / Raycast: opens in 16ms, results
// re-rank as the user types, arrows + enter to select, Esc to
// close. No search-index dep — substring scoring is fine at our
// catalog size (~30 pages + N services).

type Result = {
  kind: 'page' | 'service' | 'trace' | 'action';
  label: string;
  hint?: string;
  // navigate target — set for page/service/trace; absent for actions
  // (selecting an action enters param-prompt mode, doesn't navigate).
  to?: string;
  // action payload — set when kind === 'action'.
  action?: Action;
  score?: number;
};

const PAGES: Result[] = [
  { kind: 'page', label: 'Home',        hint: 'Overview', to: '/' },
  { kind: 'page', label: 'Services',    hint: 'Per-service RED + latency', to: '/services' },
  { kind: 'page', label: 'Topology',    hint: 'Service / operation / flows', to: '/topology' },
  { kind: 'page', label: 'Traces',      hint: 'Search raw traces', to: '/traces' },
  { kind: 'page', label: 'Logs',        hint: 'Elasticsearch logs', to: '/logs' },
  { kind: 'page', label: 'Metrics',     hint: 'Time-series explorer', to: '/metrics' },
  { kind: 'page', label: 'Explore',     hint: 'Cross-signal ad-hoc query', to: '/explore' },
  { kind: 'page', label: 'Notebook',    hint: 'Saved investigations', to: '/notebook' },
  { kind: 'page', label: 'Databases',   hint: 'DBM-style query catalog', to: '/databases' },
  { kind: 'page', label: 'Messaging',   hint: 'Kafka / RabbitMQ / SQS', to: '/messaging' },
  { kind: 'page', label: 'Dashboards',  hint: 'Operator-curated', to: '/dashboards' },
  { kind: 'page', label: 'Problems',    hint: 'Open alert + exception inbox', to: '/problems' },
  { kind: 'page', label: 'Anomalies',   hint: 'Log + trace anomaly streams', to: '/anomalies' },
  { kind: 'page', label: 'Incidents',   hint: 'Manual incident log', to: '/incidents' },
  { kind: 'page', label: 'Alerts',      hint: 'Alert rules + noisy report', to: '/alerts' },
  { kind: 'page', label: 'SLOs',        hint: 'Service level objectives', to: '/slos' },
  { kind: 'page', label: 'Monitors',    hint: 'Synthetic probes', to: '/monitors' },
  { kind: 'page', label: 'Profiling',   hint: 'Continuous profiling', to: '/profiling' },
  { kind: 'page', label: 'Errors',      hint: 'Exception groups', to: '/errors' },
  { kind: 'page', label: 'Status',      hint: 'Internal ingest health', to: '/status' },
  { kind: 'page', label: 'Public status',  hint: 'Public incident page', to: '/public-status' },
  { kind: 'page', label: 'Settings',    hint: 'AI / SMTP / retention / theme', to: '/settings' },
  { kind: 'page', label: 'Users',       hint: 'Role + team management', to: '/users' },
  { kind: 'page', label: 'Admin · Audit log',   hint: 'Operator action history', to: '/admin/audit' },
  { kind: 'page', label: 'Admin · Cardinality', hint: 'Attribute cardinality watch', to: '/admin/cardinality' },
  { kind: 'page', label: 'Admin · Service catalog', hint: 'Owner / runbook / oncall metadata', to: '/admin/catalog' },
  { kind: 'page', label: 'Admin · SQL',         hint: 'Raw CH query console', to: '/admin/sql' },
  { kind: 'page', label: 'Admin · Stats',       hint: 'Internal CH + cache stats', to: '/admin/stats' },
  { kind: 'page', label: 'Admin · Status page', hint: 'Components + subscribers', to: '/admin/status-page' },
];

// Module-level cache so re-opening the palette in the same tab
// doesn't re-fetch services every time.
let SERVICES_CACHE: Result[] | null = null;

const TRACE_ID_RE = /^[a-f0-9]{16,32}$/i;

export function CommandPalette() {
  const navigate = useNavigate();
  const { user } = useAuth();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [services, setServices] = useState<Result[]>([]);
  const [selected, setSelected] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  // Param-prompt sub-mode (v0.5.457). When activeAction is set,
  // the palette stops showing the search results and starts
  // collecting per-param input for the chosen action. paramIdx
  // tracks which param we're on; paramValues accumulates answers.
  const [activeAction, setActiveAction] = useState<Action | null>(null);
  const [paramIdx, setParamIdx] = useState(0);
  const [paramValues, setParamValues] = useState<Record<string, string>>({});
  const [running, setRunning] = useState(false);

  // Global hotkey via the existing shortcut registry — Cmd-K on
  // Mac, Ctrl-K elsewhere. Registering through useShortcuts means
  // the binding shows up in the "?" help modal automatically and
  // the editable-target guard is handled centrally. `evenInInputs`
  // so an operator typing in a filter field can still pop the
  // palette without blurring first — Cmd-K is universally
  // expected to override.
  // Reset all transient state on every open so re-opening doesn't
  // resume mid-action from a previous session.
  const resetState = () => {
    setQuery('');
    setSelected(0);
    setActiveAction(null);
    setParamIdx(0);
    setParamValues({});
    setRunning(false);
  };

  useShortcuts([{
    keys: 'mod+k',
    label: 'Open command palette',
    group: 'Navigation',
    evenInInputs: true,
    handler: () => {
      setOpen(true);
      resetState();
    },
  }], []);

  // Esc to close — local listener since the global one pauses in
  // editable targets and our input IS editable.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open]);

  // Focus the input + lazy-load services on first open. Services
  // come from the cached api response — we don't need a time
  // range here since the catalog is the same regardless of window.
  useEffect(() => {
    if (!open) return;
    setTimeout(() => inputRef.current?.focus(), 10);
    if (SERVICES_CACHE) {
      setServices(SERVICES_CACHE);
      return;
    }
    api.services({ from: 0, to: 0 })
      .then(s => {
        const list: Result[] = (s ?? []).slice(0, 200).map(svc => ({
          kind: 'service' as const,
          label: svc.name,
          hint: 'Service',
          to: `/service?name=${encodeURIComponent(svc.name)}`,
        }));
        SERVICES_CACHE = list;
        setServices(list);
      })
      .catch(() => { /* no services = page-only results */ });
  }, [open]);

  // Score: pages with the query as a prefix beat substring beat
  // fuzzy. Exact matches sort to the top. Hand-rolled rather than
  // pulling in fuzzysort — at this catalog size the diff is in
  // microseconds and the bundle stays lean.
  const results = useMemo(() => {
    const q = query.trim().toLowerCase();
    const all = [...PAGES, ...services];
    let scored: Result[];
    if (!q) {
      scored = PAGES; // empty query → just show pages
    } else {
      scored = all
        .map(r => {
          const lbl = r.label.toLowerCase();
          let score = 0;
          if (lbl === q) score = 1000;
          else if (lbl.startsWith(q)) score = 500;
          else if (lbl.includes(q)) score = 200;
          else {
            // letters-in-order fuzzy
            let qi = 0;
            for (let i = 0; i < lbl.length && qi < q.length; i++) {
              if (lbl[i] === q[qi]) qi++;
            }
            if (qi === q.length) score = 50;
          }
          return { ...r, score };
        })
        .filter(r => r.score && r.score > 0)
        .sort((a, b) => (b.score ?? 0) - (a.score ?? 0))
        .slice(0, 50);
    }
    // Action launcher results (v0.5.457). Verb-driven matches like
    // "ack" → Acknowledge problem float ABOVE navigation results
    // because the operator's intent when typing a verb is action,
    // not page nav. filterActions handles role-gating + ranking.
    const actionMatches = filterActions(user?.role, q).map<Result>(a => ({
      kind: 'action',
      label: a.label,
      hint: a.hint,
      action: a,
      score: 800,
    }));
    if (actionMatches.length > 0) {
      scored = [...actionMatches, ...scored];
    }
    // Trace-id shortcut — looks like 16-32 hex chars → offer a
    // direct jump. Trace IDs are commonly pasted from logs and
    // emails into this kind of search box.
    if (q && TRACE_ID_RE.test(q)) {
      scored = [
        { kind: 'trace', label: q, hint: 'Open trace', to: `/trace?id=${encodeURIComponent(q)}`, score: 999 },
        ...scored,
      ];
    }
    return scored;
  }, [query, services, user?.role]);

  // Reset cursor when results shrink/grow — otherwise the cursor
  // can point past the last row and Enter does nothing.
  useEffect(() => {
    if (selected >= results.length) setSelected(0);
  }, [results.length, selected]);

  // Execute the active action with current paramValues. Toast the
  // result, close the palette. Errors come back as Error.message
  // (api client formats as `HTTP NNN: <body>`) which is operator-
  // readable.
  const runActiveAction = async () => {
    if (!activeAction || running) return;
    setRunning(true);
    try {
      const msg = await activeAction.run(paramValues);
      toast.success(msg);
      setOpen(false);
      resetState();
    } catch (e) {
      const m = e instanceof Error ? e.message : String(e);
      toast.error(`${activeAction.label} failed: ${m}`);
      setRunning(false);
      // Stay in param mode so the operator can correct + retry.
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (activeAction) {
      // Param-prompt mode handles its own Enter; arrow keys
      // don't navigate a result list that isn't shown.
      if (e.key === 'Enter') {
        e.preventDefault();
        const cur = activeAction.params[paramIdx];
        const val = (paramValues[cur.name] ?? '').trim();
        if (cur.required && !val) return;
        if (paramIdx + 1 < activeAction.params.length) {
          setParamIdx(paramIdx + 1);
        } else {
          void runActiveAction();
        }
      }
      return;
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setSelected(s => Math.min(results.length - 1, s + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setSelected(s => Math.max(0, s - 1));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const r = results[selected];
      if (!r) return;
      if (r.kind === 'action' && r.action) {
        setActiveAction(r.action);
        setParamIdx(0);
        setParamValues({});
        // Defer focus to the param input — the existing inputRef
        // points at the query input; once activeAction flips, we
        // re-render and the same input becomes the param input,
        // so re-focusing keeps the cursor where the operator
        // expects.
        setTimeout(() => inputRef.current?.focus(), 0);
        return;
      }
      if (r.to) {
        navigate(r.to);
        setOpen(false);
      }
    }
  };

  if (!open) return null;
  return (
    <div onClick={() => setOpen(false)}
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.45)',
        display: 'flex', justifyContent: 'center',
        alignItems: 'flex-start', paddingTop: '12vh',
        zIndex: 100,
      }}>
      <div onClick={e => e.stopPropagation()}
        onKeyDown={onKeyDown}
        style={{
          width: 'min(640px, 92vw)',
          maxHeight: '70vh',
          display: 'flex', flexDirection: 'column',
          background: 'var(--bg)', color: 'var(--text)',
          border: '1px solid var(--border)', borderRadius: 10,
          boxShadow: '0 12px 48px rgba(0,0,0,0.5)',
        }}>
        {activeAction ? (
          // Param-prompt sub-mode (v0.5.457). Header shows the
          // action label + step pip (N/M params). Single input is
          // the current param; Enter advances or runs.
          <>
            <div style={{
              padding: '14px 16px',
              borderBottom: '1px solid var(--border)',
              display: 'flex', alignItems: 'center', gap: 10,
              background: 'var(--bg2)',
            }}>
              <span style={{
                fontSize: 10, padding: '2px 6px', borderRadius: 3,
                background: 'rgba(56,139,253,.18)', color: 'rgb(56,139,253)',
                fontFamily: 'ui-monospace, monospace', fontWeight: 600,
              }}>action</span>
              <span style={{ fontSize: 13, fontWeight: 600, flex: 1 }}>
                {activeAction.label}
              </span>
              {activeAction.params.length > 1 && (
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  {paramIdx + 1} / {activeAction.params.length}
                </span>
              )}
            </div>
            {(() => {
              const cur = activeAction.params[paramIdx];
              return (
                <input ref={inputRef}
                  value={paramValues[cur.name] ?? ''}
                  onChange={e => setParamValues({ ...paramValues, [cur.name]: e.target.value })}
                  placeholder={cur.placeholder || cur.label}
                  disabled={running}
                  style={{
                    border: 'none', outline: 'none',
                    background: 'transparent', color: 'var(--text)',
                    padding: '14px 16px', fontSize: 14,
                  }} />
              );
            })()}
            <div style={{
              padding: '10px 16px',
              fontSize: 11, color: 'var(--text3)',
              borderTop: '1px solid var(--border)',
              display: 'flex', justifyContent: 'space-between',
            }}>
              <span>
                {running
                  ? 'Running…'
                  : paramIdx + 1 < activeAction.params.length
                    ? '↵ next'
                    : '↵ run · Esc cancel'}
              </span>
              <button type="button"
                onClick={() => { resetState(); }}
                disabled={running}
                style={{
                  background: 'transparent', border: 'none',
                  color: 'var(--text3)', cursor: 'pointer', fontSize: 11,
                }}>
                ← back to search
              </button>
            </div>
          </>
        ) : (
        <>
        <input ref={inputRef}
          value={query}
          onChange={e => setQuery(e.target.value)}
          placeholder="Search services, pages, run an action, or paste a trace id…"
          style={{
            border: 'none', outline: 'none',
            background: 'transparent', color: 'var(--text)',
            padding: '14px 16px', fontSize: 14,
            borderBottom: '1px solid var(--border)',
          }} />
        <div style={{ overflowY: 'auto', flex: 1 }}>
          {results.length === 0 && (
            <div style={{ padding: 16, color: 'var(--text3)', fontSize: 13 }}>
              No matches.
            </div>
          )}
          {results.map((r, i) => (
            <div key={`${r.kind}:${r.to ?? r.action?.id ?? i}`}
              onMouseEnter={() => setSelected(i)}
              onClick={() => {
                if (r.kind === 'action' && r.action) {
                  setActiveAction(r.action);
                  setParamIdx(0);
                  setParamValues({});
                  setTimeout(() => inputRef.current?.focus(), 0);
                  return;
                }
                if (r.to) { navigate(r.to); setOpen(false); }
              }}
              style={{
                padding: '8px 16px',
                cursor: 'pointer',
                display: 'flex', alignItems: 'center', gap: 10,
                background: i === selected ? 'var(--bg2)' : 'transparent',
                borderLeft: i === selected
                  ? '2px solid var(--accent2)'
                  : '2px solid transparent',
              }}>
              <span style={{
                fontSize: 10, padding: '2px 6px', borderRadius: 3,
                background: r.kind === 'action' ? 'rgba(56,139,253,.18)' : 'var(--bg3)',
                color: r.kind === 'action' ? 'rgb(56,139,253)' : 'var(--text2)',
                fontFamily: 'ui-monospace, monospace',
                minWidth: 56, textAlign: 'center',
                fontWeight: r.kind === 'action' ? 600 : 400,
              }}>
                {r.kind === 'trace' ? 'trace'
                 : r.kind === 'service' ? 'service'
                 : r.kind === 'action' ? 'action'
                 : 'page'}
              </span>
              <span style={{ fontSize: 13, fontWeight: 500, flex: 1 }}>
                {r.label}
              </span>
              {r.hint && (
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>{r.hint}</span>
              )}
            </div>
          ))}
        </div>
        <div style={{
          padding: '6px 12px', borderTop: '1px solid var(--border)',
          fontSize: 11, color: 'var(--text3)',
          display: 'flex', gap: 16,
        }}>
          <span>↑↓ navigate</span>
          <span>↵ select</span>
          <span>esc close</span>
          <span style={{ marginLeft: 'auto' }}>{results.length} result{results.length === 1 ? '' : 's'}</span>
        </div>
        </>
        )}
      </div>
    </div>
  );
}
