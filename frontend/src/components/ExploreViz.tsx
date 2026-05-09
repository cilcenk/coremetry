import { useMemo } from 'react';
import { hashColor } from '@/lib/utils';
import { MultiLineChart } from './MultiLineChart';
import type { ExploreSeries, SpanMetricSeries } from '@/lib/types';

// Visualization picker covering the four shapes the operator
// switches between in the Data Explorer:
//
//   line  — overlay timeseries; default for "how is X over time".
//   bar   — same data, vertical bars; better for low-cardinality
//           categorical splits.
//   topN  — collapse each series to its latest value + sort desc;
//           "which dimensions are biggest right now".
//   kpi   — single number across all series for the latest bucket.
//
// All four take the same normalised ExploreSeries[] so swapping
// between them is a single state flip — no re-fetch, no shape
// translation.
export type ExploreVizKind = 'line' | 'bar' | 'topN' | 'kpi';

export function ExploreViz({ series, kind, unit }: {
  series: ExploreSeries[];
  kind: ExploreVizKind;
  unit?: string;
}) {
  if (!series || series.length === 0) {
    return <div style={{ color: 'var(--text3)', fontSize: 12 }}>No data in this window.</div>;
  }
  switch (kind) {
    case 'line': return <UPlotLine series={series} unit={unit} />;
    case 'bar':  return <LineViz   series={series} unit={unit} mode="bar" />;
    case 'topN': return <TopNViz   series={series} unit={unit} />;
    case 'kpi':  return <KpiViz    series={series} unit={unit} />;
  }
}

// UPlotLine — for `kind: 'line'`. Adapts ExploreSeries → the
// SpanMetricSeries shape MultiLineChart already consumes, so
// the explore line view gets the same hover crosshair, time
// axis, and click-to-isolate legend as the /metrics and
// /dashboards line views. Avoids maintaining two separate
// line implementations (the SVG one above was visually basic
// and missing a tooltip).
function UPlotLine({ series, unit }: { series: ExploreSeries[]; unit?: string }) {
  const adapted: SpanMetricSeries[] = useMemo(() =>
    series.map(s => ({
      groupKey: s.name ? [s.name] : [],
      // ExploreSeries point timestamps are in nanoseconds (the
      // /api/logs/timeseries + /api/metric responses agree on
      // ns), MultiLineChart already divides by 1e9. Field
      // names differ — t/v vs time/value — so we re-map.
      points: s.points.map(p => ({ time: p.t, value: p.v })),
    })),
  [series]);
  return <MultiLineChart series={adapted} unit={unit} height={320} />;
}

// ── Line / Bar ──────────────────────────────────────────────────────

