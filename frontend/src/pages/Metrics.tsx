import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { ServicePicker } from '@/components/ServicePicker';
import { MetricNamePicker } from '@/components/MetricNamePicker';
import { FilterBuilder } from '@/components/FilterBuilder';
import { MultiLineChart } from '@/components/MultiLineChart';
import { EventMarkers } from '@/components/EventMarkers';
import { DrillButton } from '@/components/DrillButton';
import { ShareButton } from '@/components/ShareButton';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { decodeRange } from '@/lib/urlState';
import { classifyMetric, type MetricTemplate } from '@/lib/metricTemplates';
import type { Service, MetricInfo, SpanMetricSeries, FilterExpr, TimeRange } from '@/lib/types';

const AGG_OPTIONS = [
  { v: 'avg',  label: 'Average' },
  { v: 'sum',  label: 'Sum' },
  { v: 'last', label: 'Last' },
  { v: 'min',  label: 'Min' },
  { v: 'max',  label: 'Max' },
  { v: 'p50',  label: 'P50' },
  { v: 'p95',  label: 'P95' },
  { v: 'p99',  label: 'P99' },
];

// v0.5.259 — sub-10s steps for short-window deep-dives. 1s + 5s
// expose the OTel point-precision metrics actually carry — at
// ingest rate ~10/sec per metric we were artificially folding
// 10 samples into one bucket on a 10min view.
const STEP_OPTIONS = [
  { v: 0,    label: 'Auto' },
  { v: 1,    label: '1 s' },
  { v: 5,    label: '5 s' },
  { v: 10,   label: '10 s' },
  { v: 30,   label: '30 s' },
  { v: 60,   label: '1 min' },
  { v: 300,  label: '5 min' },
  { v: 1800, label: '30 min' },
];

const SUGGESTED_GROUPBY = [
  'host.name', 'service.name',
  'resource.host.name', 'resource.deployment.environment',
  'resource.service.instance.id', 'resource.k8s.pod.name', 'resource.k8s.node.name',
  'http.method', 'http.route', 'http.status_code',
  'db.system',
];

