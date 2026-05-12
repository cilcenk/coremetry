import { useEffect, useMemo, useState } from 'react';
import { MultiLineChart, type DeployMarker } from './MultiLineChart';
import { Spinner } from './Spinner';
import { api } from '@/lib/api';
import { useServiceDeploys, useSLOs } from '@/lib/queries';
import { timeRangeToNs } from '@/lib/utils';
import type { SpanMetricSeries, TimeRange } from '@/lib/types';

// ServiceCharts — three core trend panels for the focused
// service: throughput (RPS by operation), error rate (%) by
// operation, and P99 latency by operation. Pulls SLOs for the
// service and paints horizontal threshold lines on the
// matching panel (latency SLO → P99 panel; availability SLO →
// error rate panel). Pulls deploys for the service and paints
// dashed vertical markers on every chart so the operator can
// read "did this regression coincide with a deploy" in one
// glance.
//
// All three charts share a syncKey so hovering one paints the
// crosshair on the other two — Datadog dashboard convention,
// turns the three panels into one synchronised view.

export function ServiceCharts({ service, range }: {
  service: string;
  range: TimeRange;
}) {
  // Memoise the time bounds so a render doesn't churn the
  // query keys (same trick the Logs page uses — Date.now() in
  // timeRangeToNs makes naive use unstable).
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const [rpsSeries, setRpsSeries] = useState<SpanMetricSeries[] | null>(null);
  const [errSeries, setErrSeries] = useState<SpanMetricSeries[] | null>(null);
  const [p99Series, setP99Series] = useState<SpanMetricSeries[] | null>(null);
  const [loading, setLoading] = useState(true);

  // Compare-to-previous-period toggle. 'off' suppresses the
  // second fetch entirely; '24h' / '7d' / 'prev' (matched
  // window) all hit the same /api/spans/span-metric path with
  // shifted from/to. Persisted in localStorage so an operator
  // who likes the comparison view keeps it across reloads.
  const [compare, setCompare] = useState<CompareMode>(() => {
    try {
      const v = localStorage.getItem('svc.charts.compare') as CompareMode | null;
      if (v === '24h' || v === '7d' || v === 'prev') return v;
    } catch { /* private browsing — best-effort */ }
    return 'off';
  });
  const setCompareAndPersist = (m: CompareMode) => {
    setCompare(m);
    try { localStorage.setItem('svc.charts.compare', m); }
    catch { /* best-effort */ }
  };
  const [rpsPrev, setRpsPrev] = useState<SpanMetricSeries[] | null>(null);
  const [errPrev, setErrPrev] = useState<SpanMetricSeries[] | null>(null);
  const [p99Prev, setP99Prev] = useState<SpanMetricSeries[] | null>(null);
  const compareOffsetNs = useMemo(() => {
    switch (compare) {
      case '24h':  return 24 * 3600 * 1e9;
      case '7d':   return 7 * 24 * 3600 * 1e9;
      case 'prev': return (to - from);
      default:     return 0;
    }
  }, [compare, from, to]);
  const compareLabel = compare === '24h' ? '24h ago'
    : compare === '7d' ? '7d ago'
    : compare === 'prev' ? 'prev window' : '';

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    const dsl = `service.name = "${service.replace(/"/g, '\\"')}"`;
    Promise.all([
      api.spanMetric({ agg: 'rate',       dsl, from, to, groupBy: 'name' }),
      api.spanMetric({ agg: 'error_rate', dsl, from, to, groupBy: 'name' }),
      api.spanMetric({ agg: 'p99',        dsl, from, to, groupBy: 'name', field: 'duration_ms' }),
    ]).then(([rps, err, p99]) => {
      if (cancelled) return;
      setRpsSeries(rps ?? []);
      setErrSeries(err ?? []);
      setP99Series(p99 ?? []);
    }).catch(() => {
      if (cancelled) return;
      setRpsSeries([]); setErrSeries([]); setP99Series([]);
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => { cancelled = true; };
  }, [service, from, to]);

  // Compare fetch — only fires when toggle is on. Separate from
  // the current-period fetch so toggling compare doesn't
  // re-fetch the current metrics (which the operator is
  // already looking at).
  useEffect(() => {
    if (compare === 'off' || compareOffsetNs === 0) {
      setRpsPrev(null); setErrPrev(null); setP99Prev(null);
      return;
    }
    let cancelled = false;
    const dsl = `service.name = "${service.replace(/"/g, '\\"')}"`;
    const prevFrom = from - compareOffsetNs;
    const prevTo = to - compareOffsetNs;
    Promise.all([
      api.spanMetric({ agg: 'rate',       dsl, from: prevFrom, to: prevTo, groupBy: 'name' }),
      api.spanMetric({ agg: 'error_rate', dsl, from: prevFrom, to: prevTo, groupBy: 'name' }),
      api.spanMetric({ agg: 'p99',        dsl, from: prevFrom, to: prevTo, groupBy: 'name', field: 'duration_ms' }),
    ]).then(([rps, err, p99]) => {
      if (cancelled) return;
      setRpsPrev(rps ?? []);
      setErrPrev(err ?? []);
      setP99Prev(p99 ?? []);
    }).catch(() => {
      if (cancelled) return;
      setRpsPrev([]); setErrPrev([]); setP99Prev([]);
    });
    return () => { cancelled = true; };
  }, [service, from, to, compare, compareOffsetNs]);

  // Deploy markers for this service in the visible window.
  const deploysQ = useServiceDeploys(service, from, to);
  const deployMarkers: DeployMarker[] | undefined = useMemo(() => {
    if (!deploysQ.data) return undefined;
    return deploysQ.data.map(d => ({
      timeUnixNs: d.timeUnixNs,
      label: d.version,
      description: `${d.spanCount.toLocaleString()} spans since first seen`,
    }));
  }, [deploysQ.data]);

  // SLO-derived thresholds for this service. Latency SLOs
  // surface on the P99 panel; availability SLOs surface on
  // the error-rate panel (as the error budget %).
  const slosQ = useSLOs();
  const { latencyThresholds, errorThresholds } = useMemo(() => {
    const lat: { value: number; label: string; severity: 'warn' | 'err' }[] = [];
    const err: { value: number; label: string; severity: 'warn' | 'err' }[] = [];
    for (const slo of slosQ.data ?? []) {
      if (slo.service !== service) continue;
      // Service-wide SLOs apply on every panel; operation-
      // scoped ones still get drawn here because the panel
      // groups by operation, so the line is meaningful when
      // the matching operation's series is on screen. The
      // label includes the operation name so the operator
      // sees which series the line belongs to.
      const opSuffix = slo.operation ? ` (${slo.operation})` : '';
      if (slo.sliType === 'latency') {
        lat.push({
          value: slo.thresholdMs,
          label: `SLO < ${slo.thresholdMs}ms${opSuffix}`,
          severity: 'err',
        });
      } else if (slo.sliType === 'availability') {
        const errBudgetPct = (1 - slo.target) * 100;
        err.push({
          value: errBudgetPct,
          label: `err ≤ ${errBudgetPct.toFixed(2)}%${opSuffix}`,
          severity: 'err',
        });
      }
    }
    return {
      latencyThresholds: lat.length > 0 ? lat : undefined,
      errorThresholds:   err.length > 0 ? err : undefined,
    };
  }, [slosQ.data, service]);

  const syncKey = `service:${service}`;

  if (loading) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 14,
        minHeight: 200, display: 'grid', placeItems: 'center',
      }}>
        <Spinner />
      </div>
    );
  }

  return (
    <div style={{ marginBottom: 14 }}>
      {/* Compare-to-previous toggle row. Sits above the three
          panels so the chosen period applies to all of them
          uniformly. Dynatrace-style "previous 24h" overlay is
          off by default (no second fetch); flipping it on
          paints a dashed ghost line per chart. */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6,
        fontSize: 11, color: 'var(--text2)',
      }}>
        <span style={{
          textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 700,
        }}>Compare to:</span>
        {(['off', '24h', '7d', 'prev'] as CompareMode[]).map(m => (
          <button key={m} type="button"
            onClick={() => setCompareAndPersist(m)}
            title={m === 'off' ? 'No comparison'
              : m === 'prev' ? 'Previous window of the same length'
              : `${m} ago at the same time`}
            style={{
              all: 'unset', cursor: 'pointer',
              fontSize: 11, padding: '2px 8px', borderRadius: 3,
              fontFamily: 'ui-monospace, SFMono-Regular, monospace',
              background: compare === m ? 'var(--accent2)' : 'var(--bg2)',
              color: compare === m ? 'var(--bg)' : 'var(--text2)',
              border: `1px solid ${compare === m ? 'var(--accent2)' : 'var(--border)'}`,
              fontWeight: compare === m ? 600 : 400,
            }}>
            {m === 'off' ? 'off' : m === 'prev' ? 'prev window' : m}
          </button>
        ))}
      </div>
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)',
        gap: 10,
      }}>
        <ChartCard title="RPS by operation">
          <MultiLineChart series={rpsSeries ?? []} unit="rps"
                          height={140}
                          deploys={deployMarkers}
                          syncKey={syncKey}
                          compareSeries={rpsPrev ?? undefined}
                          compareOffsetNs={compareOffsetNs}
                          compareLabel={compareLabel} />
        </ChartCard>
        <ChartCard title="Error rate by operation">
          <MultiLineChart series={errSeries ?? []} unit="%"
                          height={140}
                          deploys={deployMarkers}
                          thresholds={errorThresholds}
                          syncKey={syncKey}
                          compareSeries={errPrev ?? undefined}
                          compareOffsetNs={compareOffsetNs}
                          compareLabel={compareLabel} />
        </ChartCard>
        <ChartCard title="P99 latency by operation">
          <MultiLineChart series={p99Series ?? []} unit="ms"
                          height={140}
                          deploys={deployMarkers}
                          thresholds={latencyThresholds}
                          syncKey={syncKey}
                          compareSeries={p99Prev ?? undefined}
                          compareOffsetNs={compareOffsetNs}
                          compareLabel={compareLabel} />
        </ChartCard>
      </div>
    </div>
  );
}

type CompareMode = 'off' | '24h' | '7d' | 'prev';

function ChartCard({ title, children }: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 10,
      minWidth: 0, // allow flex/grid children to shrink
    }}>
      <div style={{
        fontSize: 11, fontWeight: 600, color: 'var(--text2)',
        letterSpacing: '0.3px', textTransform: 'uppercase',
        marginBottom: 4,
      }}>
        {title}
      </div>
      {children}
    </div>
  );
}
