import { useId, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { Service, Problem, TimeRange, SpanMetricSeries, OperationSummary } from '@/lib/types';
import { timeRangeToNs } from '@/lib/utils';
import { api } from '@/lib/api';
import { useServiceDeploys } from '@/lib/queries';
import { OverviewChart, type OvChartSeries } from './charts/OverviewChart';
import { ServiceFlow } from './ServiceFlow';
import { OpsCard, DbCard } from './OverviewTables';

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

function KpiTile({ lab, val, unit, accent, spark }: {
  lab: string; val: string; unit?: string; accent: string; spark?: number[];
}) {
  return (
    <div className="card ov-kpi">
      <div className="ov-kpi-accent" style={{ background: accent }} />
      <div className="ov-lab">{lab}</div>
      <div className="ov-val">{val}{unit && <span className="ov-unit">{unit}</span>}</div>
      {spark && spark.length > 1 && <OvSparkline data={spark} color={accent} />}
    </div>
  );
}

// One RED chart card: header (title + inline legend swatch) + the compact
// uPlot OverviewChart. Extracts the (single) span-metric series into the
// times + values OverviewChart consumes.
function ChartCard({ title, series, color, label, unit, mode = 'line', deploy }: {
  title: string; series: SpanMetricSeries[]; color: string; label: string;
  unit: string; mode?: 'line' | 'area'; deploy?: { sec: number; label: string } | null;
}) {
  const times = useMemo(() => (series[0]?.points ?? []).map(p => p.time / 1e9), [series]);
  const ovSeries = useMemo<OvChartSeries[]>(() => {
    const pts = series[0]?.points ?? [];
    return pts.length ? [{ label, color, data: pts.map(p => p.value) }] : [];
  }, [series, label, color]);
  return (
    <div className="card">
      <div className="ov-card-h">
        <h3>{title}</h3>
        <div className="ov-right">
          <span className="ov-legend">
            <span><i className="ov-sw" style={{ background: color }} />{label}</span>
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

  if (!info) return null;
  const rps = info.spanCount / windowSec;
  const open = problems.filter(p => p.status !== 'resolved');

  return (
    <div style={{ marginTop: 4 }}>
      {/* KPI row — golden signals + full-bleed trend sparklines. */}
      <div className="ov-grid ov-kpis ov-mb">
        <KpiTile lab="Throughput" val={rps.toFixed(rps < 10 ? 1 : 0)} unit=" req/s" accent="var(--accent)" spark={vals(s?.rate)} />
        <KpiTile lab="Failure rate" val={`${info.errorRate.toFixed(2)}%`} accent="var(--err)" spark={vals(s?.error_rate)} />
        <KpiTile lab="Response time · P99" val={info.p99DurationMs.toFixed(0)} unit=" ms" accent="var(--orange)" spark={vals(s?.p99)} />
        <KpiTile lab="Response time · median" val={(vals(s?.p50).slice(-1)[0] ?? info.avgDurationMs).toFixed(0)} unit=" ms" accent="var(--purple)" spark={vals(s?.p50)} />
        <KpiTile lab="Apdex" val={(info.apdex ?? 0).toFixed(2)} accent="var(--ok)" />
      </div>

      {/* RED charts row — response time / throughput / failure rate, each
          with the deploy markers from the service bundle. */}
      <div className="ov-grid ov-charts-3 ov-mb">
        <ChartCard title="Response time" series={s?.p99 ?? []} color="var(--orange)" label="P99" unit=" ms" mode="line" deploy={deploy} />
        <ChartCard title="Throughput" series={s?.rate ?? []} color="var(--accent)" label="req/s" unit=" req/s" mode="area" deploy={deploy} />
        <ChartCard title="Failure rate" series={s?.error_rate ?? []} color="var(--err)" label="errors" unit="%" mode="area" deploy={deploy} />
      </div>

      {/* Service flow — 1-hop request-path map (callers → svc → deps) */}
      <ServiceFlow service={service} range={range} from={from} to={to} />

      {/* Operations (compact) + Top DB statements */}
      <div className="ov-grid ov-cols-2 ov-mb">
        <OpsCard service={service} range={range} operations={operations} />
        <DbCard service={service} from={from} to={to} />
      </div>

      {/* Recent problems & events */}
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
  );
}
