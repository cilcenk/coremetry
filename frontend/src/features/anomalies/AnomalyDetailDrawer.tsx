import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Badge, Drawer } from '@/components/ui';
import { ClusterChips } from '@/components/ClusterChips';
import { CopilotExplain } from '@/components/CopilotExplain';
import { RootCauseRibbon } from '@/components/RootCauseRibbon';
import { LogsHistogram } from '@/components/LogsHistogram';
import { fmtNum, tsLong } from '@/lib/utils';
import type { AnomalyEvent } from '@/lib/types';

// AnomalyDetailDrawer — v0.8.267, operator-requested: "Anomalies
// sayfasında üzerine tıklayınca ne zaman spike oldu ve benzeri
// detay görmek iyi olurdu, problems gibi." Right-side slide-in
// mirroring the Problems TriageDrawer shell: spike timeline facts
// (started / last seen / duration / peak ×), the service's log
// volume around the spike, deploy chip, root-cause ribbon, AI
// explain, and the cross-signal deep links.
//
// ES-cost contract (operator: "log anomalies elastic backend
// kullanıldığında çok fazla sorgu yapmasın"): the ONLY backend
// fetch this drawer triggers is ONE bounded /api/logs/timeseries
// call, and only (a) when the drawer is actually open and (b) for
// log-shaped kinds. It rides the endpoint's existing 30s server
// cache; trace_op anomalies fetch nothing at all. Rows in the
// table never prefetch.

function fmtDuration(ns: number): string {
  const s = Math.max(0, Math.round(ns / 1e9));
  if (s < 90) return `${s}s`;
  if (s < 90 * 60) return `${Math.round(s / 60)}m`;
  if (s < 36 * 3600) return `${(s / 3600).toFixed(1)}h`;
  return `${(s / 86400).toFixed(1)}d`;
}

function Fact({ k, v, title }: { k: string; v: React.ReactNode; title?: string }) {
  return (
    <div style={{ minWidth: 0 }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)', fontWeight: 600,
        textTransform: 'uppercase', letterSpacing: '.05em',
      }}>{k}</div>
      <div className="mono" style={{
        fontSize: 12, color: 'var(--text)', marginTop: 2,
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }} title={title}>{v}</div>
    </div>
  );
}

const KIND_LABEL: Record<AnomalyEvent['kind'], string> = {
  log_pattern: 'LOG PATTERN',
  trace_op: 'TRACE OP',
  elastic_ml: 'ELASTIC ML',
  log_template_new: 'NEW LOG SHAPE',
};

