import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Modal } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { IconLink } from '@/components/icons';
import { MultiLineChart } from '@/components/MultiLineChart';
import { ServiceTimeline } from '@/components/traces/ServiceTimeline';
import { TraceLogList, severityBuckets } from '@/components/traces/TraceLogList';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type {
  PivotAnchor,
  CorrelationContext,
  SpanMetricSeries,
} from '@/lib/types';

// CorrelationContextDrawer (task #6) — ONE pivot surface. Opened from any single
// signal (trace ◆ on Explore, log 👁 on Logs, "Correlate" on Trace), it shows
// the correlated OTHER two lenses — trace ↔ logs ↔ metrics — joined on
// trace_id → service.name → time-window, WITHOUT a page change. This is the
// synthesis move RootCausePanel can't do (that panel is Problem-bound + one-shot):
// the drawer lets the operator RE-ANCHOR (click an exemplar / a trace_id) and
// pivot again, never leaving the drawer.
//
// Built shareable-URL-first conceptually (the anchor is fully serialisable) so a
// future standalone /correlate route is a thin wrapper. v1 is drawer-only.
//
// HONESTY (spec Risk §1): the join-key CHIP in the anchor header always tells
// the operator whether the join is exact (`trace_id`) or fuzzy (`service+window`).
// The METRIC anchor is deferred — a raw OTLP metric point has no trace_id, so the
// three entry points wired in v1 are TRACE + LOG (real / near-real trace_id
// joins). The drawer still RENDERS a metric anchor if handed one, but labels its
// derived exemplar fuzzy.
export function CorrelationContextDrawer({
  anchor,
  onClose,
}: {
  anchor: PivotAnchor | null;
  onClose: () => void;
}) {
  // Internal anchor state so re-anchor (click an exemplar / trace_id) swaps the
  // bundle without unmounting the drawer. `prev` holds the single previous
  // anchor for the 1-level "← back" affordance (spec open-question #4: cap depth
  // at 1 rather than a full breadcrumb stack).
  const [active, setActive] = useState<PivotAnchor | null>(anchor);
  const [prev, setPrev] = useState<PivotAnchor | null>(null);

  // Sync when the parent opens the drawer with a new anchor (or closes it).
  useEffect(() => {
    setActive(anchor);
    setPrev(null);
  }, [anchor]);

  const [ctx, setCtx] = useState<CorrelationContext | null | undefined>(undefined);

  useEffect(() => {
    if (!active) {
      setCtx(undefined);
      return;
    }
    let cancelled = false;
    setCtx(undefined);
    api
      .correlateContext(active)
      .then((r) => {
        if (!cancelled) setCtx(r ?? null);
      })
      .catch(() => {
        if (!cancelled) setCtx(null);
      });
    return () => {
      cancelled = true;
    };
  }, [active]);

  // Re-anchor onto a new signal, remembering the current one for "← back".
  const reAnchor = (next: PivotAnchor) => {
    setPrev(active);
    setActive(next);
  };
  const goBack = () => {
    if (prev) {
      setActive(prev);
      setPrev(null);
    }
  };

  if (!active) return <Modal open={false} onClose={onClose} />;

  const a = ctx?.anchor;
  const allEmpty =
    ctx != null &&
    !ctx.trace &&
    (ctx.logs?.length ?? 0) === 0 &&
    (ctx.metrics?.length ?? 0) === 0 &&
    !ctx.exemplar;

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={
        <span style={{ fontSize: 13, display: 'inline-flex', alignItems: 'center', gap: 8 }}>
          <IconLink size={14} />
          Correlated signals
          {prev && (
            <button
              type="button"
              onClick={goBack}
              title="Back to the previous anchor"
              style={{
                fontSize: 11,
                padding: '1px 8px',
                borderRadius: 4,
                border: '1px solid var(--border)',
                background: 'var(--bg2)',
                color: 'var(--text2)',
                cursor: 'pointer',
              }}>
              ← back
            </button>
          )}
        </span>
      }>
      {ctx === undefined && <Spinner />}
      {ctx === null && (
        <div style={{ fontSize: 12, color: 'var(--err)' }}>Failed to load correlated signals.</div>
      )}
      {ctx && a && (
        <>
          <AnchorHeader ctx={ctx} />
          {allEmpty ? (
            <Empty icon={<IconLink size={28} />} title="No correlated signals">
              Nothing else landed in this window for the anchor's {a.joinKey === 'trace_id' ? 'trace_id' : 'service'}.
            </Empty>
          ) : (
            <>
              <TraceLens ctx={ctx} onReAnchor={reAnchor} />
              <LogsLens ctx={ctx} onReAnchor={reAnchor} />
              <MetricsLens ctx={ctx} onReAnchor={reAnchor} />
            </>
          )}
        </>
      )}
    </Modal>
  );
}

