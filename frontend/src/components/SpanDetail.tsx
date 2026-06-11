import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import type { SpanRow, ProfileRow, SpanHotspotsResponse, LogRow } from '@/lib/types';
import { tsLong, tsShort, sevName, sevClass, displaySpanName } from '@/lib/utils';
import { api } from '@/lib/api';
import { IconFlame, IconSparkles } from './icons';
import { CopyButton } from './CopyButton';
import { CopilotExplain } from './CopilotExplain';
import { BreakdownBar, KindBadge } from './KindBadge';

const PANEL_MIN = 300;
const PANEL_MAX = 1100;
const PANEL_STORAGE_KEY = 'coremetry-span-panel-w';

export function SpanDetail({ span, onClose }: { span: SpanRow; onClose: () => void }) {
  const attrs = Object.entries(span.attributes ?? {});
  const res = Object.entries(span.resourceAttributes ?? {});
  const allEvents = span.events ?? [];

  // OTel SemConv: exception data lives in events named "exception" with
  // attributes exception.{type,message,stacktrace}. Pull them out into a
  // dedicated section so devs see the stack trace immediately.
  const exceptions = allEvents.filter(e => e.name === 'exception');
  const otherEvents = allEvents.filter(e => e.name !== 'exception');

  // Some SDKs put the stacktrace directly on the span attrs instead.
  const inlineStack = (span.attributes?.['exception.stacktrace'] ?? span.attributes?.['error.stack']) as string | undefined;
  const inlineExType = span.attributes?.['exception.type'] as string | undefined;
  const inlineExMsg  = span.attributes?.['exception.message'] as string | undefined;
  const hasInlineException = inlineStack || inlineExType || inlineExMsg;

  // Trace-to-profile: look up profiles whose window overlaps this span
  const [profiles, setProfiles] = useState<ProfileRow[]>([]);
  // Aggregated hotspots over the span's window — same merge as
  // the Profiling page does for a service, but bounded to the
  // few profiles whose sample window touched this span. Lets
  // the operator see "what burned CPU during this span" inline,
  // without leaving the trace.
  const [spanHotspots, setSpanHotspots] = useState<SpanHotspotsResponse | null>(null);
  useEffect(() => {
    if (!span.serviceName || !span.startTime) { setProfiles([]); setSpanHotspots(null); return; }
    api.profilesForSpan(span.serviceName, span.startTime, span.endTime)
      .then(p => setProfiles(p ?? []))
      .catch(() => setProfiles([]));
    api.spanHotspots(span.serviceName, span.startTime, span.endTime, 10)
      .then(r => setSpanHotspots(r ?? null))
      .catch(() => setSpanHotspots(null));
  }, [span.spanId, span.serviceName, span.startTime, span.endTime]);

  // Trace-to-logs: fetch logs for the whole trace, not just
  // this span. Span-scoped filtering missed logs from sibling /
  // child spans an operator typically wants to see together
  // when triaging — and many spans don't have any directly-
  // attached log line, leaving the panel empty even though the
  // trace has plenty of logs. Same result the trace-detail
  // Logs tab shows, just lifted into the side panel for the
  // span the operator is hovering on.
  const [spanLogs, setSpanLogs] = useState<LogRow[]>([]);
  useEffect(() => {
    if (!span.traceId) { setSpanLogs([]); return; }
    api.logs({ traceId: span.traceId, limit: 50 })
      .then(r => setSpanLogs(r.logs ?? []))
      .catch(() => setSpanLogs([]));
  }, [span.traceId]);

  // Baseline p50 — the 24h leading up to this span, for the same
  // service+operation, off the RED metrics path (operation_summary_5m
  // via /api/services/{svc}/operations). Anchored to the span's own
  // end time, not now(), so an old trace compares against the
  // baseline that was current when it ran. Graceful: any miss
  // (endpoint error, operation not in the window) hides the row.
  const [baseP50, setBaseP50] = useState<number | null>(null);
  useEffect(() => {
    setBaseP50(null);
    if (!span.serviceName || !span.endTime) return;
    // Stale-response guard: rapid j/k span switching can leave an
    // older fetch resolving AFTER the newer one — without the flag
    // the previous span's p50 would land under the current span.
    let stale = false;
    const DAY_NS = 86_400_000_000_000;
    api.serviceOperations(span.serviceName, { from: span.endTime - DAY_NS, to: span.endTime })
      .then(ops => {
        if (stale) return;
        const op = (ops ?? []).find(o => o.name === span.name)
          ?? (ops ?? []).find(o => o.name === displaySpanName(span));
        setBaseP50(op && op.p50DurationMs > 0 ? op.p50DurationMs : null);
      })
      .catch(() => { if (!stale) setBaseP50(null); });
    return () => { stale = true; };
  }, [span.spanId, span.serviceName, span.name, span.endTime]);

  // ── Resize handle ────────────────────────────────────────────────────────
  // Panel width persists in localStorage so navigating away and back
  // doesn't reset to the default. Initial render reads it before the
  // first paint to avoid a flash from default → restored size.
  const [panelW, setPanelW] = useState<number>(() => {
    if (typeof window === 'undefined') return 340;
    const raw = parseInt(window.localStorage.getItem(PANEL_STORAGE_KEY) ?? '', 10);
    return Number.isFinite(raw) && raw >= PANEL_MIN && raw <= PANEL_MAX ? raw : 340;
  });
  const dragRef = useRef<{ startX: number; startW: number } | null>(null);
  const onResizeStart = (e: React.MouseEvent) => {
    e.preventDefault();
    dragRef.current = { startX: e.clientX, startW: panelW };
    document.body.style.cursor = 'col-resize';
    // Suppress text selection on the rest of the page while dragging.
    document.body.style.userSelect = 'none';
  };
  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      if (!dragRef.current) return;
      // Dragging the LEFT edge — moving the cursor left makes the panel
      // wider (it's pinned to the right side of the trace layout).
      const dx = dragRef.current.startX - e.clientX;
      const next = Math.max(PANEL_MIN, Math.min(PANEL_MAX, dragRef.current.startW + dx));
      setPanelW(next);
    };
    const onUp = () => {
      if (!dragRef.current) return;
      dragRef.current = null;
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
      window.localStorage.setItem(PANEL_STORAGE_KEY, String(panelW));
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, [panelW]);
  const onResetWidth = () => {
    setPanelW(340);
    window.localStorage.setItem(PANEL_STORAGE_KEY, '340');
  };

  // Esc closes the drawer (v0.8.112 — it floats over the waterfall now,
  // so a keyboard exit matters; skip when focus is in an input so Esc
  // keeps its native "clear field" behavior there).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
      onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <div id="span-panel" style={{ width: panelW }}>
      <div className="span-panel-resizer"
           title="Drag to resize · double-click to reset"
           onMouseDown={onResizeStart}
           onDoubleClick={onResetWidth} />
      <div id="span-panel-head">
        <div className="ps-title" title={displaySpanName(span) === span.name ? span.name : `raw: ${span.name}`}>
          {displaySpanName(span)}{' '}
          <span className={`badge ${span.statusCode === 'error' ? 'b-err' : 'b-ok'}`} style={{ marginLeft: 4 }}>
            {span.statusCode === 'error' ? 'ERROR' : 'OK'}
          </span>
        </div>
        <button className="ps-close" onClick={onClose}>✕</button>
      </div>
      <div id="span-panel-body">
        {/* AI explain (v0.5.144). Per-span LLM summary — backend
            sends only target + parent + direct children + error
            siblings so the prompt stays tight. Works with any
            configured copilot backend including a local LLM
            (Ollama / vLLM / LM Studio) via the openai-compatible
            base_url. Auto-hides when copilot isn't configured. */}
        {span.traceId && span.spanId && (
          <div style={{ marginBottom: 12 }}>
            <CopilotExplain kind="span" id={span.traceId} spanId={span.spanId}
              label={<><IconSparkles /> <span style={{ marginLeft: 6 }}>Explain this span</span></>} />
          </div>
        )}
        {/* Attributes — what the application code emitted; usually
            the most informative bit when debugging an unfamiliar span.
            "Info" (service/kind/timing/IDs) below: shape of the row,
            useful but rarely the answer to "what was this span doing?". */}
        {attrs.length > 0 && (
          <Section title={`Attributes (${attrs.length})`}>
            <KV>{attrs.map(([k, v]) => <Row key={k} k={k} v={String(v)} copyable />)}</KV>
          </Section>
        )}

        <Section title="Info">
          <KV>
            <Row k="Service" v={span.serviceName} copyable />
            <Row k="Kind" v={span.kind} copyable />
            <Row k="Duration" v={`${span.durationMs.toFixed(3)} ms`} copyable />
            {baseP50 !== null && (
              <tr>
                <td>Baseline p50 (24h)</td>
                <td title={`Median duration of ${span.serviceName} · ${displaySpanName(span)} over the 24h before this span`}>
                  {baseP50 >= 1000 ? `${(baseP50 / 1000).toFixed(2)} s` : `${baseP50.toFixed(baseP50 < 10 ? 2 : 0)} ms`}{' '}
                  {span.durationMs / baseP50 >= 1.5
                    ? <span className="badge b-err">×{(span.durationMs / baseP50).toFixed(1)} slower</span>
                    : <span className="badge b-ok">normal</span>}
                </td>
              </tr>
            )}
            <Row k="Start" v={tsLong(span.startTime)} copyable />
            <Row k="Trace ID" v={span.traceId} mono copyable />
            <Row k="Span ID" v={span.spanId} mono copyable />
            {span.parentSpanId && <Row k="Parent" v={span.parentSpanId} mono copyable />}
            {span.dbSystem && <Row k="DB System" v={span.dbSystem} copyable />}
            {span.dbStatement && <Row k="DB Statement" v={span.dbStatement} pre copyable />}
            {span.httpMethod && <Row k="HTTP" v={`${span.httpMethod} ${span.httpRoute ?? ''} ${span.httpStatus ?? ''}`} copyable />}
            {span.peerService && <Row k="Peer" v={span.peerService} copyable />}
            {span.statusMessage && <Row k="Status msg" v={span.statusMessage} copyable />}
          </KV>
        </Section>

        {res.length > 0 && (
          <Section title={`Resource (${res.length})`}>
            <KV>{res.map(([k, v]) => <Row key={k} k={k} v={String(v)} copyable />)}</KV>
          </Section>
        )}

        {(exceptions.length > 0 || hasInlineException) && (
          <Section title={`Exceptions (${exceptions.length || 1})`}>
            {exceptions.map((e, i) => (
              <ExceptionView key={i}
                type={e.attributes?.['exception.type']}
                message={e.attributes?.['exception.message']}
                stacktrace={e.attributes?.['exception.stacktrace']}
                escaped={e.attributes?.['exception.escaped']}
                time={e.timeNano} />
            ))}
            {exceptions.length === 0 && hasInlineException && (
              <ExceptionView
                type={inlineExType}
                message={inlineExMsg}
                stacktrace={inlineStack}
                time={span.startTime} />
            )}
          </Section>
        )}

        {otherEvents.length > 0 && (
          <Section title={`Events (${otherEvents.length})`}>
            {otherEvents.map((e, i) => (
              <div key={i} className="ps-event">
                <b>{e.name}</b>{' '}
                <span style={{ color: 'var(--text2)' }}>{tsLong(e.timeNano)}</span>
                {Object.keys(e.attributes ?? {}).length > 0 && (
                  <table className="ps-kv" style={{ marginTop: 4 }}>
                    <tbody>
                      {Object.entries(e.attributes ?? {}).map(([k, v]) => (
                        <tr key={k}><td>{k}</td><td>{String(v)}</td></tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            ))}
          </Section>
        )}

        <Section title={
          <>
            Logs ({spanLogs.length})
            <Link to={`/logs?traceId=${span.traceId}`}
              style={{ marginLeft: 8, fontSize: 10, fontWeight: 400, color: 'var(--accent2)' }}>
              open in Logs ↗
            </Link>
          </>
        }>
          {spanLogs.length === 0 ? (
            <div style={{ fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
              No logs attached to this span
            </div>
          ) : (
            spanLogs.map(l => (
              <div key={l.id} className="ps-log">
                <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <span className={sevClass(l.severity)} style={{ fontSize: 10, fontWeight: 700, minWidth: 42 }}>
                    {l.severityText || sevName(l.severity)}
                  </span>
                  <span style={{ fontSize: 10, color: 'var(--text3)', fontFamily: 'monospace' }}>
                    {tsShort(l.timestamp)}
                  </span>
                </div>
                <div style={{ fontSize: 11, marginTop: 2, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{l.body}</div>
              </div>
            ))
          )}
        </Section>

        {/* Inline hotspots — only render when the span window
            had at least one parseable profile. Operators don't
            need a navigation to see "this span's time burned in
            method X". Click the row → open the profile that
            contributed the most samples to it (we don't track
            attribution per-row at this granularity, so it
            opens the most recent profile in the window). */}
        {spanHotspots && spanHotspots.profilesUsed > 0 && spanHotspots.hotspots.length > 0 && (
          <Section title={`Top methods in span window (${spanHotspots.profilesUsed} profiles merged)`}>
            <BreakdownBar b={spanHotspots.breakdown} />
            <table className="ps-kv" style={{ width: '100%', fontSize: 11 }}>
              <tbody>
                {spanHotspots.hotspots.map((h, i) => {
                  const total = spanHotspots.totalSamples || 1;
                  const pct = (h.self / total) * 100;
                  return (
                    <tr key={i}>
                      <td style={{ fontFamily: 'monospace', wordBreak: 'break-all', paddingRight: 6 }} title={h.name}>
                        {clipMethod(h.name)}
                        <KindBadge kind={h.kind} />
                      </td>
                      <td className="num mono" style={{ whiteSpace: 'nowrap', color: 'var(--text2)', width: 80 }}>
                        {pct.toFixed(1)}%
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </Section>
        )}

        {profiles.length > 0 && (
          <Section title={`Profiles in window (${profiles.length})`}>
            {profiles.map(p => (
              <Link key={p.profileId} to={`/profile?id=${p.profileId}`}
                className="ps-event"
                style={{ display: 'flex', alignItems: 'center', gap: 8, textDecoration: 'none', color: 'var(--text)' }}>
                <span className="badge b-info">{p.profileType.toUpperCase()}</span>
                <span style={{ flex: 1, fontFamily: 'monospace', fontSize: 11 }}>
                  {tsLong(p.startTime)} {p.durationMs > 0 && `· ${(p.durationMs/1000).toFixed(1)}s`}
                </span>
                <span style={{ color: 'var(--accent2)', display: 'inline-flex' }}>
                  <IconFlame size={14} />
                </span>
              </Link>
            ))}
          </Section>
        )}
      </div>
    </div>
  );
}

function Section({ title, children }: { title: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="ps-sec">
      <div className="ps-sec-title">{title}</div>
      {children}
    </div>
  );
}

function KV({ children }: { children: React.ReactNode }) {
  return <table className="ps-kv"><tbody>{children}</tbody></table>;
}

/**
 * Renders one OTel-style exception block (type / message / stacktrace).
 * Stacktrace is shown in a scrollable monospace pre with a copy button.
 */
function ExceptionView({ type, message, stacktrace, escaped, time }: {
  type?: string;
  message?: string;
  stacktrace?: string;
  escaped?: string | boolean;
  time?: number;
}) {
  const [collapsed, setCollapsed] = useState(false);
  const stack = (stacktrace ?? '').toString();
  const escFlag = escaped === true || escaped === 'true';

  return (
    <div className="ex">
      <div className="ex-head">
        {type && <span className="ex-type">{type}</span>}
        {message && <span className="ex-msg">{message}</span>}
        {escFlag && <span className="badge b-err" style={{ marginLeft: 6 }}>ESCAPED</span>}
        <div style={{ marginLeft: 'auto', display: 'flex', gap: 6, alignItems: 'center' }}>
          {time && <span style={{ color: 'var(--text3)', fontSize: 11 }}>{tsLong(time)}</span>}
          {stack && <CopyButton value={stack} title="Copy stacktrace" />}
          {stack && (
            <button className="ex-toggle" type="button"
              onClick={() => setCollapsed(c => !c)}
              title={collapsed ? 'Expand' : 'Collapse'}>
              {collapsed ? '▸' : '▾'}
            </button>
          )}
        </div>
      </div>
      {stack && !collapsed && (
        <pre className="ex-stack">{formatStack(stack)}</pre>
      )}
    </div>
  );
}

// Light syntax-aware reflow:
//   • Java / Go style "at pkg.Class.method(File.java:42)" lines kept verbatim
//   • Caused-by / Suppressed lines highlighted by leaving them at column 0
//   • Plain newline split — defensive against single-line dumps
function formatStack(s: string): string {
  return s.replace(/\r\n/g, '\n').trimEnd();
}

// clipMethod shortens a fully-qualified function name for the
// narrow side-panel. Keeps the trailing token (the method
// itself) and prefixes "…" so the dropped namespace stays
// hinted. Full name is still on the row title attribute for
// hover.
function clipMethod(name: string, maxChars = 48): string {
  if (name.length <= maxChars) return name;
  return '…' + name.slice(name.length - maxChars + 1);
}

function Row({ k, v, mono, pre, copyable }: {
  k: string; v: string; mono?: boolean; pre?: boolean; copyable?: boolean;
}) {
  const style: React.CSSProperties = {};
  if (mono) style.wordBreak = 'break-all';
  if (pre) style.whiteSpace = 'pre-wrap';
  return (
    <tr>
      <td>{k}</td>
      <td style={style}>
        {v}
        {copyable && v && <CopyButton value={v} title={`Copy ${k.toLowerCase()}`} />}
      </td>
    </tr>
  );
}