export function AnomalyDetailDrawer({ event, onClose }: {
  event: AnomalyEvent;
  onClose: () => void;
}) {
  const isLogKind = event.kind === 'log_pattern' || event.kind === 'log_template_new'
    || event.kind === 'elastic_ml';
  const durationNs = Math.max(0, event.lastSeen - event.startedAt);

  // Chart window: 3× the spike duration of lead-in (min 30 min) so
  // the baseline is visible left of the spike, plus a 10-minute
  // tail. Memoised — a fresh object each render would refire the
  // histogram fetch (v0.5.184 class).
  const chartRange = useMemo(() => {
    const lead = Math.max(3 * durationNs, 30 * 60 * 1e9);
    return {
      from: event.startedAt - lead,
      to: event.lastSeen + 10 * 60 * 1e9,
    };
  }, [event.startedAt, event.lastSeen, durationNs]);
  const chartFilter = useMemo(() => ({
    service: event.service, search: '', severity: 0, traceId: '', spanId: '',
  }), [event.service]);

  // /logs deep link scoped to the service + the spike window (range
  // rides the URL per useUrlRange's custom encoding).
  const logsHref = useMemo(() => {
    const p = new URLSearchParams();
    if (event.service) p.set('q', `service.name:"${event.service.replace(/"/g, '\\"')}"`);
    p.set('range', `custom:${Math.round(chartRange.from / 1e6)}-${Math.round(chartRange.to / 1e6)}`);
    return `/logs?${p.toString()}`;
  }, [event.service, chartRange]);

  // v0.8.499 (sadeleştirme #2, 5/5) — kabuk ui/Drawer'a taşındı:
  // overlay/Esc/✕ tek evden; başlık ve gövde (ES-cost sözleşmesi
  // dahil — histogram yalnız açıkken, tek 30s-cache'li çağrı) birebir.
  return (
    <Drawer onClose={onClose} width={560} header={
      <>
        <Badge tone={event.status === 'active' ? 'danger' : 'success'} style={{ fontSize: 10 }}>
          {event.status === 'active' ? 'ACTIVE' : 'CLEARED'}
        </Badge>
        <span className="badge b-gray" style={{ fontSize: 10 }}>{KIND_LABEL[event.kind]}</span>
        {event.service && (
          <Link to={`/service?name=${encodeURIComponent(event.service)}`}
            style={{ fontWeight: 700, fontSize: 14 }}>
            {event.service}
          </Link>
        )}
        <ClusterChips clusters={event.clusters} />
      </>
    }>
        <div style={{ paddingTop: 10 }}>
          <div style={{
            fontWeight: 700, fontSize: 14, marginBottom: 10,
            overflowWrap: 'anywhere',
          }} title={event.pattern}>{event.pattern}</div>

          {/* Spike timeline — the "ne zaman spike oldu" answer. */}
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))',
            gap: 12, padding: 12, marginBottom: 12,
            background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 8,
          }}>
            <Fact k="Spike started" v={tsLong(event.startedAt)} />
            <Fact k="Last seen" v={tsLong(event.lastSeen)} />
            <Fact k="Duration" v={event.status === 'active'
              ? `${fmtDuration(durationNs)} · ongoing`
              : fmtDuration(durationNs)} />
            <Fact k="Peak ratio" v={`×${event.peakRatio.toFixed(1)}`}
              title="Peak count vs the pre-spike baseline window" />
            {event.currentRatio > 0 && (
              <Fact k="Current ratio" v={`×${event.currentRatio.toFixed(1)}`} />
            )}
            {event.currentCount > 0 && (
              <Fact k="Count in window" v={fmtNum(event.currentCount)} />
            )}
          </div>

          {event.recentDeploy && (
            <div style={{
              fontSize: 12, padding: '8px 12px', marginBottom: 12,
              borderRadius: 6,
              background: 'color-mix(in srgb, var(--warn) 10%, transparent)',
              border: '1px solid color-mix(in srgb, var(--warn) 35%, transparent)',
            }}>
              ⬇ Deploy <b className="mono">{event.recentDeploy.version}</b> landed{' '}
              <b>{Math.max(1, Math.round(event.recentDeploy.ageSeconds / 60))}m before</b> the spike
              ({tsLong(event.recentDeploy.timeUnixNs)}) — likely-cause window ≤ 5m.
            </div>
          )}

          {event.sample && (
            <pre style={{
              fontSize: 11, fontFamily: 'ui-monospace, SFMono-Regular, monospace',
              whiteSpace: 'pre-wrap', overflowWrap: 'anywhere',
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 6, padding: '8px 10px', marginBottom: 12,
              color: 'var(--text2)', maxHeight: 120, overflowY: 'auto',
            }} title="Sample line captured at detection">{event.sample}</pre>
          )}

          {/* Service log volume around the spike — mounted only while
              the drawer is open, one 30s-cached timeseries call, log
              kinds only (ES-cost contract in the header comment). */}
          {isLogKind && event.service && (
            <div style={{ marginBottom: 4 }}>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>
                {event.service} log volume around the spike
                (window {tsLong(chartRange.from)} → {tsLong(chartRange.to)})
              </div>
              <LogsHistogram range={chartRange} filter={chartFilter} />
            </div>
          )}

          {/* Root cause + AI — same affordances the row had, in situ. */}
          <div style={{ marginBottom: 12 }}>
            <RootCauseRibbon anchor="anomaly" id={event.id} summary={event.rootCause} />
          </div>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <CopilotExplain kind="anomaly" id={event.id} label="✨ Explain this anomaly" />
            {isLogKind && event.service && (
              <Link to={logsHref} className="sec"
                style={{ fontSize: 12, padding: '4px 10px', textDecoration: 'none' }}
                title="Open /logs scoped to the service + spike window">
                ≡ Logs in spike window ↗
              </Link>
            )}
            {/* v0.8.585 — Operator-reported: rootOnly default'u TRUE
                olduğundan hata izleri (çoğu non-root span) boş
                listeleniyordu; hasError linki root filtresini kapatır. */}
            {event.kind === 'trace_op' && event.service && (
              <Link to={`/traces?service=${encodeURIComponent(event.service)}&hasError=true&rootOnly=false`}
                className="sec"
                style={{ fontSize: 12, padding: '4px 10px', textDecoration: 'none' }}
                title="Open error traces for this service">
                ⋮ Error traces ↗
              </Link>
            )}
          </div>
        </div>
    </Drawer>
  );
}
