import { useMemo } from 'react';
import { IconButton } from '@/components/ui';
import { CopyButton } from '@/components/CopyButton';
import { SvcBadge } from '@/components/traces/shared';
import { LogTable } from '@/components/LogTable';
import { groupSpanAttrs, groupResourceAttrs } from '@/lib/spanAttrGroups';
import { spanHasError } from '@/lib/otel';
import { fmtNs, tsLong, displaySpanName } from '@/lib/utils';
import type { LogRow, SpanRow } from '@/lib/types';

// KioskSpanPanel — v0.10.681 (operatör: "kiosk'ta attribute'lar çıkmıyor;
// Tempo'da span'e tıklayınca alttan çıkıyordu, o hâle getirebilir miyiz").
//
// Tempo'nun alt span detayının ikizi, KROMSUZ ve SALT-OKUNUR: başlık
// (servis :: işlem, süre, trace başına göre başlangıç, kind, durum, span/
// parent id), iki sütun — Span attributes (semconv grupları, lib/
// spanAttrGroups) ve Resource attributes (Service/Deployment/Container/
// Kubernetes…), exception olayları, ve bu span'e ait log satırları
// (trace bundle'ından zaten yüklü loglar + span event'leri, span_id ile
// süzülür — EK İSTEK YOK). SpanDetail.tsx bilerek kullanılmadı: sayfa
// linkleri, AI Explain ve 5 ayrı fetch taşıyor; kiosk bunların hiçbirini
// çizmez. Esc ile kapanır (TraceKiosk useEscLayer).
export function KioskSpanPanel({ span, traceStartNs, logs, eventRows, onClose }: {
  span: SpanRow;
  traceStartNs: number;
  logs: LogRow[];
  eventRows: LogRow[];
  onClose: () => void;
}) {
  const attrGroups = useMemo(() => groupSpanAttrs(span.attributes), [span.attributes]);
  const resGroups = useMemo(() => groupResourceAttrs(span.resourceAttributes), [span.resourceAttributes]);
  const spanLogs = useMemo(
    () => [...eventRows, ...logs].filter(r => r.spanId === span.spanId).sort((a, b) => a.timestamp - b.timestamp),
    [logs, eventRows, span.spanId]);
  const durNs = Math.max(0, span.endTime - span.startTime);
  const offsetNs = Math.max(0, span.startTime - traceStartNs);
  const err = spanHasError(span);
  const exceptions = (span.events ?? []).filter(e => e.name === 'exception');
  const attrCount = attrGroups.reduce((n, g) => n + g.entries.length, 0);
  const resCount = resGroups.reduce((n, g) => n + g.entries.length, 0);

  return (
    <section className="kiosk-span" aria-label="Span detayı">
      <div className="kiosk-span__head">
        <SvcBadge name={span.serviceName} />
        <span className="kiosk-span__name" title={span.name}>{displaySpanName(span)}</span>
        <span className={`badge ${err ? 'b-err' : 'b-ok'}`}>{err ? 'ERROR' : (span.statusCode || 'unset')}</span>
        <span className="trace-kiosk__brand-spacer" />
        <IconButton aria-label="Span detayını kapat (Esc)" title="Kapat (Esc)"
          icon={<span aria-hidden="true">×</span>} onClick={onClose} />
      </div>
      {/* Tempo'nun span başlık satırı: Service · Duration · Start Time (trace
          başına göre +offset ve mutlak) · Kind · Status · Library. Trace state
          saklanmıyor (otlp-converter §1: kayıp), çizilmez. */}
      <div className="kiosk-span__facts">
        <span><b>Service:</b> {span.serviceName}</span>
        <span><b>Duration:</b> {fmtNs(durNs)}</span>
        <span><b>Start Time:</b> +{fmtNs(offsetNs)} ({tsLong(span.startTime)})</span>
        <span><b>Kind:</b> {span.kind || 'internal'}</span>
        <span><b>Status:</b> {span.statusCode || 'unset'}{span.statusMessage ? ` — ${span.statusMessage}` : ''}</span>
        {span.scopeName && <span><b>Library:</b> {span.scopeName}</span>}
        <span><b>Span ID:</b> <code className="trace-kiosk__id">{span.spanId}<CopyButton value={span.spanId} title="Copy span ID" /></code></span>
        {span.parentSpanId && <span><b>Parent:</b> <code className="trace-kiosk__id">{span.parentSpanId}</code></span>}
      </div>
      <div className="kiosk-span__cols">
        <div>
          <div className="kiosk-span__col-title">Span attributes <span className="kiosk-span__cnt">{attrCount}</span></div>
          {attrGroups.length === 0 && <div className="kiosk-span__empty">Attribute yok.</div>}
          {attrGroups.map(g => <AttrGroup key={g.key} label={g.label} entries={g.entries} />)}
        </div>
        <div>
          <div className="kiosk-span__col-title">Resource attributes <span className="kiosk-span__cnt">{resCount}</span></div>
          {resGroups.length === 0 && <div className="kiosk-span__empty">Resource attribute yok.</div>}
          {resGroups.map(g => <AttrGroup key={g.key} label={g.label} entries={g.entries} />)}
        </div>
      </div>
      {exceptions.length > 0 && (
        <div className="ps-sec">
          <div className="ps-sec-title">Exception · {exceptions.length}</div>
          {exceptions.map((e, i) => (
            <div key={i} className="kiosk-span__exc">
              <div><b>{e.attributes?.['exception.type'] || 'exception'}</b> {e.attributes?.['exception.message'] || ''}</div>
              {e.attributes?.['exception.stacktrace'] && (
                <pre className="kiosk-span__stack">{e.attributes['exception.stacktrace']}</pre>
              )}
            </div>
          ))}
        </div>
      )}
      <div className="ps-sec">
        <div className="ps-sec-title">Bu span'in logları · {spanLogs.length}</div>
        {spanLogs.length > 0
          ? <LogTable logs={spanLogs} hideTraceColumn />
          : <div className="kiosk-span__empty">Bu span'e bağlı log satırı yok (yüklü trace loglarında span_id eşleşmedi).</div>}
      </div>
    </section>
  );
}

// AttrGroup — Tempo'daki gibi katlanabilir grup (▾ Database 7): native
// <details open>, LogTable emsali; JS state yok.
function AttrGroup({ label, entries }: { label: string; entries: [string, string][] }) {
  return (
    <details open className="kiosk-span__group">
      <summary className="ps-sec-title">{label} <span className="kiosk-span__cnt">{entries.length}</span></summary>
      <table className="ps-kv"><tbody>
        {entries.map(([k, v]) => <tr key={k}><td>{k}</td><td>{v}</td></tr>)}
      </tbody></table>
    </details>
  );
}