// ── Anchor header ───────────────────────────────────────────────────────────
// What the operator pivoted FROM + the resolved join-key chip. The chip is the
// honesty surface: trace_id = exact join (green), service+window = fuzzy (amber).
function AnchorHeader({ ctx }: { ctx: CorrelationContext }) {
  const a = ctx.anchor;
  const kindLabel =
    a.kind === 'trace' ? 'Trace' : a.kind === 'log' ? 'Log line' : 'Metric';
  const exact = a.joinKey === 'trace_id';
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        flexWrap: 'wrap',
        padding: '8px 10px',
        marginBottom: 12,
        border: '1px solid var(--border)',
        borderRadius: 6,
        background: 'var(--bg1)',
        fontSize: 12,
      }}>
      <span style={{ fontWeight: 600, color: 'var(--text)' }}>{kindLabel}</span>
      {a.traceId && (
        <span className="mono" style={{ color: 'var(--text3)' }} title={a.traceId}>
          {a.traceId.slice(0, 12)}…
        </span>
      )}
      {a.service && (
        <span className="mono" style={{ color: 'var(--text2)' }} title={a.service}>
          {a.service}
        </span>
      )}
      <span
        className="badge"
        title={
          exact
            ? 'Exact cross-signal join on trace_id — no time fuzz.'
            : 'Fuzzy join: no trace_id on this signal, so the other lenses are matched by service + time-window.'
        }
        style={{
          marginLeft: 'auto',
          fontSize: 10.5,
          padding: '2px 8px',
          borderRadius: 4,
          fontWeight: 600,
          background: exact
            ? 'color-mix(in srgb, var(--ok) 16%, transparent)'
            : 'color-mix(in srgb, var(--warn) 16%, transparent)',
          color: exact ? 'var(--ok)' : 'var(--warn)',
        }}>
        join: {a.joinKey}
      </span>
    </div>
  );
}

// ── Lens section shell ──────────────────────────────────────────────────────
function Lens({
  title,
  right,
  children,
}: {
  title: string;
  right?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: 6, marginBottom: 12, background: 'var(--bg1)' }}>
      <div
        style={{
          padding: '6px 10px',
          borderBottom: '1px solid var(--border)',
          fontSize: 10,
          color: 'var(--text3)',
          textTransform: 'uppercase',
          letterSpacing: 0.4,
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
        }}>
        <span>{title}</span>
        <span style={{ textTransform: 'none', letterSpacing: 0 }}>{right}</span>
      </div>
      <div style={{ padding: 8 }}>{children}</div>
    </div>
  );
}

// ── Trace lens ──────────────────────────────────────────────────────────────
function TraceLens({ ctx }: { ctx: CorrelationContext; onReAnchor: (a: PivotAnchor) => void }) {
  const t = ctx.trace;
  return (
    <Lens
      title="Trace"
      right={
        t ? (
          <Link to={`/trace?id=${t.traceId}`} style={{ fontSize: 11, color: 'var(--accent2)' }}>
            Open full trace →
          </Link>
        ) : undefined
      }>
      {!t ? (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          {ctx.anchor.joinKey === 'trace_id'
            ? 'Trace not resident (may have been sampled or aged out).'
            : 'No trace_id on this anchor — pivot a log/exemplar with a trace_id to see the trace lens.'}
        </div>
      ) : (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 10, marginBottom: 10 }}>
            <KPI label="Root op" value={t.rootName} sub={t.service} small />
            <KPI label="Duration" value={`${t.durationMs.toFixed(0)} ms`} />
            <KPI label="Spans" value={fmtNum(t.spanCount)} sub={`${t.services.length} services`} />
            <KPI label="Errors" value={fmtNum(t.errSpans)} cls={t.errSpans > 0 ? 'err' : ''} />
          </div>
          {t.spans && t.spans.length > 0 && (
            <div style={{ border: '1px solid var(--border)', borderRadius: 6, padding: 8, background: 'var(--bg2)' }}>
              <div
                style={{
                  fontSize: 10,
                  color: 'var(--text3)',
                  textTransform: 'uppercase',
                  letterSpacing: 0.4,
                  marginBottom: 6,
                }}>
                Service timeline
              </div>
              <ServiceTimeline spans={t.spans} />
            </div>
          )}
        </>
      )}
    </Lens>
  );
}

