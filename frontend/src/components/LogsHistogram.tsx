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
};
// Sort priority — bottom-most band in the stack is the most
// important (errors anchor the chart so they're always at the
// baseline of the eye's vertical scan).
const SEV_RANK: Record<string, number> = {
  FATAL: 6, ERROR: 5, WARN: 4, WARNING: 4, INFO: 3, DEBUG: 2, TRACE: 1,
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

  if (data === undefined) {
    return <div style={{ height: 80, marginBottom: 10 }} />;
  }
  if (data === null || stack.bands.length === 0) {
    return null;
  }

  const W = 1000, H = 80;
  const padT = 6, padB = 14, padL = 4, padR = 4;
  const innerW = W - padL - padR;
  const innerH = H - padT - padB;

  const xOf = (i: number) =>
    padL + (stack.times.length > 1 ? (i / (stack.times.length - 1)) * innerW : innerW / 2);
  const yOf = (v: number) =>
    padT + innerH - (v / Math.max(1, stack.max)) * innerH;

  // Render bands bottom-up. Each band's path = upper edge then
  // lower edge reversed = closed polygon.
  const bandPaths = stack.bands.map((band) => {
    const upper: string[] = [];
    const lower: string[] = [];
    for (let i = 0; i < stack.times.length; i++) {
      upper.push(`${xOf(i)},${yOf(band.cum[i])}`);
      lower.push(`${xOf(i)},${yOf(band.cum[i] - band.values[i])}`);
    }
    return `M ${upper.join(' L ')} L ${lower.reverse().join(' L ')} Z`;
  });

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 8, marginBottom: 10,
      display: 'flex', alignItems: 'flex-start', gap: 12,
    }}>
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" height={H}
        preserveAspectRatio="none"
        style={{ display: 'block', flex: 1 }}>
        {stack.bands.map((band, i) => (
          <path key={band.name} d={bandPaths[i]}
            fill={colorFor(band.name)} opacity={0.85} />
        ))}
      </svg>
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
