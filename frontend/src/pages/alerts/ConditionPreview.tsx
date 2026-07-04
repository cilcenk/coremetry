import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { MultiLineChart } from '@/components/MultiLineChart';
import { metricQuery, type MetricAgg, type MetricUnit } from '@/lib/metricQuery';
import { api } from '@/lib/api';
import type { AlertRule } from '@/lib/types';

// PREVIEW_METRIC — alert-rule metric → MetricQuery descriptor for the live
// condition preview. Mirrors the evaluator's metric set (error_rate / p99_ms /
// request_rate / error_count).
const PREVIEW_METRIC: Record<string, { agg: MetricAgg; metric: string; unit: MetricUnit }> = {
  error_rate:   { agg: 'error_rate', metric: 'calls_total', unit: '%' },
  p99_ms:       { agg: 'p99', metric: 'duration_milliseconds_bucket', unit: 'ms' },
  request_rate: { agg: 'rate', metric: 'calls_total', unit: 'rps' },
  error_count:  { agg: 'errors', metric: 'calls_total', unit: 'count' },
};

// ConditionPreview — the prototype's live Alert-rule preview: the rule's metric
// over the last hour with a dashed threshold line + a "would have fired N×"
// count scoped to the rule's service, fed by the server resolver. Lets the
// operator tune the threshold against real data before saving.
// Split out of the Alerts.tsx monolith (v0.8.252 refactor) verbatim.
export function ConditionPreview({ draft }: { draft: Partial<AlertRule> }) {
  const metricKey = draft.metric ?? 'error_rate';
  const m = PREVIEW_METRIC[metricKey] ?? PREVIEW_METRIC.error_rate;
  const svc = draft.service ?? '';
  const threshold = Number(draft.threshold ?? 0);
  const comparator = draft.comparator ?? '>';
  const sevTone: 'warn' | 'err' = draft.severity === 'critical' ? 'err' : 'warn';

  // Fixed 1h lookback captured once → no now()-per-render refetch churn.
  const win = useMemo(() => {
    const to = Date.now() * 1_000_000;
    return { from: to - 3600 * 1_000_000_000, to };
  }, []);
  const mq = useMemo(() => metricQuery({
    source: 'spanmetrics', metric: m.metric, agg: m.agg, unit: m.unit,
    filters: svc ? { 'service.name': svc } : {},
  }), [m.metric, m.agg, m.unit, svc]);
  const q = useQuery({
    queryKey: ['alert-preview', m.agg, svc, win.from, win.to],
    queryFn: () => api.resolveMetric(mq, win),
    staleTime: 30_000,
  });

  const series = q.data?.series ?? [];
  const pts = series[0]?.points ?? [];
  const wouldFire = pts.filter(p =>
    comparator === '>' ? p.value > threshold
    : comparator === '<' ? p.value < threshold
    : comparator === '>=' ? p.value >= threshold
    : comparator === '<=' ? p.value <= threshold
    : false).length;

  return (
    <div style={{ marginTop: 12, border: '1px solid var(--border)', borderRadius: 8, background: 'var(--bg2)', padding: 12 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <span style={{ fontSize: 11, fontWeight: 700, letterSpacing: '0.5px', textTransform: 'uppercase', color: 'var(--text2)' }}>Condition preview</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>last 1h · {svc || 'all services'}</span>
        <span style={{ marginLeft: 'auto', fontSize: 12, fontWeight: 700, color: wouldFire > 0 ? `var(--${sevTone})` : 'var(--text3)' }}>
          would have fired {wouldFire}×<span style={{ fontWeight: 500, color: 'var(--text3)' }}> ({pts.length} buckets)</span>
        </span>
      </div>
      {q.isLoading ? (
        <div style={{ height: 130, display: 'grid', placeItems: 'center', color: 'var(--text3)', fontSize: 12 }}>Loading…</div>
      ) : pts.length < 2 ? (
        <div style={{ height: 130, display: 'grid', placeItems: 'center', color: 'var(--text3)', fontSize: 12 }}>
          No data for {svc || 'any service'} in the last hour
        </div>
      ) : (
        <MultiLineChart series={series} unit={m.unit} height={130}
          thresholds={[{ value: threshold, label: `${comparator} ${threshold}`, severity: sevTone }]} />
      )}
    </div>
  );
}