// ── Logs lens ───────────────────────────────────────────────────────────────
function LogsLens({ ctx, onReAnchor }: { ctx: CorrelationContext; onReAnchor: (a: PivotAnchor) => void }) {
  const logs = ctx.logs ?? [];
  const buckets = useMemo(() => severityBuckets(logs), [logs]);
  const traceId = ctx.anchor.traceId;
  const offsetFromNs = ctx.trace?.startTimeNs;
  // Distinct trace_ids in the logs the operator can RE-ANCHOR onto (the pivot
  // mesh). Only surfaced when the current anchor is NOT already that trace.
  const pivotTraceIds = useMemo(() => {
    const set = new Set<string>();
    for (const l of logs) {
      if (l.traceId && l.traceId !== traceId) set.add(l.traceId);
    }
    return Array.from(set).slice(0, 4);
  }, [logs, traceId]);

  return (
    <Lens
      title="Logs"
      right={
        <span style={{ display: 'inline-flex', gap: 8, alignItems: 'center' }}>
          {buckets.err > 0 && <span style={{ color: 'var(--err)' }}>{buckets.err} err</span>}
          {buckets.warn > 0 && <span style={{ color: 'var(--warn)' }}>{buckets.warn} warn</span>}
          <span style={{ color: 'var(--text3)' }}>
            {fmtNum(logs.length)}
            {logs.length >= 500 ? '+' : ''}
          </span>
          {traceId ? (
            <Link to={`/logs?traceId=${traceId}`} style={{ color: 'var(--accent2)' }}>
              Widen to /logs →
            </Link>
          ) : ctx.anchor.service ? (
            <Link to={`/logs?service=${encodeURIComponent(ctx.anchor.service)}`} style={{ color: 'var(--accent2)' }}>
              Widen to /logs →
            </Link>
          ) : null}
        </span>
      }>
      {logs.length === 0 ? (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>No correlated logs in window.</div>
      ) : (
        <TraceLogList logs={logs} offsetFromNs={offsetFromNs} maxHeight={260} />
      )}
      {pivotTraceIds.length > 0 && (
        <div style={{ marginTop: 8, display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
          <span style={{ fontSize: 10, color: 'var(--text3)' }}>Pivot to trace:</span>
          {pivotTraceIds.map((tid) => (
            <button
              key={tid}
              type="button"
              onClick={() => onReAnchor({ kind: 'trace', traceId: tid })}
              className="mono"
              title={`Re-anchor the drawer on trace ${tid}`}
              style={{
                fontSize: 10.5,
                padding: '2px 7px',
                borderRadius: 4,
                border: '1px solid var(--border)',
                background: 'var(--bg2)',
                color: 'var(--accent2)',
                cursor: 'pointer',
              }}>
              {tid.slice(0, 10)}…
            </button>
          ))}
        </div>
      )}
    </Lens>
  );
}

// ── Metrics lens ────────────────────────────────────────────────────────────
// The anchor service's RED series (rate / error_rate / p99) over [from,to]. One
// small uPlot per metric (they carry different units, so a shared axis would
// mislead). A click on any chart resolves the bucket window and re-anchors on a
// representative trace for it (the metric→trace pivot, fuzzy by service+window).
function MetricsLens({ ctx, onReAnchor }: { ctx: CorrelationContext; onReAnchor: (a: PivotAnchor) => void }) {
  const series = ctx.metrics ?? [];
  const service = ctx.anchor.service;
  const byLabel = useMemo(() => {
    const m = new Map<string, SpanMetricSeries>();
    for (const s of series) m.set(s.groupKey[0] ?? '', s);
    return m;
  }, [series]);

  const exploreHref =
    service != null
      ? `/explore?service=${encodeURIComponent(service)}&from=${ctx.anchor.fromNs}&to=${ctx.anchor.toNs}`
      : undefined;

  const charts: { label: string; unit: string }[] = [
    { label: 'rate', unit: 'rps' },
    { label: 'error_rate', unit: '%' },
    { label: 'p99', unit: 'ms' },
  ];

  return (
    <Lens
      title={`Metrics${service ? ` · ${service}` : ''}`}
      right={
        exploreHref ? (
          <Link to={exploreHref} style={{ fontSize: 11, color: 'var(--accent2)' }}>
            Open in Explore →
          </Link>
        ) : undefined
      }>
      {series.length === 0 ? (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          {service ? 'No RED series in window.' : 'No service resolved for the metrics lens.'}
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {charts.map(({ label, unit }) => {
            const s = byLabel.get(label);
            if (!s) return null;
            return (
              <div key={label}>
                <div style={{ fontSize: 10, color: 'var(--text3)', marginBottom: 2 }}>{label}</div>
                <MultiLineChart
                  series={[s]}
                  unit={unit}
                  height={90}
                  onBucketClick={
                    service
                      ? (fromNs, toNs) =>
                          onReAnchor({
                            kind: 'metric',
                            service,
                            tsNs: Math.round((fromNs + toNs) / 2),
                            metricKind: label === 'error_rate' ? 'error' : label === 'p99' ? 'latency' : 'throughput',
                            fromNs,
                            toNs,
                          })
                      : undefined
                  }
                />
              </div>
            );
          })}
        </div>
      )}
      {ctx.exemplar && (
        <div
          style={{
            marginTop: 10,
            padding: '6px 10px',
            border: '1px solid var(--border)',
            borderRadius: 6,
            background: 'var(--bg2)',
            display: 'flex',
            gap: 10,
            alignItems: 'center',
            flexWrap: 'wrap',
            fontSize: 11,
          }}>
          <span
            title="No true OTLP exemplar on the wire — this representative trace is matched by service + window (fuzzy)."
            style={{
              fontSize: 10,
              fontWeight: 600,
              padding: '1px 6px',
              borderRadius: 4,
              background: 'color-mix(in srgb, var(--warn) 16%, transparent)',
              color: 'var(--warn)',
            }}>
            fuzzy exemplar
          </span>
          <span className="mono" style={{ color: 'var(--text2)' }} title={ctx.exemplar.name}>
            {ctx.exemplar.name}
          </span>
          <span style={{ color: 'var(--text3)' }}>{(ctx.exemplar.durationNs / 1e6).toFixed(0)}ms</span>
          <button
            type="button"
            onClick={() => onReAnchor({ kind: 'trace', traceId: ctx.exemplar!.traceId })}
            style={{
              marginLeft: 'auto',
              fontSize: 11,
              padding: '2px 8px',
              borderRadius: 4,
              border: '1px solid var(--border)',
              background: 'var(--bg1)',
              color: 'var(--accent2)',
              cursor: 'pointer',
            }}>
            Pivot into this trace ◆
          </button>
        </div>
      )}
    </Lens>
  );
}

// ── KPI tile (same shape as TracePeekDrawer's PeekKPI) ──────────────────────
function KPI({
  label,
  value,
  sub,
  cls,
  small,
}: {
  label: string;
  value: string;
  sub?: string;
  cls?: string;
  small?: boolean;
}) {
  return (
    <div style={{ padding: '6px 10px', border: '1px solid var(--border)', borderRadius: 4, background: 'var(--bg2)' }}>
      <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4 }}>{label}</div>
      <div
        style={{
          fontSize: small ? 12 : 16,
          fontWeight: 600,
          marginTop: 2,
          color: cls === 'err' ? 'var(--err)' : 'var(--text)',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}
        title={value}>
        {value}
      </div>
      {sub && (
        <div
          style={{ fontSize: 10, color: 'var(--text3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
          title={sub}>
          {sub}
        </div>
      )}
    </div>
  );
}