function LineViz({ series, unit, mode }: {
  series: ExploreSeries[];
  unit?: string;
  mode: 'line' | 'bar';
}) {
  const w = 920, h = 280, padL = 56, padR = 16, padT = 16, padB = 28;

  const flat = useMemo(() => {
    const all: { t: number; v: number; idx: number }[] = [];
    series.forEach((s, idx) => s.points.forEach(p => all.push({ t: p.t, v: p.v, idx })));
    return all;
  }, [series]);

  if (flat.length === 0) return <div style={{ color: 'var(--text3)', fontSize: 12 }}>Empty.</div>;

  const ts = flat.map(p => p.t);
  const vs = flat.map(p => p.v);
  const tMin = Math.min(...ts), tMax = Math.max(...ts);
  let vMin = Math.min(0, ...vs);
  let vMax = Math.max(...vs);
  if (vMax === vMin) vMax = vMin + 1;

  const x = (t: number) => padL + ((t - tMin) / Math.max(1, tMax - tMin)) * (w - padL - padR);
  const y = (v: number) => h - padB - ((v - vMin) / (vMax - vMin)) * (h - padT - padB);

  return (
    <div>
      <svg viewBox={`0 0 ${w} ${h}`} style={{ width: '100%', height: 280 }}>
        {/* y-axis grid lines + labels */}
        {Array.from({ length: 4 }, (_, i) => (i + 1) / 4).map(f => {
          const v = vMin + (vMax - vMin) * f;
          const yy = y(v);
          return (
            <g key={f}>
              <line x1={padL} y1={yy} x2={w - padR} y2={yy}
                stroke="var(--border)" strokeWidth={0.5} strokeDasharray="2 2" />
              <text x={padL - 6} y={yy + 4} fontSize={10}
                fill="var(--text3)" textAnchor="end">
                {fmt(v, unit)}
              </text>
            </g>
          );
        })}
        {/* x-axis endpoints */}
        <text x={padL}      y={h - 8} fontSize={10} fill="var(--text3)">
          {fmtTime(tMin)}
        </text>
        <text x={w - padR}  y={h - 8} fontSize={10} fill="var(--text3)" textAnchor="end">
          {fmtTime(tMax)}
        </text>

        {/* Series */}
        {series.map((s, i) => {
          const color = hashColor(s.name || `s${i}`);
          if (mode === 'line') {
            const d = s.points
              .sort((a, b) => a.t - b.t)
              .map((p, j) => `${j === 0 ? 'M' : 'L'} ${x(p.t)} ${y(p.v)}`)
              .join(' ');
            return <path key={i} d={d} fill="none" stroke={color} strokeWidth={1.5} />;
          }
          // bar
          const bw = Math.max(1, (w - padL - padR) / Math.max(1, s.points.length) - 1);
          return (
            <g key={i}>
              {s.points.map((p, j) => {
                const xx = x(p.t);
                const yy = y(p.v);
                const yz = y(0);
                return (
                  <rect key={j} x={xx - bw / 2} y={Math.min(yy, yz)}
                    width={bw} height={Math.abs(yz - yy)}
                    fill={color} fillOpacity={0.75} />
                );
              })}
            </g>
          );
        })}
      </svg>

      {/* Legend */}
      {series.length > 1 && (
        <div style={{
          display: 'flex', flexWrap: 'wrap', gap: 10, marginTop: 6, fontSize: 11,
        }}>
          {series.map((s, i) => (
            <span key={i} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              <span style={{ width: 10, height: 10, background: hashColor(s.name || `s${i}`), display: 'inline-block', borderRadius: 2 }} />
              {s.name}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

// ── Top-N table ──────────────────────────────────────────────────

function TopNViz({ series, unit }: { series: ExploreSeries[]; unit?: string }) {
  const rows = useMemo(() => {
    return series
      .map(s => {
        const latest = s.points.length > 0 ? s.points[s.points.length - 1].v : 0;
        const total = s.points.reduce((sum, p) => sum + p.v, 0);
        return { name: s.name, latest, total };
      })
      .sort((a, b) => b.latest - a.latest);
  }, [series]);
  const max = rows.length > 0 ? Math.max(...rows.map(r => r.latest)) : 1;

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th className="num">Latest</th>
            <th className="num">Window total</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {rows.map(r => (
            <tr key={r.name}>
              <td>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                  <span style={{ width: 8, height: 8, background: hashColor(r.name),
                                  borderRadius: '50%', display: 'inline-block' }} />
                  {r.name}
                </span>
              </td>
              <td className="num mono">{fmt(r.latest, unit)}</td>
              <td className="num mono">{fmt(r.total, unit)}</td>
              <td style={{ width: '40%' }}>
                <div style={{ height: 10, background: 'var(--bg3)', borderRadius: 3 }}>
                  <div style={{
                    width: `${(r.latest / max) * 100}%`,
                    height: '100%', background: hashColor(r.name), borderRadius: 3,
                  }} />
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ── Single-value KPI ─────────────────────────────────────────────

function KpiViz({ series, unit }: { series: ExploreSeries[]; unit?: string }) {
  const total = useMemo(() => {
    return series.reduce((sum, s) => {
      if (s.points.length === 0) return sum;
      return sum + s.points[s.points.length - 1].v;
    }, 0);
  }, [series]);
  return (
    <div style={{ padding: '24px 0', display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
      <div style={{
        fontSize: 11, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.6, fontWeight: 600,
      }}>
        Latest bucket · {series.length} series
      </div>
      <div style={{
        fontSize: 56, fontWeight: 700, marginTop: 4, fontVariantNumeric: 'tabular-nums',
      }}>
        {fmt(total, unit)}
      </div>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
        sum of every series' last point
      </div>
    </div>
  );
}

// ── Helpers ──────────────────────────────────────────────────────

function fmt(v: number, unit?: string): string {
  if (!isFinite(v)) return '—';
  if (unit === 'ms') return `${v.toFixed(1)}ms`;
  if (unit === '%')  return `${v.toFixed(2)}%`;
  if (unit === '/s') return `${v.toFixed(1)}/s`;
  if (unit === 'bytes') {
    const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    let i = 0, x = v;
    while (x >= 1024 && i < u.length - 1) { x /= 1024; i++; }
    return `${x.toFixed(x < 10 ? 2 : 1)} ${u[i]}`;
  }
  if (Math.abs(v) >= 1000) return v.toLocaleString();
  return Number.isInteger(v) ? String(v) : v.toFixed(2);
}

function fmtTime(t: number): string {
  const d = new Date(t / 1e6);
  return d.toLocaleTimeString('en', { hour12: false });
}
