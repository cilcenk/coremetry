// SpanPanel.tsx — sticky span-detail panel for the trace waterfall.
//
// Renders the selected span's identity + RED + attributes (grouped by OTel
// semantic-convention family via attrFamily), its span events — including
// exception.* events expanded to type/message/stacktrace — and its span LINKS
// (pointers to causally-related spans in OTHER traces) via the shared
// useSpanLinks hook. This is the OTel-correlation surface: the operator sees
// the full fidelity of the span without leaving the trace.
//
// CSS-var tokens only; resizable width persisted to localStorage.

import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useSpanLinks, attrFamily, spanStatus, type AttrFamily } from '@/lib/otel';
import { displaySpanName, fmtNs, tsLong } from '@/lib/utils';
import { CopyButton } from '@/components/CopyButton';
import type { SpanRow } from '@/lib/types';
import { svcColor, SpanKindChip } from './shared';

const PANEL_MIN = 320;
const PANEL_MAX = 920;
const PANEL_STORAGE_KEY = 'coremetry-trace-span-panel-w';

// Render order for the attribute family sections — operation-level families
// first (the operator's primary interest), resource families after.
const FAMILY_ORDER: AttrFamily[] = [
  'http', 'rpc', 'db', 'messaging', 'gen_ai', 'network', 'code', 'exception',
  'service', 'k8s', 'cloud', 'host', 'container', 'process', 'runtime',
  'telemetry', 'deployment', 'os', 'other',
];

const FAMILY_LABEL: Record<AttrFamily, string> = {
  service: 'Service', k8s: 'Kubernetes', cloud: 'Cloud', host: 'Host',
  process: 'Process', runtime: 'Runtime', telemetry: 'Telemetry SDK',
  deployment: 'Deployment', container: 'Container', os: 'OS',
  http: 'HTTP', rpc: 'RPC', db: 'Database', messaging: 'Messaging',
  network: 'Network', exception: 'Exception', gen_ai: 'GenAI',
  code: 'Code', other: 'Other',
};

