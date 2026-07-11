import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Badge, Drawer } from '@/components/ui';
import { Button } from '@/components/ui/Button';
import { ClusterChips } from '@/components/ClusterChips';
import { CopilotExplain } from '@/components/CopilotExplain';
import { RootCauseRibbon } from '@/components/RootCauseRibbon';
import { AIAnalysisPanel } from '@/components/AIAnalysisPanel';
import { LogsHistogram } from '@/components/LogsHistogram';
import { TimeChart } from '@/components/charts/TimeChart';
import { statusColor } from '@/lib/statusColor';
import { api } from '@/lib/api';
import { fmtNum, tsLong } from '@/lib/utils';
import { fmtDurationNs, fmtHistTick, fmtStartedTs } from './problemTime';
import type { AnomalyEvent, ExceptionGroup, ExceptionGroupState } from '@/lib/types';

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
            {event.kind === 'trace_op' && event.service && (
              <Link to={`/traces?service=${encodeURIComponent(event.service)}&hasError=true`}
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

// ── ExceptionTriageDrawer — Variant A (PatternFly dense table + triage
// drawer, spec §4A). Slides in from the right when an exception-inbox
// row is clicked on /problems (AnomaliesPage). Reuses the ui/Drawer
// shell (overlay + Esc + ✕, one design language) so the exception
// triage surface has the same anatomy as the anomaly drawer above.
//
// Sections, top→bottom (spec §4A):
//   1. Header strip: severity + state badges + fingerprint id (✕ from shell)
//   2. Title (exception type) + message
//   3. Three-fact grid: Started / Duration / Occurrences (--bg2 cells).
//      The spec's "Deviation (σ)" fact has no exception-group analogue
//      (exceptions carry no statistical baseline) → Occurrences, the
//      impact magnitude, stands in (task: "IMPACT = occurrences").
//   4. Occurrences histogram — the shared TimeChart (bar), dated ticks
//      past 20h via the tested problemTime.fmtHistTick.
//   5. "Root cause · AI" box — accent fill/border; the exception type +
//      message + the existing AIAnalysisPanel (the group's root-cause AI
//      affordance, click-to-run — no auto AI on open).
//   6. Affected services — colour dot + service link + occurrences metric.
//   7. Action row: Acknowledge (primary) · Resolve (secondary) · Ignore /
//      Reopen per state, right-aligned Logs ↗ / Traces ↗.
//
// ES/AI-cost contract: the ONLY fetch on open is one 30s-cached CH
// occurrences count (exceptions live in ClickHouse, not ES); the AI
// panel runs only on explicit click; nothing prefetches across the list.
// Deploy correlation is intentionally absent — ExceptionGroup carries no
// recentDeploy field, so (per spec) no deploy box renders at all.

const EXC_STATE_META: Record<ExceptionGroupState, { cls: string; label: string }> = {
  // 'new' shows OPEN, not NEW (v0.8.382): NEW is the yellow first-seen
  // marker on the list; the workflow state is "untriaged" → OPEN.
  new:          { cls: 'b-err',  label: 'OPEN' },
  regressed:    { cls: 'b-warn', label: 'REGRESSED' },
  acknowledged: { cls: 'b-info', label: 'ACK' },
  resolved:     { cls: 'b-ok',   label: 'RESOLVED' },
  ignored:      { cls: 'b-gray', label: 'IGNORED' },
};

function ExcFactCell({ k, v, title }: { k: string; v: React.ReactNode; title?: string }) {
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 'var(--radius-sm)', padding: '8px 10px', minWidth: 0,
    }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)', fontWeight: 600,
        textTransform: 'uppercase', letterSpacing: '.05em',
      }}>{k}</div>
      <div className="mono" style={{
        fontSize: 13, color: 'var(--text)', marginTop: 2,
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }} title={title}>{v}</div>
    </div>
  );
}

