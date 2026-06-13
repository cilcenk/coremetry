import { useEffect, useMemo, useState } from 'react';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';

// LogsHistogram — Kibana-Discover-style stacked-area histogram
// for /logs. Same filter shape as the table below; severity-
// stacked so a spike of errors stands out against background
// info traffic without reading the count column.
//
// Backend: /api/logs/timeseries?groupBy=severity returns one
// LogSeries per severity (ERROR / WARN / INFO / DEBUG / …)
// plus a "_total" fallback when the backend doesn't split.
// SVG is plain — no chart library — so it stays inline with
// the rest of the page's bundled CSS.

type Filter = {
  service: string;
  search: string;
  severity: number;
  traceId: string;
  spanId: string;
};

type Series = { name: string; points: { t: number; v: number }[] };

const SEV_COLORS: Record<string, string> = {
  // OTel ECS canonical levels — both UPPER and lower because
  // shippers vary on casing.
  FATAL:  '#dc2626',
  ERROR:  '#ef4444',
  WARN:   '#f59e0b',
  WARNING: '#f59e0b',
  INFO:   '#3b82f6',
  DEBUG:  '#a3a3a3',
  TRACE:  '#a3a3a3',
  // v0.5.396 — synthetic band for severities that didn't fit
  // the top-50 terms agg (custom levels, mixed casing). Subtle
  // grey so the operator's eye still lands on errors first.
  OTHER: '#94a3b8',
};
// Sort priority — bottom-most band in the stack is the most
// important (errors anchor the chart so they're always at the
// baseline of the eye's vertical scan). OTHER sinks to the top
// of the stack (lowest rank) so it doesn't push errors off the
// baseline.
const SEV_RANK: Record<string, number> = {
  FATAL: 6, ERROR: 5, WARN: 4, WARNING: 4, INFO: 3, DEBUG: 2, TRACE: 1,
  OTHER: 0,
};

function colorFor(name: string): string {
  return SEV_COLORS[name.toUpperCase()] ?? '#6366f1';
}

export function LogsHistogram({ range, filter }: {
  range: { from?: number; to?: number };
  filter: Filter;
}) {
  const [data, setData] = useState<Series[] | null | undefined>(undefined);

  useEffect(() => {
    setData(undefined);
    if (!range.from && !range.to && !filter.traceId) {
      // No bounded window AND no trace pin → don't blow the
      // chart up on full retention; the table below already
      // applies its own bound.
      setData([]);
      return;
    }
    api.logsTimeseries({
      from: range.from, to: range.to,
      service: filter.service || undefined,
      search:  filter.search  || undefined,
      severity: filter.severity > 0 ? filter.severity : undefined,
      traceId: filter.traceId || undefined,
      groupBy: 'severity',
      bucketSec: pickBucket(range),
    })
      .then(d => setData(d ?? []))
      .catch(() => setData(null));
  }, [range.from, range.to, filter.service, filter.search, filter.severity, filter.traceId]);

  const stack = useMemo(() => buildStack(data ?? []), [data]);
  // x-axis time ticks (unix ns → clock). Hooks must precede the early
  // returns below, so compute alongside the stack.
  const ticks = useMemo(() => axisTicks(stack.times), [stack.times]);

  if (data === undefined) {
    return <div style={{ height: 80, marginBottom: 10 }} />;
  }
  if (data === null || stack.bands.length === 0) {
    return null;
  }

  const W = 1000, H = 80;
  const padT = 6, padB = 4, padL = 4, padR = 4;
  const innerW = W - padL - padR;
  const innerH = H - padT - padB;

  const n = stack.times.length;
  const slotW = n > 0 ? innerW / n : innerW;
  // ~18% inter-bar gap, clamped so a dense window still renders a hairline bar.
  const barW = Math.max(1, slotW * 0.82);
  const xOf = (i: number) => padL + i * slotW + (slotW - barW) / 2;
  const yOf = (v: number) =>
    padT + innerH - (v / Math.max(1, stack.max)) * innerH;

  // Per-bucket hover summary (native SVG <title>): clock + per-level counts.
  const spanSec = n > 1 ? (stack.times[n - 1] - stack.times[0]) / 1e9 : 0;
  const withSec = spanSec > 0 && spanSec < 600;

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 8, marginBottom: 10,
      display: 'flex', alignItems: 'flex-start', gap: 12,
    }}>
      <div style={{ flex: 1, minWidth: 0 }}>
        {/* Kibana-Discover-style STACKED BARS — one column per time bucket,
            segmented by severity. (Was a stacked-area chart, which read as a
            line graph rather than a histogram.) */}
        <svg viewBox={`0 0 ${W} ${H}`} width="100%" height={H}
          preserveAspectRatio="none"
          style={{ display: 'block', width: '100%' }}>
          {stack.bands.map(band => (
            <g key={band.name} fill={colorFor(band.name)}>
              {band.values.map((v, i) => {
                if (v <= 0) return null;
                const yTop = yOf(band.cum[i]);
                const h = yOf(band.cum[i] - v) - yTop;
                return <rect key={i} x={xOf(i)} y={yTop} width={barW} height={Math.max(0.5, h)} />;
              })}
            </g>
          ))}
          {/* Transparent full-height hit targets carry the per-bucket tooltip. */}
          {stack.times.map((t, i) => {
            const total = stack.bands.reduce((s, b) => s + b.values[i], 0);
            if (total <= 0) return null;
            const parts = stack.bands
              .filter(b => b.values[i] > 0)
              .map(b => `${b.name} ${fmtNum(b.values[i])}`);
            return (
              <rect key={`h${i}`} x={padL + i * slotW} y={padT} width={slotW} height={innerH} fill="transparent">
                <title>{`${fmtClock(t, withSec)} · ${fmtNum(total)} logs — ${parts.join(' · ')}`}</title>
              </rect>
            );
          })}
        </svg>
        {ticks.length > 0 && (
          <div style={{
            display: 'flex', justifyContent: 'space-between',
            marginTop: 3, fontSize: 10, color: 'var(--text3)',
            fontFamily: 'ui-monospace, monospace',
          }}>
            {ticks.map((t, i) => <span key={i}>{t}</span>)}
          </div>
        )}
      </div>
      <div style={{
        display: 'flex', flexDirection: 'column', gap: 2,
        fontSize: 11, minWidth: 100,
      }}>
        {stack.bands.slice().reverse().map(b => (
          <span key={b.name} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <span style={{ width: 10, height: 10, background: colorFor(b.name), borderRadius: 2 }} />
            <span style={{ color: 'var(--text2)' }}>{b.name}</span>
            <span style={{ color: 'var(--text3)', fontFamily: 'ui-monospace, monospace' }}>
              {fmtNum(b.total)}
            </span>
          </span>
        ))}
      </div>
    </div>
  );
}

