'use client';
import { Suspense, useEffect, useRef, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TraceWaterfall } from '@/components/TraceWaterfall';
import { SpanDetail } from '@/components/SpanDetail';
import { CopyButton } from '@/components/CopyButton';
import { CopilotExplain } from '@/components/CopilotExplain';
import { IconLink, IconCheck, IconDownload, IconSparkles } from '@/components/icons';
import { api } from '@/lib/api';
import { fmtNs, tsLong } from '@/lib/utils';
import type { LogRow, SpanRow, TimeRange } from '@/lib/types';

function TraceDetailInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const id = searchParams.get('id') ?? '';

  const [range, setRange] = useState<TimeRange>({ preset: '1h' });
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
    api.trace(id).then(d => setSpans(d.spans ?? [])).catch(() => setSpans(null));
  }, [id]);

  useEffect(() => {
    if (tab !== 'logs' || !id || logs !== undefined) return;
    api.logs({ traceId: id, limit: 500 })
      .then(r => setLogs(r.logs ?? []))
      .catch(() => setLogs(null));
  }, [tab, id, logs]);

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
  const root = spans?.find(s => !s.parentSpanId) ?? spans?.[0];
  const minT = spans && spans.length ? Math.min(...spans.map(s => s.startTime)) : 0;
  const maxT = spans && spans.length ? Math.max(...spans.map(s => s.endTime)) : 0;
  const totalNs = maxT - minT;
  const hasErr = spans?.some(s => s.statusCode === 'error') ?? false;


  return (
    <>
      <Topbar title="Trace Detail" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 10, display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <button className="sec" onClick={() => router.back()}>← Back</button>
          <code style={{ fontSize: 11, color: 'var(--text2)', background: 'var(--bg2)', padding: '2px 6px', borderRadius: 4 }}>
            {id}<CopyButton value={id} title="Copy trace ID" />
          </code>
          {spans && spans.length > 0 && (
            <>
              <span className={`badge ${hasErr ? 'b-err' : 'b-ok'}`}>{hasErr ? 'ERROR' : 'OK'}</span>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>{spans.length} spans · {fmtNs(totalNs)}</span>
              {root && <span style={{ color: 'var(--text3)', fontSize: 12 }}>{tsLong(root.startTime)}</span>}
              <span style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
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
            <div style={{ marginBottom: 10 }}>
              <CopilotExplain kind="trace" id={id} label={<><IconSparkles /> <span style={{ marginLeft: 6 }}>Explain this trace</span></>} />
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
                  <TraceWaterfall spans={spans} selectedId={selectedId} onSelect={setSelectedId} />
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
  // Ascending chronological order — easier to read with the trace
  // waterfall above mentally lined up to the same timeline.
  const sorted = [...logs].sort((a, b) => a.timestamp - b.timestamp);
  return (
    <div className="trace-logs">
      {sorted.map((l, i) => {
        const sev = l.severityText || severityFromNumber(l.severity);
        const sevCls = severityClass(sev);
        return (
          <div key={i} className="trace-logs-row">
            <span className="trace-logs-ts" title={tsLong(l.timestamp)}>{fmtClock(l.timestamp)}</span>
            <span className={`trace-logs-sev ${sevCls}`}>{sev || 'info'}</span>
            <span className="trace-logs-svc">{l.serviceName || '—'}</span>
            <span className="trace-logs-msg">{l.body}</span>
            {l.spanId && (
              <span className="trace-logs-span" title={`span_id: ${l.spanId}`}>
                {l.spanId.slice(0, 12)}
              </span>
            )}
          </div>
        );
      })}
    </div>
  );
}

function severityFromNumber(n: number): string {
  // OTel SeverityNumber bands (1-4 trace, 5-8 debug, 9-12 info, 13-16 warn, 17-20 error, 21-24 fatal)
  if (n >= 21) return 'fatal';
  if (n >= 17) return 'error';
  if (n >= 13) return 'warn';
  if (n >= 9)  return 'info';
  if (n >= 5)  return 'debug';
  return 'trace';
}
function severityClass(s: string): string {
  const lc = (s || '').toLowerCase();
  if (lc.startsWith('fatal') || lc.startsWith('crit')) return 'sev-fatal';
  if (lc.startsWith('err')) return 'sev-err';
  if (lc.startsWith('warn')) return 'sev-warn';
  if (lc.startsWith('info') || lc === 'notice') return 'sev-info';
  if (lc.startsWith('debug')) return 'sev-debug';
  return 'sev-trace';
}
// HH:MM:SS.mmm formatter — drops the date prefix because all logs in
// the panel belong to the same trace (often <1s wide).
function fmtClock(ns: number): string {
  const d = new Date(ns / 1e6);
  const pad = (n: number, w = 2) => n.toString().padStart(w, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
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

  // Click-outside to close.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
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
      const res = await api.shareTrace(traceId, 24);
      setPublicURL(res.url);
      setPublicExpiresAt(res.expiresAt);
      // Auto-copy on generate so the common case is one-click.
      await copyToClipboard(res.url);
      setPublicCopied(true);
      setTimeout(() => setPublicCopied(false), 2000);
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
            Expires in 24 hours.
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
                <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 6 }}>
                  Expires {tsLong(publicExpiresAt)}
                </div>
              )}
            </>
          )}
          {error && (
            <div style={{ marginTop: 8, fontSize: 11, color: 'var(--err)' }}>{error}</div>
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