export function ExceptionTriageDrawer({ group, isAdmin, onChanged, onClose }: {
  group: ExceptionGroup;
  isAdmin: boolean;
  onChanged: () => void;
  onClose: () => void;
}) {
  // Optimistic local state chip — flip the badge immediately, roll back
  // on failure. Re-sync when a different group opens in the same mounted
  // drawer.
  const [state, setState] = useState<ExceptionGroupState>(group.state);
  useEffect(() => { setState(group.state); }, [group.fingerprint, group.state]);

  // Occurrences-over-time: the server-side gap-filled COUNT (v0.8.309,
  // NOT bucketed from sampled timestamps). One 30s-cached CH call, only
  // while the drawer is mounted (open) — exceptions live in ClickHouse,
  // so this is off the ES-cost path entirely.
  const occQ = useQuery({
    queryKey: ['exc-occ-detail', group.fingerprint],
    queryFn: () => api.exceptionGroupOccurrences(group.fingerprint),
    staleTime: 30_000,
  });
  const occ = occQ.data ?? [];
  // Dated ticks past a 20h window (spec §3) — memoised on the window so
  // an unrelated re-render can't tear down/rebuild the uPlot (v0.5.184).
  const occWindowSec = occ.length >= 2 ? (occ[occ.length - 1].time - occ[0].time) / 1e9 : 0;
  const fmtOccTick = useCallback((t: number) => fmtHistTick(t, occWindowSec), [occWindowSec]);
  const occTimes = useMemo(() => occ.map(p => p.time / 1e9), [occ]);
  const occSeries = useMemo(() => [{
    key: 'occ', label: 'occurrences', data: occ.map(p => p.count),
    color: statusColor('warn'), type: 'bar' as const,
  }], [occ]);

  const durationNs = Math.max(0, group.lastSeen - group.firstSeen);

  // Cross-signal deep links — house patterns (spec §3): 30m lead-in
  // before first seen, 10m tail after last seen.
  const logsFrom = Math.round((group.firstSeen - 30 * 60 * 1e9) / 1e6);
  const logsTo = Math.round((group.lastSeen + 10 * 60 * 1e9) / 1e6);
  const logsHref = `/logs?q=${encodeURIComponent(`service.name:"${group.service.replace(/"/g, '\\"')}"`)}&range=${encodeURIComponent(`custom:${logsFrom}-${logsTo}`)}`;
  const tracesHref = `/traces?service=${encodeURIComponent(group.service)}&hasError=true`;

  const act = async (next: ExceptionGroupState) => {
    setState(next);
    try {
      await api.setExceptionGroupState(group.fingerprint, next);
      onChanged();
    } catch (err) {
      alert(err instanceof Error ? err.message : String(err));
      setState(group.state);
    }
  };

  const meta = EXC_STATE_META[state];
  const isOpenState = state === 'new' || state === 'regressed';

  return (
    <Drawer onClose={onClose} width={520} header={
      <>
        {/* Exceptions carry no severity axis — every one is an error →
            a constant ERROR chip gives the PatternFly severity label the
            list reads by, while STATE carries the workflow. */}
        <span className="badge b-err" style={{ fontSize: 10 }}>ERROR</span>
        <span className={`badge ${meta.cls}`} style={{ fontSize: 10 }}>{meta.label}</span>
        <span className="badge b-gray mono" style={{ fontSize: 10 }}
          title={group.fingerprint}>{group.fingerprint.slice(0, 12)}</span>
      </>
    }>
      <div style={{ paddingTop: 10 }}>
        {/* 2. Title + message */}
        <div className="mono" style={{
          fontWeight: 700, fontSize: 14, color: 'var(--err)',
          wordBreak: 'break-all',
        }} title={group.type}>{group.type}</div>
        <div className="mono" style={{
          fontSize: 12, color: 'var(--text2)', margin: '4px 0 12px',
          overflowWrap: 'anywhere',
        }} title={group.message}>{group.message || '—'}</div>

        {/* 3. Fact grid */}
        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)',
          gap: 8, marginBottom: 12,
        }}>
          <ExcFactCell k="Started" v={fmtStartedTs(group.firstSeen)}
            title={tsLong(group.firstSeen)} />
          <ExcFactCell k="Duration" v={fmtDurationNs(durationNs)}
            title={`Last seen ${tsLong(group.lastSeen)}`} />
          <ExcFactCell k="Occurrences" v={fmtNum(group.occurrences)} />
        </div>

        {/* 4. Occurrences histogram */}
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>
            Occurrences over time · {fmtNum(group.occurrences)} total
          </div>
          {occ.length === 0 ? (
            <div style={{ color: 'var(--text3)', fontSize: 12 }}>
              {occQ.isLoading ? 'Loading…' : 'No occurrences to chart.'}
            </div>
          ) : (
            <TimeChart times={occTimes} series={occSeries} height={110} fmtX={fmtOccTick} />
          )}
        </div>

        {/* 5. Root cause · AI box (accent fill + border) */}
        <div style={{
          background: 'var(--accent-bg)', border: '1px solid var(--accent-border)',
          borderRadius: 'var(--radius-sm)', padding: 10, marginBottom: 12,
        }}>
          <div style={{
            fontSize: 10, fontWeight: 700, color: 'var(--accent2)',
            textTransform: 'uppercase', letterSpacing: '.05em', marginBottom: 6,
          }}>Root cause · AI</div>
          <AIAnalysisPanel service={group.service} />
        </div>

        {/* 6. Affected services */}
        <div style={{ marginBottom: 12 }}>
          <div style={{
            fontSize: 10, fontWeight: 700, color: 'var(--text3)',
            textTransform: 'uppercase', letterSpacing: '.05em', marginBottom: 6,
          }}>Affected services</div>
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8,
            padding: '6px 8px', borderRadius: 'var(--radius-sm)',
            background: 'var(--bg1)', border: '1px solid var(--border)',
          }}>
            <span style={{
              width: 8, height: 8, borderRadius: '50%', flexShrink: 0,
              background: isOpenState ? 'var(--err)' : 'var(--text3)',
            }} />
            <Link to={`/service?name=${encodeURIComponent(group.service)}`}
              className="mono" style={{ fontSize: 12 }}>{group.service}</Link>
            <span style={{ flex: 1 }} />
            <span className="mono" style={{ fontSize: 12, color: 'var(--err)', fontWeight: 600 }}>
              {fmtNum(group.occurrences)}
            </span>
          </div>
        </div>

        {/* 7. Action row — admin/editor only; viewers see the state chip
            (header) + read-only deep links, no mutating buttons. */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          {isAdmin && (state === 'new' || state === 'regressed') && (
            <>
              <Button variant="primary" size="sm" onClick={() => act('acknowledged')}>Acknowledge</Button>
              <Button variant="secondary" size="sm" onClick={() => act('resolved')}>Resolve</Button>
              <Button variant="secondary" size="sm" onClick={() => act('ignored')}>Ignore</Button>
            </>
          )}
          {isAdmin && state === 'acknowledged' && (
            <>
              <Button variant="primary" size="sm" onClick={() => act('resolved')}>Resolve</Button>
              <Button variant="secondary" size="sm" onClick={() => act('new')}>Reopen</Button>
              <Button variant="secondary" size="sm" onClick={() => act('ignored')}>Ignore</Button>
            </>
          )}
          {isAdmin && state === 'resolved' && (
            <Button variant="secondary" size="sm" onClick={() => act('new')}>Reopen</Button>
          )}
          {isAdmin && state === 'ignored' && (
            <Button variant="secondary" size="sm" onClick={() => act('new')}>Unignore</Button>
          )}
          <span style={{ flex: 1 }} />
          <Link to={logsHref} className="sec"
            style={{ fontSize: 12, padding: '4px 10px', textDecoration: 'none' }}
            title="Open /logs scoped to the service + occurrence window">
            ≡ Logs ↗
          </Link>
          <Link to={tracesHref} className="sec"
            style={{ fontSize: 12, padding: '4px 10px', textDecoration: 'none' }}
            title="Open error traces for this service">
            ⋮ Traces ↗
          </Link>
        </div>
      </div>
    </Drawer>
  );
}