// pickBucket picks a sensible histogram resolution from the
// window size — same heuristic the Explore page uses so the
// chart never has more than ~120 buckets (browser-friendly)
// or fewer than ~20 (looks empty).
function pickBucket(range: { from?: number; to?: number }): number {
  if (!range.from || !range.to) return 30;
  const spanSec = (range.to - range.from) / 1_000_000_000;
  if (spanSec < 60 * 15)     return 5;     // <15min  → 5s buckets
  if (spanSec < 60 * 60)     return 30;    // <1h     → 30s
  if (spanSec < 60 * 60 * 6) return 60;    // <6h     → 1m
  if (spanSec < 60 * 60 * 24) return 5*60; // <24h    → 5m
  return 15 * 60;                          // ≥24h    → 15m
}

// axisTicks — up to 6 evenly-spaced bucket timestamps (unix ns)
// formatted as clock labels for the x-axis. Seconds are shown only
// for sub-10-minute windows so a 5s-bucket view doesn't collapse to
// a row of identical HH:MM. Rendered as HTML below the SVG (the SVG
// is non-uniformly stretched, which would distort in-svg <text>).
function axisTicks(times: number[]): string[] {
  if (times.length === 0) return [];
  const spanSec = (times[times.length - 1] - times[0]) / 1e9;
  const withSec = spanSec > 0 && spanSec < 600;
  const N = Math.min(6, times.length);
  const out: string[] = [];
  for (let k = 0; k < N; k++) {
    const idx = Math.round((k * (times.length - 1)) / Math.max(1, N - 1));
    out.push(fmtClock(times[idx], withSec));
  }
  return out;
}

function fmtClock(ns: number, withSec: boolean): string {
  const d = new Date(ns / 1e6);
  const p = (n: number) => n.toString().padStart(2, '0');
  return withSec
    ? `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
    : `${p(d.getHours())}:${p(d.getMinutes())}`;
}

// buildStack turns the per-severity series into a cumulative
// stack indexed by time bucket, with the most-severe levels at
// the BOTTOM (highest yOf, lowest visual y) so the operator's
// eye lands on errors first.
function buildStack(input: Series[]) {
  if (input.length === 0) return { bands: [] as Band[], times: [] as number[], max: 0 };
  // Union of bucket timestamps across series.
  const timeSet = new Set<number>();
  for (const s of input) for (const p of s.points) timeSet.add(p.t);
  const times = Array.from(timeSet).sort((a, b) => a - b);
  // Per-series indexed counts.
  const sorted = input.slice().sort((a, b) =>
    (SEV_RANK[a.name.toUpperCase()] ?? 0) - (SEV_RANK[b.name.toUpperCase()] ?? 0));
  // Cumulative running total per time bucket — each band rides
  // on top of the previous.
  const cumRun = new Array(times.length).fill(0);
  const bands: Band[] = [];
  let max = 0;
  for (const s of sorted) {
    const idx = new Map<number, number>();
    for (const p of s.points) idx.set(p.t, p.v);
    const values = times.map(t => idx.get(t) ?? 0);
    const cum = values.map((v, i) => cumRun[i] + v);
    for (let i = 0; i < cum.length; i++) cumRun[i] = cum[i];
    if (cum.length > 0) max = Math.max(max, Math.max(...cum));
    const total = values.reduce((a, b) => a + b, 0);
    bands.push({ name: s.name, values, cum, total });
  }
  return { bands, times, max };
}

type Band = {
  name: string;
  values: number[];
  cum: number[];
  total: number;
};
