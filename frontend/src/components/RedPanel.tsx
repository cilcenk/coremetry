import { useEffect, useState } from 'react';
import { MultiLineChart } from './MultiLineChart';
import { Spinner, Empty } from './Spinner';
import { api } from '@/lib/api';
import type { SpanMetricSeries, FilterExpr } from '@/lib/types';

// RedPanel — v0.5.258. Uptrace-style "RED" view: Rate /
// Errors / Duration stacked as three synced sub-panels in one
// card. The operator hovers any chart → the cursor line snaps to
// the same timestamp on all three, so "what's happening to all
// three metrics at this instant" reads at a glance.
//
// Three parallel api.spanMetric calls fan out (rate, error_rate,
// p99 latency) over the same filter + window. uPlot's built-in
// `cursor.sync` (via the shared syncKey prop on MultiLineChart)
// keeps the cursors in lockstep without any per-chart wiring on
// our side.
//
// Why a separate component:
//   • Keeps the already-1500-line Explore.tsx from growing more
//   • Internal fetch state means callers don't need to thread
//     three series+loading+error states through the page
//   • Future "RED for service / RED for operation" surfaces can
//     reuse the same component with different filter inputs.

interface Props {
  // Same shape Explore.tsx already passes to spanMetric:
  filters: FilterExpr[];
  dsl?: string;
  from: number;
  to: number;
  groupBy: string[];
  step: number; // 0 = auto
  field: string; // duration_ms etc — for the quantiles agg
}

interface RedData {
  rate:      SpanMetricSeries[] | null;
  errorRate: SpanMetricSeries[] | null;
  p99:       SpanMetricSeries[] | null;
}

export function RedPanel({ filters, dsl, from, to, groupBy, step, field }: Props) {
  const [data, setData] = useState<RedData | null | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    setData(undefined);
    // Encode filters the same way Explore.tsx does for the
    // single-agg path. spanMetric ignores empty params, so we
    // pass through verbatim.
    const filterArg = filters.length > 0 ? JSON.stringify(filters) : undefined;
    const common = {
      filters: filterArg,
      dsl: dsl || undefined,
      groupBy: groupBy.join(',') || undefined,
      from, to,
      step: step || undefined,
    };
    // Three parallel queries — backend caches each one with its
    // own key so subsequent renders are warm without further
    // round trips.
    Promise.all([
      api.spanMetric({ ...common, agg: 'rate',       field: 'count' }).catch(() => null),
      api.spanMetric({ ...common, agg: 'error_rate', field: 'count' }).catch(() => null),
      api.spanMetric({ ...common, agg: 'p99',        field }).catch(() => null),
    ]).then(([rate, errorRate, p99]) => {
      if (cancelled) return;
      setData({ rate, errorRate, p99 });
    });
    return () => { cancelled = true; };
  }, [JSON.stringify(filters), dsl, from, to, JSON.stringify(groupBy), step, field]);

  if (data === undefined) return <Spinner />;
  if (data === null ||
      (data.rate === null && data.errorRate === null && data.p99 === null)) {
    return (
      <Empty icon="◎" title="No data for this query">
        Try a wider time range, fewer filters, or remove split keys.
      </Empty>
    );
  }

  // syncKey shared across the three charts so uPlot's cursor.sync
  // links them. The page-unique-ish key keeps multiple RedPanel
  // mounts (future "compare two services side-by-side") from
  // accidentally cross-syncing.
  const syncKey = `red-${from}-${to}`;

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14,
      display: 'flex', flexDirection: 'column', gap: 14,
    }}>
      <RedSubPanel title="Rate" subtitle="requests / minute"
        series={data.rate} unit="" syncKey={syncKey} />
      <RedSubPanel title="Errors" subtitle="error rate (0..1)"
        series={data.errorRate} unit="" syncKey={syncKey} />
      <RedSubPanel title="Duration" subtitle="p99 latency"
        series={data.p99} unit="ms" syncKey={syncKey} />
    </div>
  );
}

function RedSubPanel({ title, subtitle, series, unit, syncKey }: {
  title: string;
  subtitle: string;
  series: SpanMetricSeries[] | null;
  unit: string;
  syncKey: string;
}) {
  return (
    <div>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 6,
      }}>
        <span style={{
          fontSize: 11, fontWeight: 700, color: 'var(--text2)',
          textTransform: 'uppercase', letterSpacing: 0.5,
        }}>{title}</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>{subtitle}</span>
      </div>
      {series === null ? (
        <div style={{ fontSize: 11, color: 'var(--err)' }}>
          Failed to load — check filters / time range.
        </div>
      ) : series.length === 0 ? (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No data in this window.
        </div>
      ) : (
        <MultiLineChart series={series} unit={unit} height={180} syncKey={syncKey} />
      )}
    </div>
  );
}
