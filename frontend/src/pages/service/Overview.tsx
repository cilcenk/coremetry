import { useId, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { Service, Problem, TimeRange, SpanMetricSeries, OperationSummary } from '@/lib/types';
import { timeRangeToNs } from '@/lib/utils';
import { api } from '@/lib/api';
import { useServiceDeploys } from '@/lib/queries';
import { OverviewChart, type OvChartSeries } from './charts/OverviewChart';
import { Neighbors } from './Neighbors';
import { OpsCard, DbCard } from './OverviewTables';
import { ServiceInstancesCard } from './ServiceInstancesCard';
import { MetricPanel } from '@/components/MetricPanel';
import { metricQuery, type MetricQuery } from '@/lib/metricQuery';

// Service Overview (v0.7.92+) — Dynatrace-style at-a-glance APM view, ported
// from the design handoff. The new tab on /service?name=<svc> (becomes the
// default once complete). Reuses the service-bundle data Service.tsx already
// fetched (info + problems); the RED series for the KPI sparklines + charts
// come from one batched span-metric call here.
//
// Done: KPI row (+ full-bleed sparklines), RED charts row, recent problems.
// Next: service-flow map, compact ops + top-DB tables, instances, sub-tabs.

interface Props {
  service: string;
  range: TimeRange;
  info: Service | null;
  problems: Problem[];
  operations: OperationSummary[];
}

function vals(s?: SpanMetricSeries[] | null): number[] {
  return s && s[0] ? s[0].points.map(p => p.value) : [];
}

// Trend delta vs the prior window — mean of the first third vs the last
// third of the series (mirrors the design's data.js delta()/prior()). >0.5%
// = up, <-0.5% = down, else flat. Returns null when the series is too short.
type Delta = { pct: string; dir: 'up' | 'down' | 'flat' };
function computeDelta(arr: number[]): Delta | null {
  if (arr.length < 6) return null;
  const third = Math.max(1, Math.floor(arr.length / 3));
  const mean = (xs: number[]) => xs.reduce((a, b) => a + b, 0) / (xs.length || 1);
  const prev = mean(arr.slice(0, third));
  const cur = mean(arr.slice(-third));
  if (prev === 0) return null;
  const d = ((cur - prev) / prev) * 100;
  return { pct: Math.abs(d).toFixed(1), dir: d > 0.5 ? 'up' : d < -0.5 ? 'down' : 'flat' };
}

// Full-bleed gradient sparkline pinned to the bottom of a KPI tile. Inline
// SVG (the existing Sparkline pattern), stretched to the tile width via
// preserveAspectRatio="none"; gradient fill 28%→0% of the series colour.
function OvSparkline({ data, color }: { data: number[]; color: string }) {
  const gid = useId();
  if (data.length < 2) return null;
  const W = 120, H = 34, pad = 2;
  const mn = Math.min(...data), mx = Math.max(...data), rng = mx - mn || 1;
  const xs = (i: number) => pad + (i / (data.length - 1)) * (W - pad * 2);
  const ys = (v: number) => H - pad - ((v - mn) / rng) * (H - pad * 2 - 2);
  const line = data.map((v, i) => `${i ? 'L' : 'M'}${xs(i).toFixed(1)},${ys(v).toFixed(1)}`).join(' ');
  const area = `${line} L${xs(data.length - 1).toFixed(1)},${H} L${xs(0).toFixed(1)},${H} Z`;
  return (
    <svg className="ov-spark" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <linearGradient id={gid} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.28" />
          <stop offset="100%" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#${gid})`} />
      <path d={line} fill="none" stroke={color} strokeWidth="1.6" vectorEffect="non-scaling-stroke"
        strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

function KpiTile({ lab, val, unit, accent, spark, delta, goodWhenUp }: {
  lab: string; val: string; unit?: string; accent: string; spark?: number[];
  delta?: Delta | null; goodWhenUp?: boolean;
}) {
  // Color by whether the move is GOOD for this metric (README §Status
  // semantics): throughput/apdex up = good (green); failure/latency up =
  // bad (red). The .ov-delta classes encode up=err/down=ok by default, with
  // .up.good / .down.bad overrides for the goodWhenUp case.
  const deltaCls = delta
    ? `ov-delta ${delta.dir}${goodWhenUp && delta.dir === 'up' ? ' good' : ''}${goodWhenUp && delta.dir === 'down' ? ' bad' : ''}`
    : '';
  return (
    <div className="card ov-kpi">
      <div className="ov-kpi-accent" style={{ background: accent }} />
      <div className="ov-lab">{lab}</div>
      <div className="ov-val">{val}{unit && <span className="ov-unit">{unit}</span>}</div>
      {delta && (
        <div className={deltaCls}>
          {delta.dir === 'up' ? '▲' : delta.dir === 'down' ? '▼' : '—'} {delta.pct}%
          <span style={{ color: 'var(--text3)', fontWeight: 500 }}>vs prior</span>
        </div>
      )}
      {spark && spark.length > 1 && <OvSparkline data={spark} color={accent} />}
    </div>
  );
}

// One RED chart card: header (title + inline legend) + the compact uPlot
// OverviewChart. Accepts N lines over the same x axis (Response time draws
// p50/p95/p99 as three lines; throughput/failure draw one). The time axis
// comes from the first non-empty line — all aggs share the WHERE + step so
// the points align index-for-index.
interface ChartLine { series: SpanMetricSeries[]; color: string; label: string }
function ChartCard({ title, lines, unit, mode = 'line', deploy }: {
  title: string; lines: ChartLine[]; unit: string;
  mode?: 'line' | 'area' | 'stacked'; deploy?: { sec: number; label: string } | null;
}) {
  const times = useMemo(() => {
    const base = lines.find(l => (l.series[0]?.points ?? []).length)?.series[0]?.points ?? [];
    return base.map(p => p.time / 1e9);
  }, [lines]);
  const ovSeries = useMemo<OvChartSeries[]>(() =>
    lines
      .map(l => ({ label: l.label, color: l.color, data: (l.series[0]?.points ?? []).map(p => p.value) }))
      .filter(s => s.data.length),
  [lines]);
  return (
    <div className="card">
      <div className="ov-card-h">
        <h3>{title}</h3>
        <div className="ov-right">
          <span className="ov-legend">
            {lines.map(l => <span key={l.label}><i className="ov-sw" style={{ background: l.color }} />{l.label}</span>)}
          </span>
        </div>
      </div>
      <div className="ov-card-b" style={{ paddingTop: 10, paddingBottom: 10 }}>
        {times.length < 2 ? (
          <div style={{ height: 150, display: 'grid', placeItems: 'center', color: 'var(--text3)', fontSize: 12 }}>
            No data in this window
          </div>
        ) : (
          <OverviewChart times={times} series={ovSeries} unit={unit} mode={mode}
            deployAtSec={deploy?.sec ?? null} deployLabel={deploy?.label} />
        )}
      </div>
    </div>
  );
}

const PROB_ICON: Record<string, { ic: string; fg: string }> = {
  critical: { ic: '▲', fg: 'var(--err)' },
  warning:  { ic: '◆', fg: 'var(--warn)' },
  info:     { ic: '•', fg: 'var(--accent)' },
};

function relTime(ns: number): string {
  const ms = Date.now() - ns / 1e6;
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

export function ServiceOverview({ service, range, info, problems, operations }: Props) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const windowSec = Math.max(1, (to - from) / 1e9);

  // One batched span-metric call: rate + error_rate + p99 + p50 over the
  // same WHERE (service.name = svc). Feeds the KPI sparklines + RED charts.
  const seriesQ = useQuery({
    queryKey: ['service-overview-red', service, from, to],
    queryFn: () => api.spanMetricBatch({
      from, to,
      dsl: `service.name = "${service.replace(/"/g, '\\"')}"`,
      aggs: [
        { name: 'rate', agg: 'rate' },
        { name: 'error_rate', agg: 'error_rate' },
        { name: 'p99', agg: 'p99', field: 'duration_ms' },
        { name: 'p95', agg: 'p95', field: 'duration_ms' },
        { name: 'p50', agg: 'p50', field: 'duration_ms' },
      ],
    }),
    enabled: !!service,
    staleTime: 30_000,
  });
  const s = seriesQ.data;

  const deploysQ = useServiceDeploys(service, from, to);
  // The single deploy marker drawn on the charts = the latest deploy inside
  // the window (the design shows one ▼ flag).
  const deploy = useMemo(() => {
    const ds = (deploysQ.data ?? []).filter(d => d.timeUnixNs >= from && d.timeUnixNs <= to);
    if (!ds.length) return null;
    const latest = ds.reduce((a, b) => (b.timeUnixNs > a.timeUnixNs ? b : a));
    return { sec: latest.timeUnixNs / 1e9, label: latest.version };
  }, [deploysQ.data, from, to]);

  // Throughput stacked-area bands (OK vs Errors) derived from the MV-backed
  // rate + error_rate series — no extra query, no raw-spans scan (invariant
  // #3). Error band = rate × err%, OK band = the remainder; they stack to the
  // total rate. (A 4xx-vs-5xx split would need an HTTP-status MV dimension.)
  const throughputBands = useMemo<ChartLine[]>(() => {
    const ratePts = s?.rate?.[0]?.points ?? [];
    const erPts = s?.error_rate?.[0]?.points ?? [];
    if (ratePts.length < 2) return [{ series: s?.rate ?? [], color: 'var(--accent)', label: 'req/s' }];
    const okPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * (1 - (erPts[i]?.value ?? 0) / 100)) }));
    const errPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * ((erPts[i]?.value ?? 0) / 100)) }));
    return [
      { series: [{ groupKey: [], points: okPts }], color: 'var(--ok)', label: 'OK' },
      { series: [{ groupKey: [], points: errPts }], color: 'var(--err)', label: 'Errors' },
    ];
  }, [s]);

  if (!info) return null;
  const rps = info.spanCount / windowSec;
  const open = problems.filter(p => p.status !== 'resolved');

  // "Every metric is a doorway" (Phase C) — canonical descriptors for each KPI
  // + RED chart. The SAME object that the panel carries is what the Explorer
  // re-opens via MetricPanel's ⋮ / body-click / `e`. filters ALWAYS pin the
  // focused service; KPI tiles use viz:'stat', RED charts use viz:'line'. The
  // descriptor only feeds the doorway — it does NOT drive the rendered numbers
  // (those stay the existing info.* / span-metric series, byte-identical).
  const svcFilter = { 'service.name': service };
  const mkThroughput = (viz: MetricQuery['viz']) =>
    metricQuery({ metric: 'calls_total', agg: 'rate', unit: 'rps', filters: svcFilter, viz, range });
  const mkFailureRate = (viz: MetricQuery['viz']) =>
    metricQuery({ metric: 'calls_total', agg: 'error_rate', unit: '%', filters: svcFilter, viz, range });
  const mkLatency = (agg: 'p50' | 'p95' | 'p99', viz: MetricQuery['viz']) =>
    metricQuery({ metric: 'duration_milliseconds_bucket', agg, unit: 'ms', filters: svcFilter, viz, range });

  return (
    <div style={{ marginTop: 4 }}>
      {/* KPI row — golden signals + full-bleed trend sparklines. Each tile is
          wrapped in the reusable MetricPanel doorway (compact: a hover-revealed
          ⋮ + body-click → Explore); the tile body renders verbatim. */}
      <div className="ov-grid ov-kpis ov-mb">
        <MetricPanel compact title="Throughput" metricQuery={mkThroughput('stat')}>
          <KpiTile lab="Throughput" val={rps.toFixed(rps < 10 ? 1 : 0)} unit=" req/s" accent="var(--accent)" spark={vals(s?.rate)} delta={computeDelta(vals(s?.rate))} goodWhenUp />
        </MetricPanel>
        <MetricPanel compact title="Failure rate" metricQuery={mkFailureRate('stat')}>
          <KpiTile lab="Failure rate" val={`${info.errorRate.toFixed(2)}%`} accent="var(--err)" spark={vals(s?.error_rate)} delta={computeDelta(vals(s?.error_rate))} goodWhenUp={false} />
        </MetricPanel>
        <MetricPanel compact title="Response time · P99" metricQuery={mkLatency('p99', 'stat')}>
          <KpiTile lab="Response time · P99" val={info.p99DurationMs.toFixed(0)} unit=" ms" accent="var(--orange)" spark={vals(s?.p99)} delta={computeDelta(vals(s?.p99))} goodWhenUp={false} />
        </MetricPanel>
        <MetricPanel compact title="Response time · median" metricQuery={mkLatency('p50', 'stat')}>
          <KpiTile lab="Response time · median" val={(vals(s?.p50).slice(-1)[0] ?? info.avgDurationMs).toFixed(0)} unit=" ms" accent="var(--purple)" spark={vals(s?.p50)} delta={computeDelta(vals(s?.p50))} goodWhenUp={false} />
        </MetricPanel>
        {/* Apdex has no calls_total/duration descriptor analogue in the
            spanmetrics pipeline (it's a composite of latency thresholds), so it
            stays a plain tile — no doorway. */}
        <KpiTile lab="Apdex" val={(info.apdex ?? 0).toFixed(2)} accent="var(--ok)" />
      </div>

      {/* RED charts row — response time / throughput / failure rate, each
          with the deploy markers from the service bundle. Each chart carries
          its viz:'line' descriptor through the compact MetricPanel doorway. */}
      <div className="ov-grid ov-charts-3 ov-mb">
        <MetricPanel compact title="Response time" metricQuery={mkLatency('p99', 'line')}>
          <ChartCard title="Response time" unit=" ms" mode="line" deploy={deploy} lines={[
            { series: s?.p50 ?? [], color: 'var(--purple)', label: 'P50' },
            { series: s?.p95 ?? [], color: 'var(--orange)', label: 'P95' },
            { series: s?.p99 ?? [], color: 'var(--err)', label: 'P99' },
          ]} />
        </MetricPanel>
        <MetricPanel compact title="Throughput" metricQuery={mkThroughput('line')}>
          <ChartCard title="Throughput" unit=" req/s" mode="stacked" deploy={deploy} lines={throughputBands} />
        </MetricPanel>
        <MetricPanel compact title="Failure rate" metricQuery={mkFailureRate('line')}>
          <ChartCard title="Failure rate" unit="%" mode="area" deploy={deploy} lines={[
            { series: s?.error_rate ?? [], color: 'var(--err)', label: 'errors' },
          ]} />
        </MetricPanel>
      </div>

      {/* Upstream / downstream neighbors (prototype design-parity) — flat
          two-column list of inbound callers + outbound dependencies. Replaced
          the old in-page service-flow map; the full graph lives on /topology. */}
      <Neighbors service={service} range={range} from={from} to={to} />

      {/* Operations (compact) + Top DB statements */}
      <div className="ov-grid ov-cols-2 ov-mb">
        <OpsCard service={service} range={range} operations={operations} />
        <DbCard service={service} from={from} to={to} />
      </div>

      {/* Instances (infra health) + Recent problems & events */}
      <div className="ov-grid ov-cols-2 ov-mb">
        <ServiceInstancesCard service={service} since={range.preset} />
        <div className="card">
          <div className="ov-card-h">
            <h3>Recent problems &amp; events</h3>
            {open.length > 0 && <span className="ov-sub">{open.length} open</span>}
          </div>
          {open.length === 0 ? (
            <div className="ov-card-b" style={{ color: 'var(--text2)', fontSize: 13 }}>
              No open problems for {service} in this window.
            </div>
          ) : (
            <div>
              {open.slice(0, 8).map(p => {
                const sk = PROB_ICON[p.severity] ?? PROB_ICON.info;
                return (
                  <div className="ov-prob" key={p.id}>
                    <div className="ov-ic" style={{ background: 'var(--accent-soft)', color: sk.fg }}>{sk.ic}</div>
                    <div>
                      <div className="ov-ti">{p.ruleName}</div>
                      <div className="ov-de">{p.description}</div>
                    </div>
                    <div className="ov-tm">{relTime(p.startedAt)}</div>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
