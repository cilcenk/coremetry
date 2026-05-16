import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { useShortcuts } from '@/lib/keyboard';

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
  kind: 'page' | 'service' | 'trace';
  label: string;
  hint?: string;
  to: string;
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
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [services, setServices] = useState<Result[]>([]);
  const [selected, setSelected] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  // Global hotkey via the existing shortcut registry — Cmd-K on
  // Mac, Ctrl-K elsewhere. Registering through useShortcuts means
  // the binding shows up in the "?" help modal automatically and
  // the editable-target guard is handled centrally. `evenInInputs`
  // so an operator typing in a filter field can still pop the
  // palette without blurring first — Cmd-K is universally
  // expected to override.
  useShortcuts([{
    keys: 'mod+k',
    label: 'Open command palette',
    group: 'Navigation',
    evenInInputs: true,
    handler: () => {
      setOpen(true);
      setQuery('');
      setSelected(0);
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
  }, [query, services]);

  // Reset cursor when results shrink/grow — otherwise the cursor
  // can point past the last row and Enter does nothing.
  useEffect(() => {
    if (selected >= results.length) setSelected(0);
  }, [results.length, selected]);

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setSelected(s => Math.min(results.length - 1, s + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setSelected(s => Math.max(0, s - 1));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const r = results[selected];
      if (r) {
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
        <input ref={inputRef}
          value={query}
          onChange={e => setQuery(e.target.value)}
          placeholder="Search services, pages, or paste a trace id…"
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
            <div key={`${r.kind}:${r.to}`}
              onMouseEnter={() => setSelected(i)}
              onClick={() => { navigate(r.to); setOpen(false); }}
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
                background: 'var(--bg3)', color: 'var(--text2)',
                fontFamily: 'ui-monospace, monospace',
                minWidth: 56, textAlign: 'center',
              }}>
                {r.kind === 'trace' ? 'trace' : r.kind === 'service' ? 'service' : 'page'}
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
      </div>
    </div>
  );
}
