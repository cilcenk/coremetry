import { Suspense, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TraceWaterfall } from '@/components/TraceWaterfall';
import { computeCriticalPath } from '@/lib/criticalPath';
import { SpanDetail } from '@/components/SpanDetail';
import { CopyButton } from '@/components/CopyButton';
import { LogTable } from '@/components/LogTable';
import { CopilotExplain } from '@/components/CopilotExplain';
import { IconLink, IconCheck, IconDownload, IconSparkles } from '@/components/icons';
import { useShortcuts } from '@/lib/keyboard';
import { api } from '@/lib/api';
import { fmtNs, tsLong, tsRel } from '@/lib/utils';
import type { LogRow, SpanRow, TimeRange } from '@/lib/types';

function TraceDetailInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const id = searchParams.get('id') ?? '';

  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  const [spans, setSpans] = useState<SpanRow[] | null | undefined>(undefined);
  // selectedId + tab are URL-bound so a Share-button copy round-
  // trips: "open trace X with the rpc-call span focused on the Logs
  // tab" comes back identical when pasted in another browser.
  const [selectedId, setSelectedId] = useState<string | null>(
    () => searchParams.get('span'));
  // Side-tab state — Trace (waterfall + detail) vs Logs (entries
  // matching this trace_id, Uptrace-style). Logs are fetched lazily
  // on first tab click so the trace page stays fast for users who
  // never need them.
  const [tab, setTab] = useState<'trace' | 'logs'>(
    () => (searchParams.get('tab') === 'logs' ? 'logs' : 'trace'));
  const [logs, setLogs] = useState<LogRow[] | null | undefined>(undefined);

  useEffect(() => {
    if (!id) return;
    setSpans(undefined);
    // Reset logs too so the next Logs-tab open re-fetches.
    // Without this, navigating from one trace to another
    // (React Router preserves component state across
    // searchParams changes) kept the previous trace's logs
    // result — including a stale empty array, which fooled
    // the `logs !== undefined` guard in the next effect
    // into skipping the fetch entirely. The operator saw
    // "no logs for this trace" even when the log was
    // exactly the one they'd clicked from /logs to get here.
    setLogs(undefined);
    api.trace(id).then(d => setSpans(d.spans ?? [])).catch(() => setSpans(null));
  }, [id]);

  useEffect(() => {
    if (tab !== 'logs' || !id || logs !== undefined) return;
    // Wait for spans to load — they give us the trace's time
    // bounds, which we send to the logs backend as a narrow
    // range filter. Without this the ES query has to scan the
    // full retention; with it ES prunes partitions to the few
    // minutes around the trace, dropping query time from
    // seconds to tens of ms (the trick Grafana's "trace to
    // logs" datasource uses for sub-second speed).
    //
    // spans === undefined means still loading; null means
    // load failed and we should fall through with no time
    // bound (best-effort instead of blocking on a sibling
    // signal).
    if (spans === undefined) return;
    let from: number | undefined;
    let to: number | undefined;
    if (spans && spans.length > 0) {
      let minTs = Infinity, maxTs = -Infinity;
      for (const sp of spans) {
        if (sp.startTime < minTs) minTs = sp.startTime;
        const end = sp.endTime || sp.startTime;
        if (end > maxTs) maxTs = end;
      }
      // ±5 min buffer absorbs clock skew between the app
      // emitting the trace and the log shipper writing to
      // ES, plus end-of-trace logs that fire after the root
      // span returned.
      const bufferNs = 5 * 60 * 1_000_000_000;
      from = minTs - bufferNs;
      to   = maxTs + bufferNs;
    }
    api.logs({ traceId: id, from, to, limit: 500 })
      .then(r => setLogs(r.logs ?? []))
      .catch(() => setLogs(null));
  }, [tab, id, logs, spans]);

  // Mirror selectedId + tab to the URL via replaceState so the Share
  // button captures the current view exactly. We don't push history
  // — selecting a span shouldn't add a back-button stop.
  useEffect(() => {
    if (typeof window === 'undefined' || !id) return;
    const url = new URL(window.location.href);
    if (selectedId) url.searchParams.set('span', selectedId);
    else url.searchParams.delete('span');
    if (tab === 'logs') url.searchParams.set('tab', 'logs');
    else url.searchParams.delete('tab');
    window.history.replaceState({}, '', url.toString());
  }, [selectedId, tab, id]);

  if (!id) {
    return (
      <>
        <Topbar title="Trace" range={range} onRangeChange={setRange} />
        <div id="content"><Empty icon="⚠" title="Missing trace id" /></div>
      </>
    );
  }

  const sel = spans?.find(s => s.spanId === selectedId) ?? null;

  // Visible-order span list for j/k navigation. Same DFS the
  // waterfall renders — sort all spans by parent + start
  // time, then walk depth-first so j/k step through rows in
  // the order an operator's eye scans them.
  const orderedSpanIds = useMemo<string[]>(() => {
    if (!spans || spans.length === 0) return [];
    const byParent = new Map<string, SpanRow[]>();
    for (const sp of spans) {
      const pid = sp.parentSpanId || '';
      const list = byParent.get(pid);
      if (list) list.push(sp);
      else byParent.set(pid, [sp]);
    }
    for (const list of byParent.values()) {
      list.sort((a, b) => a.startTime - b.startTime);
    }
    const out: string[] = [];
    const walk = (parentId: string) => {
      for (const sp of byParent.get(parentId) ?? []) {
        out.push(sp.spanId);
        walk(sp.spanId);
      }
    };
    // Roots first: any span whose parent is empty OR refers to
    // a parent not in the trace. Sorted by start time so the
    // order is deterministic across multi-root edge cases.
    const ids = new Set(spans.map(s => s.spanId));
    const roots = spans
      .filter(s => !s.parentSpanId || !ids.has(s.parentSpanId))
      .sort((a, b) => a.startTime - b.startTime);
    for (const r of roots) {
      out.push(r.spanId);
      walk(r.spanId);
    }
    return out;
  }, [spans]);

  // j/k step + g g / G + Enter / Esc — the same vocabulary
  // useTableNav installs for list pages; the waterfall has
  // its own row layout so it gets a hand-rolled binding rather
  // than the hook.
  useShortcuts([
    {
      keys: 'j', label: 'Next span', group: 'Trace',
      handler: () => {
        if (orderedSpanIds.length === 0) return;
        const i = selectedId ? orderedSpanIds.indexOf(selectedId) : -1;
        const next = Math.min(orderedSpanIds.length - 1, i + 1);
        setSelectedId(orderedSpanIds[next] ?? null);
      },
    },
    {
      keys: 'k', label: 'Previous span', group: 'Trace',
      handler: () => {
        if (orderedSpanIds.length === 0) return;
        const i = selectedId ? orderedSpanIds.indexOf(selectedId) : 0;
        const prev = Math.max(0, i - 1);
        setSelectedId(orderedSpanIds[prev] ?? null);
      },
    },
    {
      keys: 'g g', label: 'Jump to first span', group: 'Trace',
      handler: () => {
        if (orderedSpanIds.length > 0) setSelectedId(orderedSpanIds[0]);
      },
    },
    {
      keys: 'shift+g', label: 'Jump to last span', group: 'Trace',
      handler: () => {
        if (orderedSpanIds.length > 0) {
          setSelectedId(orderedSpanIds[orderedSpanIds.length - 1]);
        }
      },
    },
    {
      keys: 'Escape', label: 'Close span detail', group: 'Trace',
      evenInInputs: true,
      handler: () => setSelectedId(null),
    },
  ], [orderedSpanIds, selectedId]);
  const root = spans?.find(s => !s.parentSpanId) ?? spans?.[0];
  const minT = spans && spans.length ? Math.min(...spans.map(s => s.startTime)) : 0;
  const maxT = spans && spans.length ? Math.max(...spans.map(s => s.endTime)) : 0;

  // Critical path — synchronous longest chain through the
  // span DAG. Cheap O(N) DFS; useMemo only recomputes when
  // the span list identity changes (i.e., when a new trace
  // is loaded). Operator can hide the highlight via the
  // toolbar toggle.
  const [showCritical, setShowCritical] = useState(true);
  const criticalPath = useMemo(() => {
    if (!spans || spans.length === 0) return null;
    return computeCriticalPath(spans.map(s => ({
      spanId: s.spanId,
      parentId: s.parentSpanId ?? '',
      startTime: s.startTime,
      duration: s.endTime - s.startTime,
    })));
  }, [spans]);
  const criticalPathIds = (showCritical && criticalPath) ? criticalPath.ids : undefined;
  const totalNs = maxT - minT;
  const hasErr = spans?.some(s => s.statusCode === 'error') ?? false;


  return (
    <>
      <Topbar title="Trace Detail" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 10, display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <button className="sec" onClick={() => navigate(-1)}>← Back</button>
          <code style={{ fontSize: 11, color: 'var(--text2)', background: 'var(--bg2)', padding: '2px 6px', borderRadius: 4 }}>
            {id}<CopyButton value={id} title="Copy trace ID" />
          </code>
          {spans && spans.length > 0 && (
            <>
              <span className={`badge ${hasErr ? 'b-err' : 'b-ok'}`}>{hasErr ? 'ERROR' : 'OK'}</span>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>{spans.length} spans · {fmtNs(totalNs)}</span>
              {root && <span style={{ color: 'var(--text3)', fontSize: 12 }}>{tsLong(root.startTime)}</span>}
              <span style={{ marginLeft: 'auto', display: 'flex', gap: 6, alignItems: 'center' }}>
                {/* Critical path summary — when computed, the
                    chain's total duration tells the operator
                    how much of the trace's wall-clock time
                    happens on the dominant path. Toggle hides
                    the highlight without recomputing. */}
                {criticalPath && criticalPath.ids.size > 0 && (
                  <label style={{
                    display: 'inline-flex', alignItems: 'center', gap: 6,
                    fontSize: 11, color: 'var(--text2)', marginRight: 4,
                  }} title={`${criticalPath.ids.size} spans summing to ${fmtNs(criticalPath.totalNs)}. The synchronous longest chain through the trace's DAG.`}>
                    <input type="checkbox"
                           checked={showCritical}
                           onChange={e => setShowCritical(e.target.checked)} />
                    Critical path · {fmtNs(criticalPath.totalNs)}
                  </label>
                )}
                {/* Compare button — bumped to primary-accent in
                    v0.4.96 because the secondary-style version
                    blended into the action chip row and
                    operators kept asking "how do I diff two
                    traces". Same destination, just visually
                    promoted. */}
                <Link to={`/trace/compare?a=${encodeURIComponent(id)}`}
                      title="Compare this trace side-by-side with another (operation-level diff)"
                      style={{
                        fontSize: 12, padding: '4px 12px',
                        display: 'inline-flex', alignItems: 'center', gap: 6,
                        textDecoration: 'none', fontWeight: 600,
                        background: 'rgba(56,139,253,0.15)',
                        color: 'var(--accent2)',
                        border: '1px solid rgba(56,139,253,0.45)',
                        borderRadius: 6,
                      }}>
                  ↔ Compare trace
                </Link>
                <SharePopover traceId={id} />
                <button className="sec"
                  onClick={() => exportTraceJSON(id, spans)}
                  title="Download this trace as JSON (full span list with attributes + events)"
                  style={{ fontSize: 12, padding: '3px 10px', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                  <IconDownload /> <span>Export JSON</span>
                </button>
              </span>
            </>
          )}
        </div>

        {spans === undefined && <Spinner />}
        {spans && spans.length === 0 && <Empty icon="⋮" title="Trace not found" />}
        {spans && spans.length > 0 && (
          <>
            <div style={{ marginBottom: 10, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <CopilotExplain kind="trace" id={id} label={<><IconSparkles /> <span style={{ marginLeft: 6 }}>Explain this trace</span></>} />
              <CompareTracesButton aId={id} />
            </div>

            {/* Trace vs Logs (Uptrace-style) — uses the shared
                .tab-strip pattern so it visually matches Settings,
                Exceptions inbox, and Status Page admin tabs. */}
            <div className="tab-strip" style={{ marginBottom: 10 }}>
              <TabBtn active={tab === 'trace'} onClick={() => setTab('trace')}>
                Trace <span style={{ color: 'var(--text3)', marginLeft: 4 }}>{spans.length}</span>
              </TabBtn>
              <TabBtn active={tab === 'logs'} onClick={() => setTab('logs')}>
                Logs {logs && <span style={{ color: 'var(--text3)', marginLeft: 4 }}>{logs.length}</span>}
              </TabBtn>
            </div>

            {tab === 'trace' && (
              <div id="td-outer">
                <div id="td-wf">
                  <TraceWaterfall spans={spans} selectedId={selectedId} onSelect={setSelectedId}
                                  criticalPathIds={criticalPathIds} />
                </div>
                {sel && <SpanDetail span={sel} onClose={() => setSelectedId(null)} />}
              </div>
            )}

            {tab === 'logs' && (
              <TraceLogsPanel logs={logs} />
            )}
          </>
        )}
      </div>
    </>
  );
}

function TabBtn({ active, onClick, children }: {
  active: boolean; onClick: () => void; children: React.ReactNode;
}) {
  return (
    <button onClick={onClick} className={active ? 'active' : ''}>{children}</button>
  );
}

// TraceLogsPanel — flat list of log entries that share this trace
// id, ordered chronologically. Layout mirrors Uptrace's trace→logs
// tab: timestamp · severity · service · message preview, with the
// span_id shown as a smaller tag so the operator can correlate
// "this log line belongs to that span".
function TraceLogsPanel({ logs }: { logs: LogRow[] | null | undefined }) {
  if (logs === undefined) return <Spinner />;
  if (logs === null) return <Empty icon="⚠" title="Failed to load logs" />;
  if (logs.length === 0) {
    return <Empty icon="≡" title="No logs for this trace">
      Make sure your collector ships logs with the W3C trace context (trace_id + span_id) populated.
    </Empty>;
  }
  // Ascending chronological order — lines up with the trace
  // waterfall above. Display reuses the shared <LogTable> so
  // severity colouring / row expand layout / attribute tables
  // stay consistent with the /logs page; operators don't
  // re-learn a second viewer when they drill in from a trace.
  const sorted = [...logs].sort((a, b) => a.timestamp - b.timestamp);
  return (
    <>
      <div style={{
        display: 'flex', gap: 10, padding: '6px 10px',
        fontSize: 11, color: 'var(--text3)',
      }}>
        <span>{sorted.length} log line{sorted.length === 1 ? '' : 's'}</span>
      </div>
      <LogTable logs={sorted} hideTraceColumn />
    </>
  );
}

// Severity-helpers + per-log time formatter used to live
// here for the legacy custom .trace-logs grid. After v0.5.62
// the panel renders through the shared <LogTable> which
// handles all of that itself, so the helpers are gone.

// CompareTracesButton — opens an inline form asking for a
// second trace ID, then calls /api/copilot/compare-traces.
// Renders the model's diff explanation underneath in the
// same panel style as CopilotExplain so the trace-detail
// chrome stays visually consistent. Self-hides when the
// copilot isn't configured (same gate CopilotExplain uses).
function CompareTracesButton({ aId }: { aId: string }) {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [open, setOpen] = useState(false);
  const [bId, setBId] = useState('');
  const [busy, setBusy] = useState(false);
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);
  if (enabled !== true) return null;

  const submit = async () => {
    const b = bId.trim().toLowerCase().replace(/^0x/, '');
    if (!/^[0-9a-f]{16,32}$/.test(b)) {
      setError('Trace ID must be 16-32 hex characters.');
      return;
    }
    if (b === aId.toLowerCase()) {
      setError('Pick a DIFFERENT trace to compare with.');
      return;
    }
    setBusy(true); setError(null); setText(null);
    try {
      const r = await api.copilotCompareTraces(aId, b);
      setText(r.explanation);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Compare failed');
    } finally {
      setBusy(false);
    }
  };

  if (!open) {
    return (
      <button onClick={() => setOpen(true)} className="sec"
        style={{ padding: '5px 12px', fontSize: 12, color: 'var(--accent2)',
                 display: 'inline-flex', alignItems: 'center', gap: 6 }}
        title="Diff this trace against another by ID — model explains why they diverged">
        <IconSparkles /> <span>Compare with…</span>
      </button>
    );
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, width: '100%' }}>
      <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
        <input value={bId}
          onChange={e => setBId(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') submit(); }}
          placeholder="Other trace ID (hex)"
          className="mono"
          style={{ width: 320, fontSize: 12 }} />
        <button onClick={submit} disabled={busy}
          style={{ padding: '5px 12px', fontSize: 12 }}>
          {busy ? 'Thinking…' : 'Compare'}
        </button>
        <button onClick={() => { setOpen(false); setText(null); setError(null); }}
          className="sec" style={{ padding: '5px 10px', fontSize: 12 }}>
          Cancel
        </button>
      </div>
      {error && (
        <div style={{
          padding: 10, borderRadius: 6, fontSize: 12,
          background: 'rgba(255,82,82,.10)', color: 'var(--err)',
          border: '1px solid rgba(255,82,82,.25)', maxWidth: 720,
        }}>{error}</div>
      )}
      {text && (
        <div style={{
          padding: 12, borderRadius: 6, fontSize: 13, lineHeight: 1.5,
          background: 'rgba(56,139,253,.08)',
          border: '1px solid rgba(56,139,253,.25)',
          color: 'var(--text)', whiteSpace: 'pre-wrap', maxWidth: 720,
        }}>
          <div style={{ fontSize: 10, color: 'var(--accent2)', marginBottom: 6, fontWeight: 700, letterSpacing: '.5px',
                        display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <IconSparkles size={11} /> COMPARE
          </div>
          {text}
        </div>
      )}
    </div>
  );
}

export default function TraceDetailPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <TraceDetailInner />
    </Suspense>
  );
}

// copyToClipboard handles the async Clipboard API + a hidden-textarea
// fallback for non-secure dev hosts (Clipboard API requires HTTPS or
// localhost). Returns a Promise so the caller can flip a UI state on
// completion.
async function copyToClipboard(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch { /* fall through to legacy */ }
  }
  fallbackCopy(text);
}

// SharePopover — Grafana-style two-tab share popover. Internal link
// is the current URL (preserves span/tab/range from the page state
// mirror). Public link mints a 24h time-boxed token via
// /api/traces/{id}/share that resolves to a no-auth /public/trace
// page; useful for handing a trace to support, customers, or anyone
// outside Coremetry without giving them an account.
function SharePopover({ traceId }: { traceId: string }) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [internalCopied, setInternalCopied] = useState(false);
  const [publicURL, setPublicURL] = useState<string | null>(null);
  const [publicCopied, setPublicCopied] = useState(false);
  const [publicExpiresAt, setPublicExpiresAt] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // TTL picker — operator picks the share-link lifetime
  // instead of always getting 24h. Banking-scale operators
  // sharing with vendors / support tickets routinely want
  // 7-30d; in-team handoffs default to 24h.
  const [ttlHours, setTtlHours] = useState(24);
  // Active shares for this trace. Loaded when the popover
  // opens; refreshed after mint / revoke.
  type ShareRow = Awaited<ReturnType<typeof api.listTraceShares>>;
  const [shares, setShares] = useState<NonNullable<ShareRow>>([]);
  const reloadShares = async () => {
    try {
      const list = await api.listTraceShares(traceId);
      setShares(list ?? []);
    } catch { setShares([]); }
  };

  // Click-outside to close.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  // Fetch active shares when popover opens so the operator sees
  // what's already out there before minting another.
  useEffect(() => {
    if (open) void reloadShares();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const copyInternal = async () => {
    if (typeof window === 'undefined') return;
    await copyToClipboard(window.location.href);
    setInternalCopied(true);
    setTimeout(() => setInternalCopied(false), 2000);
  };

  const generatePublic = async () => {
    setBusy(true); setError(null);
    try {
      const res = await api.shareTrace(traceId, ttlHours);
      setPublicURL(res.url);
      setPublicExpiresAt(res.expiresAt);
      // Auto-copy on generate so the common case is one-click.
      await copyToClipboard(res.url);
      setPublicCopied(true);
      setTimeout(() => setPublicCopied(false), 2000);
      void reloadShares();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to mint share link');
    } finally {
      setBusy(false);
    }
  };

  const copyPublic = async () => {
    if (!publicURL) return;
    await copyToClipboard(publicURL);
    setPublicCopied(true);
    setTimeout(() => setPublicCopied(false), 2000);
  };

  const revoke = async (token: string) => {
    try {
      await api.revokeTraceShare(token);
      // If we revoked the one just minted, clear the URL slot
      // so the popover returns to the "generate" affordance.
      if (publicURL && publicURL.includes(token)) {
        setPublicURL(null);
        setPublicExpiresAt(null);
      }
      void reloadShares();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Revoke failed');
    }
  };

  return (
    <div ref={wrapRef} style={{ position: 'relative' }}>
      <button className="sec"
        onClick={() => setOpen(o => !o)}
        title="Share this trace"
        style={{ fontSize: 12, padding: '3px 10px',
                 display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <IconLink /> <span>Share</span>
      </button>
      {open && (
        <div role="dialog" style={{
          position: 'absolute', right: 0, top: 'calc(100% + 6px)', zIndex: 60,
          width: 380, padding: 12,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.30)',
        }}>
          {/* Internal link section */}
          <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)',
                        textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 6 }}>
            Internal link
          </div>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8, lineHeight: 1.5 }}>
            For Coremetry users — preserves your selected span, tab, and time range.
          </div>
          <button onClick={copyInternal} className="sec"
            style={{ width: '100%', fontSize: 12, padding: '6px 10px',
                     display: 'inline-flex', alignItems: 'center', gap: 6, justifyContent: 'center',
                     color: internalCopied ? 'var(--ok)' : undefined }}>
            {internalCopied ? <IconCheck /> : <IconLink />}
            <span>{internalCopied ? 'Copied' : 'Copy current URL'}</span>
          </button>

          {/* Divider */}
          <div style={{ borderTop: '1px solid var(--border)', margin: '14px -12px' }} />

          {/* Public link section */}
          <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)',
                        textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 6 }}>
            Public link
          </div>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8, lineHeight: 1.5 }}>
            Anyone with this URL can view a read-only snapshot — no Coremetry account needed.
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8, fontSize: 11 }}>
            <span style={{ color: 'var(--text2)' }}>Expires in:</span>
            <select value={ttlHours}
              onChange={e => setTtlHours(Number(e.target.value))}
              style={{ fontSize: 11, padding: '2px 4px' }}>
              <option value={1}>1 hour</option>
              <option value={24}>24 hours</option>
              <option value={24 * 7}>7 days</option>
              <option value={24 * 30}>30 days</option>
            </select>
          </div>
          {!publicURL ? (
            <button onClick={generatePublic} disabled={busy}
              style={{ width: '100%', fontSize: 12, padding: '6px 10px',
                       display: 'inline-flex', alignItems: 'center', gap: 6, justifyContent: 'center' }}>
              <IconLink /> <span>{busy ? 'Generating…' : 'Generate public link'}</span>
            </button>
          ) : (
            <>
              <div style={{ display: 'flex', gap: 6 }}>
                <input value={publicURL} readOnly
                  onClick={e => (e.target as HTMLInputElement).select()}
                  style={{ flex: 1, fontSize: 11, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }} />
                <button onClick={copyPublic} className="sec"
                  style={{ fontSize: 11, padding: '4px 10px',
                           display: 'inline-flex', alignItems: 'center', gap: 4,
                           color: publicCopied ? 'var(--ok)' : undefined }}>
                  {publicCopied ? <IconCheck /> : <IconLink />}
                  <span>{publicCopied ? 'Copied' : 'Copy'}</span>
                </button>
              </div>
              {publicExpiresAt && (
                <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 6 }}
                     title={tsLong(publicExpiresAt)}>
                  Expires {tsRel(publicExpiresAt)}
                </div>
              )}
            </>
          )}
          {error && (
            <div style={{ marginTop: 8, fontSize: 11, color: 'var(--err)' }}>{error}</div>
          )}
          {/* Active shares for this trace — operator audits
              what's already out there + revokes leaks
              without having to remember tokens. Empty list
              hides the whole block so the popover stays
              tidy when there's nothing to manage. */}
          {shares.length > 0 && (
            <div style={{ marginTop: 14, paddingTop: 12, borderTop: '1px solid var(--border)' }}>
              <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)',
                            textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 6 }}>
                Active shares ({shares.length})
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                {shares.map(s => (
                  <div key={s.token} style={{
                    display: 'flex', alignItems: 'center', gap: 6,
                    fontSize: 11, color: 'var(--text2)',
                  }}>
                    <span style={{
                      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                      flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }} title={`${s.createdBy || 'unknown'} · expires ${tsLong(s.expiresAt)}`}>
                      …{s.token.slice(-8)} · {s.createdBy || 'unknown'}
                    </span>
                    <span style={{ fontSize: 10, color: 'var(--text3)', whiteSpace: 'nowrap' }}
                          title={tsLong(s.expiresAt)}>
                      {tsRel(s.expiresAt)}
                    </span>
                    <button onClick={() => revoke(s.token)}
                      className="sec"
                      style={{ fontSize: 10, padding: '2px 6px', color: 'var(--err)' }}>
                      Revoke
                    </button>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function fallbackCopy(text: string) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  try { document.execCommand('copy'); } catch { /* swallow */ }
  ta.remove();
}

// exportTraceJSON triggers a browser download of the full trace as a
// pretty-printed JSON file. Filename includes a short trace-id prefix
// so a folder of exports stays scannable. Pure client-side — no
// extra round-trip; the spans are already loaded.
function exportTraceJSON(traceId: string, spans: unknown[]) {
  const payload = JSON.stringify({ traceId, spans }, null, 2);
  const blob = new Blob([payload], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `trace-${traceId.slice(0, 12)}.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
