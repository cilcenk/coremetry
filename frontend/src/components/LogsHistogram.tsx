import { useEffect, useMemo, useState, useRef } from 'react';
import { api } from '@/lib/api';
import { severityBandOf } from '@/lib/severityBand';
import { TimeChart, type TimeChartSeries } from '@/components/charts/TimeChart';

// LogsHistogram — log volume over time for /logs, the service Logs tab and
// the anomaly drawer.
//
// v0.9.218 — was 362 lines of hand-drawn SVG stacking one band per severity.
// That stack was the reason the chart couldn't answer its own question: bands
// are stacked linearly against a shared max, so at a realistic INFO:ERROR
// ratio of 100:1 the error band computes to well under a pixel and got
// clamped to a 0.5px hairline. The component's own header comment promised
// "a spike of errors stands out" — untrue anywhere the ratio is worse than
// about 1:20, i.e. in production. It also spent ~95% of its ink on INFO in
// the page's accent colour: the most mundane data got the loudest treatment.
//
// Now a thin adapter over the shared <TimeChart> primitive (VolumeChart is
// the precedent). Total volume in a neutral grey, the ERROR share overlaid
// in red, and the error RATE as a line on the right axis — so a spike that
// is invisible by count is unmissable by proportion. Switching to the shared
// primitive also inherits the y axis, gridlines, crosshair tooltip,
// double-click zoom-reset and theme re-resolution the hand-drawn version
// never had, and fixes a real brush bug: bar x positions were index-based
// while the selection interpolated linear time, so on the sparse series ES/CH
// actually return (min_doc_count: 1 drops empty buckets) dragging over a
// visible spike selected a DIFFERENT window — worst exactly when the ERROR
// facet was on and the series was at its sparsest.

type Filter = {
  service: string;
  cluster?: string; // v0.9.216 — was absent, so the chart ignored the toolbar's cluster select
  env?: string; // v0.8.400 — global ?env= deployment-environment filter
  search: string;
  severity: number;
  traceId: string;
  spanId: string;
  // v0.9.287 — was absent, exactly like `cluster` before v0.9.216, and
  // with the same consequence: "◆ With trace" narrowed the severity
  // chips and the table but NOT the chart between them. The parent
  // already spreads the value in; only the type and the request were
  // missing it.
  hasTrace?: boolean;
};

type Series = { name: string; points: { t: number; v: number }[] };

export function LogsHistogram({ range, filter, onRangeSelect , onSeries }: {
  range: { from?: number; to?: number };
  filter: Filter;
  // Drag-select a horizontal span → called with the selection as unix-ns
  // bounds; the parent narrows its time range. Omitted → hover-only.
  onRangeSelect?: (fromNs: number, toNs: number) => void;
  // v0.9.358 — opsiyonel; verilirse fetch edilen serilerin bant toplamları iletilir.
  onSeries?: (s: { name: string; total: number }[]) => void;
}) {
  const [data, setData] = useState<Series[] | null | undefined>(undefined);
  // v0.9.358 — the per-band series this chart ALREADY fetches, exposed to the
  // parent so chip counts can be honest without a second request. Ref'd (the
  // onZoomRef pattern) so a per-render callback identity doesn't re-run the
  // fetch effect.
  const onSeriesRef = useRef(onSeries); onSeriesRef.current = onSeries;

  useEffect(() => {
    setData(undefined);
    if (!range.from && !range.to && !filter.traceId) {
      // No bounded window AND no trace pin → don't blow the chart up on full
      // retention; the table below applies its own bound.
      setData([]);
      return;
    }
    api.logsTimeseries({
      from: range.from, to: range.to,
      service: filter.service || undefined,
      cluster: filter.cluster || undefined, // v0.9.216
      env:     filter.env     || undefined, // v0.8.400 — global env filter
      search:  filter.search  || undefined,
      severity: filter.severity > 0 ? filter.severity : undefined,
      traceId: filter.traceId || undefined,
      hasTrace: filter.hasTrace || undefined, // v0.9.287
      groupBy: 'severity',
      bucketSec: pickBucket(range),
    })
      .then(d => {
        setData(d ?? []);
        onSeriesRef.current?.((d ?? []).map(sr => ({
          name: sr.name,
          total: sr.points.reduce((a, p) => a + p.v, 0),
        })));
      })
      .catch(() => setData(null));
  }, [range.from, range.to, filter.service, filter.cluster, filter.env, filter.search, filter.severity, filter.traceId, filter.hasTrace]);

  const { times, series, totals } = useMemo(
    () => collapse(data ?? [], filter.severity > 0), [data, filter.severity]);

  if (data === undefined) return <div style={{ height: 104, marginBottom: 10 }} />;
  if (data === null || times.length === 0) return null;

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 8, marginBottom: 10,
    }}>
      <div style={{
        display: 'flex', justifyContent: 'flex-end', marginBottom: 4,
        fontSize: 10, color: 'var(--text-faint)',
        fontFamily: 'ui-monospace, monospace',
      }}>
        {/* The hand-drawn version's only hint that it was draggable was a
            crosshair cursor. Say it. */}
        <span>
          {totals.ratePct !== null && totals.error > 0 && `${fmtPct(totals.ratePct)} hata · `}
          {/* v0.9.287 — a severity floor narrows the denominator, so the
              rate isn't the error rate any more. Say that instead of
              printing a plausible wrong number (or nothing at all). */}
          {totals.ratePct === null && filter.severity > 0 && (
            <span title="Hata oranı tüm loglara göre hesaplanır. Seviye süzgeci açıkken payda zaten süzülmüş oluyor, o yüzden oran gösterilmiyor — süzgeci kaldırınca geri gelir.">
              seviye süzgeci açık — oran yok ·{' '}
            </span>
          )}
          sürükle = zaman seç · çift tık = geri
        </span>
      </div>
      <TimeChart
        times={times}
        series={series}
        height={92}
        rightUnit="%"
        fmtRight={fmtPct}
        onBrush={onRangeSelect
          ? (fromMs, toMs) => onRangeSelect(fromMs * 1e6, toMs * 1e6)
          : undefined}
      />
    </div>
  );
}

