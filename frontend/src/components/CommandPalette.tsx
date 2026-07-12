import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { getRecentServices, getPinnedServices } from '@/lib/recentServices';
import { useShortcuts } from '@/lib/keyboard';
import { useAuth } from '@/components/AuthProvider';
import {
  filterActions, DEFAULT_DURATIONS,
  type Action, type ParamValues, type SuggestItem,
} from '@/lib/actions';
import { toast } from '@/lib/toast';

// CommandPalette — global Cmd-K / Ctrl-K spotlight (v0.5.162).
// Mounted once at AppShell level; listens for the hotkey and pops
// the modal. Three result kinds in v1:
//   • Pages — hardcoded route catalog (every internal SPA page)
//   • Services — server-searched per keystroke (v0.8.518), pinned/recent
//                in-memory for the session
//   • Trace — when the query looks like a trace id (32 hex chars)
//             a "Go to trace" suggestion appears
//
// Designed to feel like Linear / Raycast: opens in 16ms, results
// re-rank as the user types, arrows + enter to select, Esc to
// close. No search-index dep — substring scoring is fine at our
// catalog size (~30 pages + N services).

type Result = {
  kind: 'page' | 'service' | 'trace' | 'action' | 'endpoint';
  label: string;
  hint?: string;
  // navigate target — set for page/service/trace; absent for actions
  // (selecting an action enters param-prompt mode, doesn't navigate).
  to?: string;
  // action payload — set when kind === 'action'.
  action?: Action;
  score?: number;
};

// v0.8.525 — güncel rotalara hizalandı. Önceki katalogda ölü hedefler
// vardı (Home /, Topology /topology, Errors /errors, Status /status —
// hepsi redirect/retired) ve tüm 'Admin · …' girişleri /admin/* idi;
// admin sayfaları v0.8.9'da /system/:tab altına taşındı. Eksik canlı
// sayfalar da eklendi (Inbox, Endpoints, Service Map, External, Hosts,
// Trace compare, Events, AI). Sidebar'dan gizli sayfalar (Monitors,
// Profiling, External, Hosts, Events) ⌘K'da BİLEREK kalır — keşif
// yüzeyi burasıdır.
const PAGES: Result[] = [
  // Triage
  { kind: 'page', label: 'Inbox',       hint: 'Unified triage queue', to: '/inbox' },
  { kind: 'page', label: 'Incidents',   hint: 'Manual incident log', to: '/incidents' },
  { kind: 'page', label: 'Problems',    hint: 'Open alert + exception inbox', to: '/problems' },
  { kind: 'page', label: 'Anomalies',   hint: 'Log + trace anomaly streams', to: '/anomalies' },
  // Services
  { kind: 'page', label: 'Services',    hint: 'Per-service RED + latency', to: '/services' },
  { kind: 'page', label: 'Endpoints',   hint: 'Per-route RED', to: '/endpoints' },
  { kind: 'page', label: 'Service Map', hint: 'Topology / flows', to: '/service-map' },
  { kind: 'page', label: 'Databases',   hint: 'DBM-style query catalog', to: '/databases' },
  { kind: 'page', label: 'Messaging',   hint: 'Kafka / RabbitMQ / SQS', to: '/messaging' },
  { kind: 'page', label: 'External',    hint: 'Third-party dependencies', to: '/external' },
  { kind: 'page', label: 'Hosts',       hint: 'Infrastructure inventory', to: '/hosts' },
  // Signals
  { kind: 'page', label: 'Traces',      hint: 'Search raw traces', to: '/traces' },
  { kind: 'page', label: 'Metrics',     hint: 'Time-series explorer', to: '/metrics' },
  { kind: 'page', label: 'Logs',        hint: 'Elasticsearch logs', to: '/logs' },
  { kind: 'page', label: 'Profiling',   hint: 'Continuous profiling', to: '/profiling' },
  { kind: 'page', label: 'Trace compare', hint: 'Diff two traces', to: '/trace/compare' },
  // Workspaces
  { kind: 'page', label: 'Explore',     hint: 'Cross-signal ad-hoc query', to: '/explore' },
  { kind: 'page', label: 'Runbooks',    hint: 'Operational procedures', to: '/runbooks' },
  { kind: 'page', label: 'Dashboards',  hint: 'Operator-curated', to: '/dashboards' },
  // Alerting
  { kind: 'page', label: 'Alerts',      hint: 'Alert rules + noisy report', to: '/alerts' },
  { kind: 'page', label: 'SLOs',        hint: 'Service level objectives', to: '/slos' },
  { kind: 'page', label: 'Monitors',    hint: 'Synthetic probes', to: '/monitors' },
  { kind: 'page', label: 'Events',      hint: 'Deploy / config markers', to: '/events' },
  // System (v0.8.9 — /admin/* → /system/:tab)
  { kind: 'page', label: 'System · Overview',      hint: 'Internal CH + cache stats', to: '/system/stats' },
  { kind: 'page', label: 'System · ClickHouse',    hint: 'CH health + mutations', to: '/system/clickhouse' },
  { kind: 'page', label: 'System · Elasticsearch', hint: 'ES backend health', to: '/system/elastic' },
  { kind: 'page', label: 'System · Cluster',       hint: 'Cluster topology', to: '/system/cluster' },
  { kind: 'page', label: 'System · Cardinality',   hint: 'Attribute cardinality watch', to: '/system/cardinality' },
  { kind: 'page', label: 'System · Catalog',       hint: 'Owner / runbook / oncall metadata', to: '/system/catalog' },
  { kind: 'page', label: 'System · Audit log',     hint: 'Operator action history', to: '/system/audit' },
  { kind: 'page', label: 'System · SQL',           hint: 'Raw CH query console', to: '/system/sql' },
  { kind: 'page', label: 'System · Query',         hint: 'Query console', to: '/system/query' },
  { kind: 'page', label: 'System · Status page',   hint: 'Components + subscribers', to: '/system/status-page' },
  { kind: 'page', label: 'AI observability', hint: 'Copilot usage + cost', to: '/ai' },
  // Meta
  { kind: 'page', label: 'Settings',    hint: 'AI / SMTP / retention / theme', to: '/settings' },
  { kind: 'page', label: 'Users',       hint: 'Role + team management', to: '/users' },
  { kind: 'page', label: 'Public status',  hint: 'Public incident page', to: '/public-status' },
];

