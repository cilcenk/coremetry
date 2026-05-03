'use client';
import { useEffect, useState } from 'react';
import Link from 'next/link';
import type { SpanRow, ProfileRow, LogRow } from '@/lib/types';
import { tsLong, tsShort, sevName, sevClass } from '@/lib/utils';
import { api } from '@/lib/api';
import { CopyButton } from './CopyButton';

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
  useEffect(() => {
    if (!span.serviceName || !span.startTime) { setProfiles([]); return; }
    api.profilesForSpan(span.serviceName, span.startTime, span.endTime)
      .then(p => setProfiles(p ?? []))
      .catch(() => setProfiles([]));
  }, [span.spanId, span.serviceName, span.startTime, span.endTime]);

  // Trace-to-logs: fetch logs attached to this exact span
  const [spanLogs, setSpanLogs] = useState<LogRow[]>([]);
  useEffect(() => {
    if (!span.spanId) { setSpanLogs([]); return; }
    api.logs({ traceId: span.traceId, spanId: span.spanId, limit: 50 })
      .then(r => setSpanLogs(r.logs ?? []))
      .catch(() => setSpanLogs([]));
  }, [span.spanId, span.traceId]);

  return (
    <div id="span-panel">
      <div id="span-panel-head">
        <div className="ps-title">
          {span.name}{' '}
          <span className={`badge ${span.statusCode === 'error' ? 'b-err' : 'b-ok'}`} style={{ marginLeft: 4 }}>
            {span.statusCode === 'error' ? 'ERROR' : 'OK'}
          </span>
        </div>
        <button className="ps-close" onClick={onClose}>✕</button>
      </div>
      <div id="span-panel-body">
        <Section title="Info">
          <KV>
            <Row k="Service" v={span.serviceName} />
            <Row k="Kind" v={span.kind} />
            <Row k="Duration" v={`${span.durationMs.toFixed(3)} ms`} />
            <Row k="Start" v={tsLong(span.startTime)} />
            <Row k="Trace ID" v={span.traceId} mono copyable />
            <Row k="Span ID" v={span.spanId} mono copyable />
            {span.parentSpanId && <Row k="Parent" v={span.parentSpanId} mono copyable />}
            {span.dbSystem && <Row k="DB System" v={span.dbSystem} />}
            {span.dbStatement && <Row k="DB Statement" v={span.dbStatement} pre />}
            {span.httpMethod && <Row k="HTTP" v={`${span.httpMethod} ${span.httpRoute ?? ''} ${span.httpStatus ?? ''}`} />}
            {span.peerService && <Row k="Peer" v={span.peerService} />}
            {span.statusMessage && <Row k="Status msg" v={span.statusMessage} />}
          </KV>
        </Section>

        {attrs.length > 0 && (
          <Section title={`Attributes (${attrs.length})`}>
            <KV>{attrs.map(([k, v]) => <Row key={k} k={k} v={String(v)} />)}</KV>
          </Section>
        )}

        {res.length > 0 && (
          <Section title={`Resource (${res.length})`}>
            <KV>{res.map(([k, v]) => <Row key={k} k={k} v={String(v)} />)}</KV>
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
            <Link href={`/logs?traceId=${span.traceId}&spanId=${span.spanId}`}
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
                <div style={{ fontSize: 11, marginTop: 2, wordBreak: 'break-word' }}>{l.body}</div>
              </div>
            ))
          )}
        </Section>

        {profiles.length > 0 && (
          <Section title={`Profiles in window (${profiles.length})`}>
            {profiles.map(p => (
              <Link key={p.profileId} href={`/profile?id=${p.profileId}`}
                className="ps-event"
                style={{ display: 'flex', alignItems: 'center', gap: 8, textDecoration: 'none', color: 'var(--text)' }}>
                <span className="badge b-info">{p.profileType.toUpperCase()}</span>
                <span style={{ flex: 1, fontFamily: 'monospace', fontSize: 11 }}>
                  {tsLong(p.startTime)} {p.durationMs > 0 && `· ${(p.durationMs/1000).toFixed(1)}s`}
                </span>
                <span style={{ color: 'var(--accent2)' }}>🔥</span>
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