// pickBucket picks a histogram resolution from the window size — the same
// heuristic Explore uses, so the chart never has more than ~120 buckets
// (browser-friendly) or fewer than ~20 (looks empty).
function pickBucket(range: { from?: number; to?: number }): number {
  if (!range.from || !range.to) return 30;
  const spanSec = (range.to - range.from) / 1_000_000_000;
  if (spanSec < 60 * 15)     return 5;     // <15min  → 5s buckets
  if (spanSec < 60 * 60)     return 30;    // <1h     → 30s
  if (spanSec < 60 * 60 * 6) return 60;    // <6h     → 1m
  if (spanSec < 60 * 60 * 24) return 5*60; // <24h    → 5m
  return 15 * 60;                          // ≥24h    → 15m
}

function fmtPct(v: number): string {
  if (v <= 0) return '0%';
  if (v < 1) return `${v.toFixed(2)}%`;
  return `${v.toFixed(v < 10 ? 1 : 0)}%`;
}

// collapse folds the per-severity series into the three the chart draws:
// total volume, the ERROR share of it, and the error rate. Severity names
// vary by shipper (casing, numeric severity_number strings, custom
// vocabularies) so every name resolves through severityBandOf — the same
// classifier the chips and badges use, so the chart can never disagree
// with the numbers beside it.
//
// severityFiltered (v0.9.287) — the caller applied a severity floor, so
// the rows we got back are already a subset. The error RATE is
// undefined against that denominator and is withheld rather than
// guessed; totals.ratePct goes null for the same reason.
export function collapse(input: Series[], severityFiltered = false) {
  const empty = {
    times: [] as number[],
    series: [] as TimeChartSeries[],
    totals: { all: 0, error: 0, ratePct: null as number | null },
  };
  if (input.length === 0) return empty;

  const timeSet = new Set<number>();
  for (const s of input) for (const p of s.points) timeSet.add(p.t);
  const tsNs = Array.from(timeSet).sort((a, b) => a - b);
  if (tsNs.length === 0) return empty;

  const all = new Array(tsNs.length).fill(0);
  const err = new Array(tsNs.length).fill(0);
  const idx = new Map(tsNs.map((t, i) => [t, i]));

  for (const s of input) {
    const isError = severityBandOf(s.name) === 'ERROR';
    for (const p of s.points) {
      const i = idx.get(p.t);
      if (i === undefined) continue;
      all[i] += p.v;
      if (isError) err[i] += p.v;
    }
  }

  // Rate is null (a gap, not a zero) in buckets with no logs at all —
  // 0/0 is "we don't know", and drawing it as 0% invents a clean window.
  //
  // v0.9.287 — and null for the WHOLE series when a severity floor is
  // active, because then `all` is not the population the rate is about.
  // err/all was being read off a denominator the filter had already
  // narrowed: at ERROR the line pinned to a flat 100% (every remaining
  // log is an error) and the red bars sat exactly on the grey ones; at
  // WARN it silently became error/(warn+error) — a number that looks
  // perfectly reasonable and is not the error rate. Clicking ERROR is
  // this page's most frequent action, so this was the most-seen wrong
  // number on the surface.
  const rate: (number | null)[] = severityFiltered
    ? all.map(() => null)
    : all.map((n, i) => (n > 0 ? (err[i] / n) * 100 : null));

  const sumAll = all.reduce((a, b) => a + b, 0);
  const sumErr = err.reduce((a, b) => a + b, 0);

  // Draw order = overlay order: the full bar first, the error share on top
  // of it (reads from the baseline), the rate line last.
  const series: TimeChartSeries[] = [
    {
      key: 'total', label: 'logs', data: all, type: 'bar', axis: 'left',
      // Deliberately NOT the accent: the loudest colour should mark the
      // rarest, most actionable data, not the background traffic.
      color: 'color-mix(in srgb, var(--text3) 45%, transparent)',
    },
    { key: 'error', label: 'errors', data: err, type: 'bar', axis: 'left', color: 'var(--err)' },
    {
      key: 'rate', label: 'error rate', data: rate, type: 'line', axis: 'right',
      color: 'var(--warn)', width: 1.8, pointsShow: true,
    },
  ];

  return {
    times: tsNs.map(t => Math.round(t / 1e9)), // ns → unix sec
    series,
    totals: {
      all: sumAll,
      error: sumErr,
      // v0.9.287 — same reasoning as the series: under a severity floor
      // this ratio is not the error rate, so it is withheld, not shown.
      ratePct: severityFiltered || sumAll === 0 ? null : (sumErr / sumAll) * 100,
    },
  };
}