export function SpanPanel({ span, onClose }: { span: SpanRow; onClose: () => void }) {
  const { links, exceptions } = useSpanLinks(span);

  const [panelW, setPanelW] = useState<number>(() => {
    try {
      const s = localStorage.getItem(PANEL_STORAGE_KEY);
      const n = s ? parseInt(s, 10) : NaN;
      if (Number.isFinite(n)) return Math.min(PANEL_MAX, Math.max(PANEL_MIN, n));
    } catch { /* ignore */ }
    return 420;
  });
  const startResize = (e: React.MouseEvent) => {
    e.preventDefault();
    // Anchor = (start clientX) + (start width). The panel grows as the cursor
    // moves LEFT (it's a right-side panel), so width = anchor − clientX.
    const anchor = e.clientX + panelW;
    let latest = panelW;
    const onMove = (ev: MouseEvent) => {
      latest = Math.min(PANEL_MAX, Math.max(PANEL_MIN, anchor - ev.clientX));
      setPanelW(latest);
    };
    const onUp = () => {
      try { localStorage.setItem(PANEL_STORAGE_KEY, String(latest)); } catch { /* ignore */ }
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  };

  // Group attributes (span + resource merged) by family, in render order.
  const grouped = useMemo(() => {
    const all: Array<[string, string]> = [
      ...Object.entries(span.attributes ?? {}),
      ...Object.entries(span.resourceAttributes ?? {}),
    ];
    const map = new Map<AttrFamily, Array<[string, string]>>();
    for (const [k, v] of all) {
      const fam = attrFamily(k);
      const list = map.get(fam);
      if (list) list.push([k, v]); else map.set(fam, [[k, v]]);
    }
    for (const list of map.values()) list.sort((a, b) => a[0].localeCompare(b[0]));
    return FAMILY_ORDER
      .filter(f => map.has(f))
      .map(f => ({ family: f, label: FAMILY_LABEL[f], rows: map.get(f)! }));
  }, [span.attributes, span.resourceAttributes]);

  const events = (span.events ?? []).filter(e => e.name !== 'exception');
  const status = spanStatus(span.statusCode);
  const err = status === 'error' || exceptions.length > 0;

  return (
    <div
      style={{
        width: panelW, minWidth: PANEL_MIN, flex: 'none',
        borderLeft: '1px solid var(--border)', background: 'var(--bg2)',
        position: 'relative', alignSelf: 'stretch',
        display: 'flex', flexDirection: 'column', overflow: 'hidden',
      }}>
      {/* resize grip */}
      <span
        onMouseDown={startResize}
        title="Drag to resize"
        style={{ position: 'absolute', left: 0, top: 0, width: 6, height: '100%', cursor: 'col-resize', zIndex: 2 }} />

      <div style={{ overflowY: 'auto', padding: 14 }}>
        {/* header */}
        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, marginBottom: 10 }}>
          <span style={{ width: 8, height: 8, borderRadius: 8, background: svcColor(span.serviceName), marginTop: 5, flex: 'none' }} />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontWeight: 600, fontSize: 13, wordBreak: 'break-word' }}>{displaySpanName(span)}</div>
            <div style={{ fontSize: 11, color: 'var(--text2)', marginTop: 2 }}>{span.serviceName || 'unknown'}</div>
          </div>
          <button onClick={onClose} title="Close" style={{ background: 'transparent', border: 'none', color: 'var(--text3)', cursor: 'pointer', fontSize: 16, lineHeight: 1 }}>✕</button>
        </div>

        {/* badges + RED */}
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center', marginBottom: 12 }}>
          <span className={`badge ${err ? 'b-err' : status === 'ok' ? 'b-ok' : ''}`}>{err ? 'ERROR' : status.toUpperCase()}</span>
          <SpanKindChip kind={span.kind} />
          <span className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>{fmtNs(span.endTime - span.startTime)}</span>
        </div>

        {span.statusMessage && (
          <div style={{ fontSize: 11.5, color: 'var(--err)', background: 'color-mix(in srgb, var(--err) 8%, transparent)', border: '1px solid color-mix(in srgb, var(--err) 25%, transparent)', borderRadius: 4, padding: '6px 8px', marginBottom: 12 }}>
            {span.statusMessage}
          </div>
        )}

        {/* identity rows */}
        <KV k="Span ID" v={span.spanId} mono copy />
        {span.parentSpanId && <KV k="Parent" v={span.parentSpanId} mono copy />}
        <KV k="Trace ID" v={span.traceId} mono copy />
        <KV k="Started" v={tsLong(span.startTime)} />
        {span.scopeName && <KV k="Scope" v={span.scopeName} mono />}

        {/* exceptions */}
        {exceptions.length > 0 && (
          <Section title={`Exceptions (${exceptions.length})`}>
            {exceptions.map((ex, i) => (
              <div key={i} style={{ marginBottom: 10 }}>
                <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--err)' }}>{ex.type || 'exception'}</div>
                {ex.message && <div style={{ fontSize: 11.5, color: 'var(--text)', margin: '2px 0' }}>{ex.message}</div>}
                {ex.stacktrace && (
                  <pre style={{
                    fontSize: 10.5, lineHeight: 1.45, color: 'var(--text2)',
                    background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 4,
                    padding: 8, overflowX: 'auto', whiteSpace: 'pre', margin: '4px 0 0',
                    maxHeight: 260,
                  }}>{ex.stacktrace}</pre>
                )}
              </div>
            ))}
          </Section>
        )}

        {/* span links — pointers into OTHER traces */}
        {links.length > 0 && (
          <Section title={`Links (${links.length})`}>
            {links.map((l, i) => (
              <div key={`${l.traceId}-${i}`} style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4, fontSize: 11 }}>
                <Link to={`/trace?id=${l.traceId}${l.spanId ? `&span=${l.spanId}` : ''}`}
                  className="mono"
                  style={{ color: 'var(--accent2)', textDecoration: 'none', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                  title={`trace ${l.traceId}${l.spanId ? ` · span ${l.spanId}` : ''}`}>
                  {l.traceId.slice(0, 16)}…{l.spanId ? ` · ${l.spanId.slice(0, 8)}` : ''}
                </Link>
                {Object.keys(l.attributes).length > 0 && (
                  <span style={{ fontSize: 9.5, color: 'var(--text3)' }}>
                    {Object.entries(l.attributes).map(([k, v]) => `${k}=${v}`).join(' · ')}
                  </span>
                )}
              </div>
            ))}
          </Section>
        )}

        {/* span events (non-exception) */}
        {events.length > 0 && (
          <Section title={`Events (${events.length})`}>
            {events.map((e, i) => (
              <div key={i} style={{ marginBottom: 6 }}>
                <div style={{ fontSize: 11.5, fontWeight: 600 }}>
                  {e.name}
                  <span className="mono" style={{ fontSize: 10, color: 'var(--text3)', marginLeft: 8 }}>{tsLong(e.timeNano)}</span>
                </div>
                {Object.entries(e.attributes ?? {}).map(([k, v]) => (
                  <div key={k} style={{ fontSize: 10.5, color: 'var(--text2)', paddingLeft: 8 }}>
                    <span className="mono" style={{ color: 'var(--text3)' }}>{k}</span> {v}
                  </div>
                ))}
              </div>
            ))}
          </Section>
        )}

        {/* attributes grouped by semconv family */}
        {grouped.map(g => (
          <Section key={g.family} title={g.label}>
            {g.rows.map(([k, v]) => <KV key={k} k={k} v={v} mono />)}
          </Section>
        ))}
      </div>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginTop: 14 }}>
      <div style={{
        fontSize: 10, fontWeight: 700, letterSpacing: '0.5px', textTransform: 'uppercase',
        color: 'var(--text3)', marginBottom: 6, borderBottom: '1px solid var(--border)', paddingBottom: 4,
      }}>{title}</div>
      {children}
    </div>
  );
}

function KV({ k, v, mono, copy }: { k: string; v: string; mono?: boolean; copy?: boolean }) {
  return (
    <div style={{ display: 'flex', gap: 8, fontSize: 11.5, padding: '2px 0', alignItems: 'baseline' }}>
      <span style={{ color: 'var(--text3)', flex: 'none', minWidth: 96, fontFamily: mono ? 'ui-monospace, SFMono-Regular, Menlo, monospace' : undefined, fontSize: mono ? 10.5 : 11.5, wordBreak: 'break-all' }}>{k}</span>
      <span style={{ color: 'var(--text)', wordBreak: 'break-all', fontFamily: mono ? 'ui-monospace, SFMono-Regular, Menlo, monospace' : undefined, flex: 1 }}>
        {v}
        {copy && <CopyButton value={v} title={`Copy ${k}`} />}
      </span>
    </div>
  );
}