// Module-level cache so re-opening the palette in the same tab
// doesn't re-fetch services every time.

const TRACE_ID_RE = /^[a-f0-9]{16,32}$/i;

export function CommandPalette() {
  const navigate = useNavigate();
  const { user } = useAuth();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [services, setServices] = useState<Result[]>([]);
  // Endpoint (operation) matches — server-debounced search, NOT an eager
  // catalogue (picker = server-side search; 10k+ operations can't ride a
  // client list). Refreshed per keystroke; cleared when the query is too
  // short or looks like a trace id. (UX pass #1.)
  const [endpoints, setEndpoints] = useState<Result[]>([]);
  // v0.7.89 — pinned + recently-viewed services, refreshed each open,
  // shown in the empty-query state as the pivot rotation.
  const [pivotSvcs, setPivotSvcs] = useState<Result[]>([]);
  const [selected, setSelected] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  // Param-prompt sub-mode (v0.5.457). When activeAction is set,
  // the palette stops showing the search results and starts
  // collecting per-param input for the chosen action. paramIdx
  // tracks which param we're on; paramValues accumulates answers.
  const [activeAction, setActiveAction] = useState<Action | null>(null);
  const [paramIdx, setParamIdx] = useState(0);
  const [paramValues, setParamValues] = useState<ParamValues>({});
  const [running, setRunning] = useState(false);
  // id-suggest sub-state (v0.5.459). Per-step typed query, the
  // current debounced result list, and which row is highlighted
  // for keyboard pick. Cleared between params + on reset.
  const [suggestQuery, setSuggestQuery] = useState('');
  const [suggestResults, setSuggestResults] = useState<SuggestItem[]>([]);
  const [suggestSelected, setSuggestSelected] = useState(0);
  const [suggestLoading, setSuggestLoading] = useState(false);

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
    setSuggestQuery('');
    setSuggestResults([]);
    setSuggestSelected(0);
    setSuggestLoading(false);
  };

  // Debounced id-suggest fetch. When the current param is an
  // id-suggest, every keystroke on suggestQuery triggers a 200ms
  // debounced call to its suggest(q) function. Cancellation flag
  // protects against late responses racing a newer query.
  useEffect(() => {
    if (!activeAction) return;
    const cur = activeAction.params[paramIdx];
    if (!cur || cur.kind !== 'id-suggest' || !cur.suggest) return;
    const q = suggestQuery;
    let cancelled = false;
    setSuggestLoading(true);
    const t = window.setTimeout(async () => {
      try {
        const rows = await cur.suggest!(q);
        if (cancelled) return;
        setSuggestResults(rows);
        setSuggestSelected(0);
      } catch {
        if (!cancelled) setSuggestResults([]);
      } finally {
        if (!cancelled) setSuggestLoading(false);
      }
    }, 200);
    return () => { cancelled = true; clearTimeout(t); };
  }, [activeAction, paramIdx, suggestQuery]);

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

  // Focus the input + refresh the pivot rotation on open.
  useEffect(() => {
    if (!open) return;
    setTimeout(() => inputRef.current?.focus(), 10);
    // v0.7.89 — refresh the pivot rotation each open: pinned (★) first,
    // then recently-viewed (newest first), deduped. Each is a
    // one-keystroke jump back to a service the operator is working.
    const pinned = getPinnedServices();
    const recents = getRecentServices().filter(n => !pinned.includes(n));
    const mkSvc = (name: string, hint: string): Result => ({
      kind: 'service', label: name, hint, to: `/service?name=${encodeURIComponent(name)}`,
    });
    setPivotSvcs([
      ...pinned.map(n => mkSvc(n, '★ Pinned')),
      ...recents.map(n => mkSvc(n, 'Recent')),
    ]);
  }, [open]);

  // v0.8.518 (perf raporu #11) — servis araması SUNUCUDA. Eski eager
  // /api/services fetch'i 200 ile kesiyordu: 1400+ servisli prod'da
  // katalogun çoğu ⌘K'da hiç bulunamıyordu (fiilen ölü) ve her ilk
  // açılış tam liste indiriyordu. Endpoint aramasının deseniyle
  // (200ms debounce + stale-guard) autocomplete ucuna gider;
  // pinned/recent boş-sorgu rotasyonu aynen.
  useEffect(() => {
    const q = query.trim();
    if (!open || q.length < 2 || TRACE_ID_RE.test(q)) { setServices([]); return; }
    let cancelled = false;
    const t = window.setTimeout(() => {
      api.serviceNames(q, 20)
        .then(r => {
          if (cancelled) return;
          setServices((r?.names ?? []).map(name => ({
            kind: 'service' as const,
            label: name,
            hint: 'Service',
            to: `/service?name=${encodeURIComponent(name)}`,
          })));
        })
        .catch(() => { if (!cancelled) setServices([]); });
    }, 200);
    return () => { cancelled = true; clearTimeout(t); };
  }, [query, open]);

  // Endpoint search — server-debounced operation lookup so the palette can
  // jump to an endpoint, not just pages/services, WITHOUT eager-loading the
  // operation catalogue (10k+ ops; picker = server-side search constraint).
  // Fires only for a real ≥2-char query that isn't a trace id; each hit
  // jumps to that operation's traces. 200ms debounce + stale-guard mirror
  // the OperationPicker. (UX pass #1.)
  useEffect(() => {
    const q = query.trim();
    if (!open || q.length < 2 || TRACE_ID_RE.test(q)) { setEndpoints([]); return; }
    let cancelled = false;
    const t = window.setTimeout(() => {
      api.operationNames(undefined, q, 6)
        .then(r => {
          if (cancelled) return;
          setEndpoints((r?.names ?? []).map(name => ({
            kind: 'endpoint' as const,
            label: name,
            hint: 'Endpoint',
            to: `/traces?operation=${encodeURIComponent(name)}`,
          })));
        })
        .catch(() => { if (!cancelled) setEndpoints([]); });
    }, 200);
    return () => { cancelled = true; clearTimeout(t); };
  }, [query, open]);

  // Score: pages with the query as a prefix beat substring beat
  // fuzzy. Exact matches sort to the top. Hand-rolled rather than
  // pulling in fuzzysort — at this catalog size the diff is in
  // microseconds and the bundle stays lean.
  const results = useMemo(() => {
    const q = query.trim().toLowerCase();
    const all = [...PAGES, ...services];
    let scored: Result[];
    if (!q) {
      // empty query → pivot rotation (pinned ★ + recent services) first,
      // then the page catalog (v0.7.89).
      scored = [...pivotSvcs, ...PAGES];
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
    // Endpoint matches (already server-filtered by the query) appended after
    // the page/service hits so they're visible without crowding out an exact
    // page/service name match. (UX pass #1.)
    if (q && endpoints.length > 0) {
      scored = [...scored, ...endpoints];
    }
    return scored;
  }, [query, services, endpoints, pivotSvcs, user?.role]);

  // Reset cursor when results shrink/grow — otherwise the cursor
  // can point past the last row and Enter does nothing.
  useEffect(() => {
    if (selected >= results.length) setSelected(0);
  }, [results.length, selected]);

  // advanceParam — store the picked value, then either move to
  // the next param or fire run(). Shared by Enter (text input,
  // id-suggest pick) and chip clicks (duration).
  const advanceParam = (paramName: string, value: string | SuggestItem | number) => {
    if (!activeAction) return;
    const next = { ...paramValues, [paramName]: value };
    setParamValues(next);
    if (paramIdx + 1 < activeAction.params.length) {
      setParamIdx(paramIdx + 1);
      // Clear suggest sub-state for the next param.
      setSuggestQuery('');
      setSuggestResults([]);
      setSuggestSelected(0);
      // Re-focus the input (which becomes the next param's input
      // after re-render).
      setTimeout(() => inputRef.current?.focus(), 0);
    } else {
      // Last param — fire run with the final paramValues. We
      // pass `next` directly because the setParamValues from
      // above hasn't flushed by the time we'd read state.
      void runActionWithValues(next);
    }
  };

  // runActionWithValues — same as runActiveAction but reads the
  // values param explicitly so we don't depend on a state flush.
  const runActionWithValues = async (values: ParamValues) => {
    if (!activeAction || running) return;
    setRunning(true);
    try {
      const msg = await activeAction.run(values);
      toast.success(msg);
      setOpen(false);
      resetState();
    } catch (e) {
      const m = e instanceof Error ? e.message : String(e);
      toast.error(`${activeAction.label} failed: ${m}`);
      setRunning(false);
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (activeAction) {
      const cur = activeAction.params[paramIdx];
      if (!cur) return;
      // ArrowUp/Down navigates the suggest dropdown when id-suggest.
      if (cur.kind === 'id-suggest') {
        if (e.key === 'ArrowDown') {
          e.preventDefault();
          setSuggestSelected(s => Math.min(suggestResults.length - 1, s + 1));
          return;
        }
        if (e.key === 'ArrowUp') {
          e.preventDefault();
          setSuggestSelected(s => Math.max(0, s - 1));
          return;
        }
        if (e.key === 'Enter') {
          e.preventDefault();
          const picked = suggestResults[suggestSelected];
          if (!picked) return;
          advanceParam(cur.name, picked);
          return;
        }
        return;
      }
      // Text: Enter advances if the value passes the required
      // check. Empty + required → ignored.
      if (cur.kind === 'text' && e.key === 'Enter') {
        e.preventDefault();
        const raw = paramValues[cur.name];
        const val = typeof raw === 'string' ? raw.trim() : '';
        if (cur.required && !val) return;
        advanceParam(cur.name, val);
        return;
      }
      // Duration: chips are click-driven; Enter has no semantics
      // (operator should click a chip). Esc bubbles to the global
      // close handler.
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
              if (cur.kind === 'duration') {
                const opts = cur.durations ?? DEFAULT_DURATIONS;
                return (
                  <div style={{
                    padding: '14px 16px', display: 'flex', gap: 8,
                    alignItems: 'center', flexWrap: 'wrap',
                  }}>
                    <span style={{ fontSize: 12, color: 'var(--text2)', marginRight: 6 }}>
                      {cur.label}:
                    </span>
                    {opts.map(o => (
                      <button key={o.label} type="button"
                        onClick={() => advanceParam(cur.name, o.seconds)}
                        disabled={running}
                        style={{
                          padding: '5px 12px', borderRadius: 4, fontSize: 12,
                          background: 'var(--bg2)',
                          border: '1px solid var(--border)',
                          color: 'var(--text)', cursor: 'pointer',
                        }}>
                        {o.label}
                      </button>
                    ))}
                  </div>
                );
              }
              if (cur.kind === 'id-suggest') {
                return (
                  <>
                    <input ref={inputRef}
                      value={suggestQuery}
                      onChange={e => setSuggestQuery(e.target.value)}
                      placeholder={cur.placeholder || cur.label}
                      disabled={running}
                      style={{
                        border: 'none', outline: 'none',
                        background: 'transparent', color: 'var(--text)',
                        padding: '14px 16px', fontSize: 14,
                      }} />
                    <div style={{
                      maxHeight: 240, overflowY: 'auto',
                      borderTop: '1px solid var(--border)',
                    }}>
                      {suggestLoading && suggestResults.length === 0 && (
                        <div style={{ padding: 12, fontSize: 11, color: 'var(--text3)' }}>
                          Searching…
                        </div>
                      )}
                      {!suggestLoading && suggestResults.length === 0 && (
                        <div style={{ padding: 12, fontSize: 11, color: 'var(--text3)' }}>
                          No matches. Type to search.
                        </div>
                      )}
                      {suggestResults.map((r, i) => (
                        <div key={r.id}
                          onMouseEnter={() => setSuggestSelected(i)}
                          onClick={() => advanceParam(cur.name, r)}
                          style={{
                            padding: '8px 16px', cursor: 'pointer',
                            display: 'flex', alignItems: 'center', gap: 10,
                            background: i === suggestSelected ? 'var(--bg2)' : 'transparent',
                            borderLeft: i === suggestSelected
                              ? '2px solid var(--accent2)' : '2px solid transparent',
                          }}>
                          <span style={{ fontSize: 13, flex: 1 }}>{r.label}</span>
                          {r.hint && (
                            <span style={{ fontSize: 11, color: 'var(--text3)' }}>{r.hint}</span>
                          )}
                        </div>
                      ))}
                    </div>
                  </>
                );
              }
              // 'text'
              const v = paramValues[cur.name];
              const text = typeof v === 'string' ? v : '';
              return (
                <input ref={inputRef}
                  value={text}
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
                {(() => {
                  if (running) return 'Running…';
                  const cur = activeAction.params[paramIdx];
                  const isLast = paramIdx + 1 >= activeAction.params.length;
                  if (cur.kind === 'duration') {
                    return isLast ? 'Click a duration · Esc cancel' : 'Click a duration to continue';
                  }
                  if (cur.kind === 'id-suggest') {
                    return isLast ? '↑↓ ↵ pick · Esc cancel' : '↑↓ ↵ pick to continue';
                  }
                  return isLast ? '↵ run · Esc cancel' : '↵ next';
                })()}
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
          placeholder="Search services, endpoints, pages, run an action, or paste a trace id…"
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
                 : r.kind === 'endpoint' ? 'endpoint'
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