export default function MetricsPage() {
  // v0.6.10 — bug-fix: Services sparkline click and similar
  // drill-ins navigate here with `?service=X&metric=Y&range=Z`.
  // Pre-v0.6.10 these params were ignored and the page opened
  // empty. Now we seed the initial state from them so the
  // drill lands on a populated chart immediately.
  //
  // setSearchParams is intentionally NOT wired back from every
  // state change — keeping the URL writes scoped to deep-link
  // entry only avoids a thrash of replaceState() calls every
  // time the operator twiddles agg/step/groupBy. Use ShareButton
  // for the explicit "copy this state" affordance.
  const [searchParams] = useSearchParams();
  const [range, setRange] = useState<TimeRange>(() =>
    decodeRange(searchParams.get('range'), { preset: '30m' }));
  // v0.5.198 — `services` eager cache dropped. FilterBuilder
  // server-fetches service.name values via /api/attribute-values?q=
  // when the operator types in the value field; the ServicePicker
  // up top is already debounced server-side. The eager all-services
  // load at billion-span scale was the page's biggest TTFI cost.
  // Metadata for the currently-picked metric (unit, type,
  // description). Populated by MetricNamePicker's onPick
  // callback so we don't have to eager-fetch the entire metric
  // catalogue just to display "ms · gauge · request_duration".
  // v0.5.181 — previously this was derived from a full
  // metricNames[] list eager-loaded on mount, which at 10k+
  // metrics dominated the page's TTFI.
  const [currentMeta, setCurrentMeta] = useState<MetricInfo | null>(null);
  // v0.5.487 — template auto-applied to the current metric. null
  // means the operator typed the metric directly (no MetricInfo
  // arrived) or cleared the template chip.
  const [appliedTemplate, setAppliedTemplate] = useState<MetricTemplate | null>(null);
  const [service, setService] = useState(() => searchParams.get('service') || '');
  const [metric, setMetric] = useState(() => searchParams.get('metric') || '');
  const [agg, setAgg] = useState(() => searchParams.get('agg') || 'avg');
  const [step, setStep] = useState(0);
  const [filters, setFilters] = useState<FilterExpr[]>([]);
  const [groupBy, setGroupBy] = useState<string[]>([]);
  // v0.6.18 — SLO / alert threshold overlay. Operator types a
  // value; chart draws a dashed horizontal line + a faint band
  // above. Auto-populated when a known OTel-template metric is
  // picked (e.g. http.server.request.duration → 1000ms). null =
  // no overlay.
  const [threshold, setThreshold] = useState<number | null>(null);
  // v0.5.484 — SRE incident-debug toolkit:
  // compare = overlay the same series shifted back N hours.
  // logScale = uPlot distr:3 for orders-of-magnitude metrics.
  type CompareMode = 'off' | '1h' | '24h' | '7d' | 'prev';
  const [compare, setCompare] = useState<CompareMode>('off');
  const [logScale, setLogScale] = useState(false);
  const [compareSeries, setCompareSeries] = useState<SpanMetricSeries[] | null>(null);
  const [groupDraft, setGroupDraft] = useState('');
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);

  // Suggestion sources for filters and group-by
  const [hostValues, setHostValues] = useState<string[]>([]);
  const [instanceValues, setInstanceValues] = useState<string[]>([]);

  // Service swap → metric becomes stale. Clear the selection so
  // the operator picks fresh from the new service's catalog
  // rather than seeing data for the wrong service via a metric
  // that doesn't apply.
  useEffect(() => {
    setMetric('');
    setCurrentMeta(null);
    setAppliedTemplate(null);
    setThreshold(null);
  }, [service]); // eslint-disable-line react-hooks/exhaustive-deps

  // v0.5.487 — apply the OTel semconv template to a freshly-
  // picked metric. Only stomps defaults: agg jumps to the
  // template's choice unconditionally (the operator picked the
  // metric, they want it set up right), groupBy only fills when
  // empty so we don't clobber an in-flight slice operation.
  const onPickMetric = (info: MetricInfo) => {
    setCurrentMeta(info);
    const tpl = classifyMetric(info);
    setAppliedTemplate(tpl);
    if (tpl) {
      setAgg(tpl.agg);
      if (tpl.groupBy && tpl.groupBy.length > 0 && groupBy.length === 0) {
        setGroupBy(tpl.groupBy);
      }
      // v0.6.18 — auto-populate the threshold overlay from the
      // template's suggestion (e.g. HTTP server latency → 1000ms).
      // Operator can clear or override via the input below the
      // chart. Only sets when the operator hasn't already typed
      // their own value.
      if (tpl.threshold && threshold === null) {
        setThreshold(tpl.threshold.value);
      }
    }
  };

  // Pull dimension values for the current metric (host / instance) for combobox
  useEffect(() => {
    if (!metric) { setHostValues([]); setInstanceValues([]); return; }
    api.metricLabels(metric, 'host.name').then(v => setHostValues(v ?? [])).catch(() => {});
    api.metricLabels(metric, 'resource.service.instance.id').then(v => setInstanceValues(v ?? [])).catch(() => {});
  }, [metric]);

  // v0.5.481 — lifted out of useEffect so EventMarkers can use
  // the same resolved bounds without a second timeRangeToNs
  // call (which is a stable-references trap because of now()).
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  useEffect(() => {
    if (!metric) { setSeries(null); return; }
    setSeries(undefined);
    api.metricQuery({
      name: metric,
      service: service || undefined,
      agg,
      filters: filters.length ? JSON.stringify(filters) : undefined,
      groupBy: groupBy.join(',') || undefined,
      from, to, step: step || undefined,
    }).then(r => setSeries(r ?? [])).catch(() => setSeries(null));
  }, [metric, service, agg, filters, groupBy, step, from, to]);

  // v0.5.484 — compare-period fetch. Off → null. 1h/24h/7d use
  // fixed offsets; 'prev' uses the matched window (to - from)
  // so a 30min window compares to 30min before that.
  const compareOffsetNs = useMemo(() => {
    switch (compare) {
      case '1h':  return 60 * 60 * 1_000_000_000;
      case '24h': return 24 * 60 * 60 * 1_000_000_000;
      case '7d':  return 7 * 24 * 60 * 60 * 1_000_000_000;
      case 'prev': return to - from;
      default: return 0;
    }
  }, [compare, from, to]);

  useEffect(() => {
    if (!metric || compare === 'off' || compareOffsetNs <= 0) {
      setCompareSeries(null);
      return;
    }
    let cancelled = false;
    api.metricQuery({
      name: metric,
      service: service || undefined,
      agg,
      filters: filters.length ? JSON.stringify(filters) : undefined,
      groupBy: groupBy.join(',') || undefined,
      from: from - compareOffsetNs,
      to: to - compareOffsetNs,
      step: step || undefined,
    })
      .then(r => { if (!cancelled) setCompareSeries(r ?? []); })
      .catch(() => { if (!cancelled) setCompareSeries(null); });
    return () => { cancelled = true; };
  }, [metric, service, agg, filters, groupBy, step, from, to, compare, compareOffsetNs]);

  // Display meta only when the picker handed us metadata for
  // the currently-typed metric — typing an arbitrary name
  // before picking shows no meta, which is the right signal.
  const meta = currentMeta && currentMeta.name === metric ? currentMeta : null;
  const unit = meta?.unit ?? '';

  const addGroupKey = (k: string) => {
    const t = k.trim();
    if (!t || groupBy.includes(t)) return;
    setGroupBy([...groupBy, t]);
    setGroupDraft('');
  };
  const removeGroupKey = (k: string) => setGroupBy(groupBy.filter(x => x !== k));

  // Per-series stats for the legend table
  const summary = useMemo(() => (series ?? []).map(s => {
    const vs = s.points.map(p => p.value).filter(v => v != null && !isNaN(v));
    if (vs.length === 0) return { key: s.groupKey, last: 0, avg: 0, max: 0, min: 0, count: 0 };
    return {
      key: s.groupKey,
      last: vs[vs.length - 1],
      avg: vs.reduce((a, b) => a + b, 0) / vs.length,
      max: Math.max(...vs),
      min: Math.min(...vs),
      count: vs.length,
    };
  }), [series]);

  // v0.5.484 — window-wide aggregate for the stat tiles. Across
  // ALL series + ALL points in the visible window. delta is the
  // last value vs the compare-period equivalent (when compare
  // is on) — gives SREs "is this drifting?" at a glance.
  const stats = useMemo(() => {
    const all: number[] = [];
    const lasts: number[] = [];
    for (const s of series ?? []) {
      const vs = s.points.map(p => p.value).filter(v => v != null && !isNaN(v));
      all.push(...vs);
      if (vs.length > 0) lasts.push(vs[vs.length - 1]);
    }
    if (all.length === 0) return null;
    const sorted = all.slice().sort((a, b) => a - b);
    const pct = (p: number) => sorted[Math.min(sorted.length - 1, Math.floor(p * sorted.length))];
    const current = lasts.length > 0
      ? lasts.reduce((a, b) => a + b, 0) / lasts.length
      : sorted[sorted.length - 1];
    // Compare-window equivalent for delta calculation.
    let compareCurrent: number | null = null;
    if (compareSeries) {
      const cLast: number[] = [];
      for (const s of compareSeries) {
        const vs = s.points.map(p => p.value).filter(v => v != null && !isNaN(v));
        if (vs.length > 0) cLast.push(vs[vs.length - 1]);
      }
      if (cLast.length > 0) {
        compareCurrent = cLast.reduce((a, b) => a + b, 0) / cLast.length;
      }
    }
    return {
      current,
      min: sorted[0],
      max: sorted[sorted.length - 1],
      avg: sorted.reduce((a, b) => a + b, 0) / sorted.length,
      p50: pct(0.50),
      p99: pct(0.99),
      compareCurrent,
    };
  }, [series, compareSeries]);

  // Range encoded as a `custom:fromMs-toMs` string so the
  // DrillButton's TimeRange propagation lands on /traces with
  // the SAME window the operator is looking at. The chart's
  // resolved bounds, not the picker's preset, drive this.
  const customRange: TimeRange = useMemo(() => ({
    preset: 'custom',
    fromMs: Math.floor(from / 1_000_000),
    toMs:   Math.floor(to / 1_000_000),
  }), [from, to]);

  return (
    <>
      <Topbar title="Metrics" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ fontSize: 12, color: 'var(--text2)', flex: 1 }}>
            Pick a metric, slice by service / host / instance, or split by any attribute.
          </span>
          <ShareButton />
        </div>

        {/* Metric + service + agg + step */}
        <div className="controls">
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Service:</span>
          <ServicePicker value={service} onChange={setService}
            placeholder="(all)" width={170} />
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Metric:</span>
          <MetricNamePicker service={service} value={metric}
            onChange={setMetric}
            onPick={onPickMetric}
            placeholder="select metric…" width={280} />
          {appliedTemplate && appliedTemplate.id !== `OTel ${currentMeta?.type || 'metric'}` && (
            <span
              className="fb-chip"
              title={appliedTemplate.description + (appliedTemplate.threshold
                ? `\nThreshold: ${appliedTemplate.threshold.cmp} ${appliedTemplate.threshold.value} (${appliedTemplate.threshold.reason})`
                : '')}
              style={{ borderColor: 'var(--accent2)', color: 'var(--accent2)' }}
            >
              Template: <b>{appliedTemplate.id}</b>
              <button className="fb-chip-x" type="button"
                onClick={() => setAppliedTemplate(null)}
                aria-label="Clear template">✕</button>
            </span>
          )}
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Agg:</span>
          <select value={agg} onChange={e => setAgg(e.target.value)}>
            {AGG_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
          </select>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Step:</span>
          <select value={step} onChange={e => setStep(Number(e.target.value))}>
            {STEP_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
          </select>
          {meta && (
            <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
              {meta.type}{meta.unit && ` · ${meta.unit}`}{meta.description && ` · ${meta.description}`}
            </span>
          )}
        </div>

        {/* Attribute filters */}
        <FilterBuilder value={filters} onChange={setFilters}
          suggestedValues={{
            'host.name':      hostValues,
            'resource.host.name': hostValues,
            'resource.service.instance.id': instanceValues,
            'resource.deployment.environment': ['production', 'demo', 'staging'],
            'http.method':    ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
          }} />

        {/* Split by */}
        <div className="controls" style={{ marginBottom: 14 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Split by:</span>
          {groupBy.length === 0 && (
            <span style={{ color: 'var(--text3)', fontSize: 12, fontStyle: 'italic' }}>
              (single line — add attributes to break down)
            </span>
          )}
          {groupBy.map(k => (
            <span key={k} className="fb-chip">
              <b>{k}</b>
              <button className="fb-chip-x" type="button"
                onClick={() => removeGroupKey(k)} aria-label="Remove">✕</button>
            </span>
          ))}
          <Combobox value={groupDraft} onChange={setGroupDraft}
            options={SUGGESTED_GROUPBY.filter(k => !groupBy.includes(k))}
            placeholder="+ split key" width={200}
            onEnter={() => addGroupKey(groupDraft)} />
          {groupDraft && <button className="sec" onClick={() => addGroupKey(groupDraft)}>Add</button>}
        </div>

        {!metric && (
          <Empty icon="∿" title="Select a metric to begin">
            Use the Metric dropdown above. <code>OTEL_EXPORTER_OTLP_ENDPOINT</code> apps push here.
          </Empty>
        )}
        {metric && series === undefined && <Spinner />}
        {metric && series && series.length === 0 && (
          <Empty icon="∿" title="No data points for this query">
            Try a wider time range, fewer filters, or remove split keys.
          </Empty>
        )}
        {metric && series && series.length > 0 && (
          <>
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14,
            }}>
              {/* v0.5.484 — header strip: title + SRE toolbar
                  (compare, log scale, drill-to-traces). Stat
                  tiles below carry the at-a-glance numbers. */}
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
                <div style={{ fontSize: 11, color: 'var(--text2)', flex: 1, minWidth: 200 }}>
                  <b style={{ color: 'var(--accent2)' }}>{agg}</b> of{' '}
                  <b style={{ color: 'var(--accent2)' }}>{metric}</b>
                  {service && <> · service <b>{service}</b></>}
                  {groupBy.length > 0 && <> · split by <b>{groupBy.join(' / ')}</b></>}
                  {' · '}{series.length} series
                </div>
                <label style={{ fontSize: 11, color: 'var(--text2)', display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                  Compare:
                  <select value={compare} onChange={e => setCompare(e.target.value as CompareMode)}
                    style={{ fontSize: 11, padding: '2px 6px' }}
                    title="Overlay the same series shifted back by N hours so an anomaly stands out against its own baseline">
                    <option value="off">off</option>
                    <option value="1h">1h ago</option>
                    <option value="24h">24h ago</option>
                    <option value="7d">7d ago</option>
                    <option value="prev">prev window</option>
                  </select>
                </label>
                <label style={{ fontSize: 11, color: 'var(--text2)', display: 'inline-flex', alignItems: 'center', gap: 4, cursor: 'pointer' }}
                  title="Log10 y-axis — flip when the metric spans orders of magnitude">
                  <input type="checkbox" checked={logScale}
                    onChange={e => setLogScale(e.target.checked)} />
                  log y
                </label>
                <DrillButton to="/traces"
                  params={{ service: service || undefined }}
                  range={customRange}
                  title="View traces in this window"
                  label="⋮ Traces" />
              </div>

              {/* Stat tiles (v0.5.484) — SRE at-a-glance. */}
              {stats && (
                <div style={{
                  display: 'flex', gap: 14, marginBottom: 10, flexWrap: 'wrap',
                  padding: '6px 0', borderTop: '1px solid var(--border)',
                  borderBottom: '1px solid var(--border)',
                }}>
                  <StatTile label="current" value={fmtMetric(stats.current, unit)}
                    delta={stats.compareCurrent != null ? deltaPct(stats.current, stats.compareCurrent) : null}
                    compareLabel={compareLabelFor(compare)} />
                  <StatTile label="min"  value={fmtMetric(stats.min,  unit)} />
                  <StatTile label="max"  value={fmtMetric(stats.max,  unit)} />
                  <StatTile label="avg"  value={fmtMetric(stats.avg,  unit)} />
                  <StatTile label="p50"  value={fmtMetric(stats.p50,  unit)} />
                  <StatTile label="p99"  value={fmtMetric(stats.p99,  unit)} />
                </div>
              )}

              {/* v0.5.481 — operator event markers (deploy /
                  config / incident / maintenance) overlaid on
                  the metric chart. Service-scoped when the
                  operator narrowed by service; otherwise shows
                  all events in the window.
                  v0.6.18 — optional SLO/alert threshold line,
                  auto-populated from the OTel metric template
                  when one matches (e.g. http.server.request.
                  duration → 1000ms). Operator clears/edits via
                  the row beneath the chart. */}
              <div style={{ position: 'relative' }}>
                <MultiLineChart series={series} unit={unit}
                  compareSeries={compareSeries ?? undefined}
                  compareOffsetNs={compareOffsetNs > 0 ? compareOffsetNs : undefined}
                  compareLabel={compareLabelFor(compare) ?? undefined}
                  logScale={logScale}
                  thresholds={threshold !== null ? [{
                    value: threshold,
                    label: appliedTemplate?.threshold
                      ? `${appliedTemplate.id} threshold`
                      : 'threshold',
                    severity: 'warn',
                  }] : undefined} />
                <EventMarkers fromNs={from} toNs={to} service={service || undefined} />
              </div>

              {/* v0.6.18 — SLO/alert threshold control. Lives
                  beneath the chart so it doesn't clutter the
                  main controls bar above. The auto-populated
                  value (from the metric template) appears in
                  the input; the operator can override or clear.
                  Cleared state hides the line entirely. */}
              <div style={{
                display: 'flex', gap: 8, alignItems: 'center',
                marginTop: 8, fontSize: 12, color: 'var(--text2)',
              }}>
                <span>Threshold:</span>
                <input
                  type="number"
                  value={threshold ?? ''}
                  onChange={e => {
                    const v = e.target.value.trim();
                    setThreshold(v === '' ? null : parseFloat(v));
                  }}
                  placeholder="(none)"
                  style={{
                    width: 110, padding: '2px 8px',
                    fontFamily: 'ui-monospace, monospace',
                    fontSize: 12,
                  }}
                  title="Draw a horizontal SLO/alert line on the chart. Auto-populated from the OTel metric template when applicable."
                />
                {unit && <span style={{ color: 'var(--text3)' }}>{unit}</span>}
                {appliedTemplate?.threshold && (
                  <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                    · template suggests {appliedTemplate.threshold.cmp} {appliedTemplate.threshold.value}
                    ({appliedTemplate.threshold.reason})
                  </span>
                )}
                {threshold !== null && (
                  <button className="sec" type="button"
                    onClick={() => setThreshold(null)}
                    style={{ fontSize: 11, padding: '2px 8px' }}>
                    Clear
                  </button>
                )}
              </div>
            </div>

            {groupBy.length > 0 && summary.length > 1 && (
              <div className="table-wrap" style={{ marginTop: 14 }}>
                <table>
                  <thead>
                    <tr>
                      <th>Series</th>
                      <th style={{ textAlign: 'right' }}>Last</th>
                      <th style={{ textAlign: 'right' }}>Min</th>
                      <th style={{ textAlign: 'right' }}>Avg</th>
                      <th style={{ textAlign: 'right' }}>Max</th>
                      <th style={{ textAlign: 'right' }}>Buckets</th>
                    </tr>
                  </thead>
                  <tbody>
                    {summary.map((row, i) => (
                      <tr key={i}>
                        <td><b>{row.key.join(' / ') || '(all)'}</b></td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.last.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.min.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.avg.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.max.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(row.count)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
    </>
  );
}

// ─── v0.5.484 SRE toolkit helpers ───────────────────────────

function fmtMetric(v: number, unit: string): string {
  if (!isFinite(v)) return '—';
  // Compact human-readable formatting. For "ms" / "s" / "%"
  // keep one decimal; for counter-style metrics (no unit or
  // "1") fmtNum's k/M/B collapse reads better.
  if (unit === 'ms' || unit === 's' || unit === '%') {
    return v.toFixed(v >= 100 ? 0 : 1) + (unit ? ' ' + unit : '');
  }
  return fmtNum(v) + (unit && unit !== '1' ? ' ' + unit : '');
}

function deltaPct(current: number, prev: number): number {
  if (!isFinite(prev) || prev === 0) return 0;
  return ((current - prev) / Math.abs(prev)) * 100;
}

function compareLabelFor(c: 'off' | '1h' | '24h' | '7d' | 'prev'): string | null {
  switch (c) {
    case '1h':  return '1h ago';
    case '24h': return '24h ago';
    case '7d':  return '7d ago';
    case 'prev': return 'prev window';
    default: return null;
  }
}

function StatTile({ label, value, delta, compareLabel }: {
  label: string;
  value: string;
  delta?: number | null;
  compareLabel?: string | null;
}) {
  const hasDelta = delta != null && isFinite(delta);
  // ±5% threshold for visually neutral; beyond that the colour
  // signals whether the metric is climbing or falling. Operator
  // can read direction without parsing the number.
  const sign = hasDelta && Math.abs(delta!) >= 5
    ? (delta! > 0 ? 'up' : 'down')
    : 'flat';
  const deltaColour =
    sign === 'flat' ? 'var(--text3)'
    : sign === 'up'  ? 'rgb(220,38,38)'
    : 'rgb(46,160,67)';
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 80 }}>
      <span style={{
        fontSize: 10, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>{label}</span>
      <span style={{ fontSize: 14, color: 'var(--text)', fontWeight: 600, fontFamily: 'ui-monospace, monospace' }}>
        {value}
      </span>
      {hasDelta && compareLabel && (
        <span style={{ fontSize: 10, color: deltaColour, fontFamily: 'ui-monospace, monospace' }}
          title={`current vs ${compareLabel}`}>
          {sign === 'flat' ? '~' : sign === 'up' ? '↑' : '↓'} {Math.abs(delta!).toFixed(1)}% vs {compareLabel}
        </span>
      )}
    </div>
  );
}
