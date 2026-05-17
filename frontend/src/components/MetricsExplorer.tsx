import { useEffect, useState } from 'react';
import { ExploreViz, type ExploreVizKind } from './ExploreViz';
import { Spinner } from './Spinner';
import { ServicePicker } from './ServicePicker';
import { MetricNamePicker } from './MetricNamePicker';
import { Combobox } from './Combobox';
import { api } from '@/lib/api';
import { timeRangeToNs } from '@/lib/utils';
import type { ExploreSeries, MetricInfo, TimeRange } from '@/lib/types';

// MetricsExplorer is the Metrics source on /explore. Picks any
// metric name from the autocompleted list of what's actually
// flowing for the selected service (or all services), then
// renders the time-series via the chosen viz. Compare-period
// overlays the previous window as faded twin series.
export function MetricsExplorer({ range, viz, compare, initialService = '', initialMetric = '' }: {
  range: TimeRange;
  viz: ExploreVizKind;
  compare: boolean;
  // When the explorer is opened from a deep-link (infra tile,
  // anomaly card, etc.) these pre-fill the picker so the chart
  // renders immediately. Empty defaults preserve the standalone
  // "operator picks from scratch" flow.
  initialService?: string;
  initialMetric?: string;
}) {
  const [service, setService]         = useState(initialService);
  const [metric, setMetric]           = useState(initialMetric);
  // Metadata for the picked metric (v0.5.181) — populated by
  // MetricNamePicker's onPick callback. Replaces the prior
  // eager-loaded names[] catalogue that dominated TTFI at 10k+
  // metrics scale.
  const [currentMeta, setCurrentMeta] = useState<MetricInfo | null>(null);
  const [series, setSeries]           = useState<ExploreSeries[] | null | undefined>(undefined);

  useEffect(() => {
    // Service swap → metric becomes stale; clear it so the
    // operator re-picks against the new service's catalogue.
    setMetric('');
    setCurrentMeta(null);
  }, [service]);

  useEffect(() => {
    if (!metric) {
      setSeries([]);
      return;
    }
    setSeries(undefined);
    const { from, to } = timeRangeToNs(range);

    const fetchOne = (fromNs: number, toNs: number) => api.metrics({
      name: metric,
      service: service || undefined,
      from: fromNs, to: toNs,
      limit: 1000,
    });

    Promise.all([
      fetchOne(from, to),
      compare ? fetchOne(from - (to - from), from) : Promise.resolve([]),
    ]).then(([cur, prev]) => {
      const out: ExploreSeries[] = [];
      // Group raw points by their attrs string so each unique
      // label tuple becomes one series.
      const byAttrs = new Map<string, { t: number; v: number }[]>();
      (cur ?? []).forEach(p => {
        const key = p.attrs || metric;
        const list = byAttrs.get(key) ?? [];
        list.push({ t: p.time, v: p.value });
        byAttrs.set(key, list);
      });
      byAttrs.forEach((pts, name) => {
        out.push({ name, points: pts.sort((a, b) => a.t - b.t) });
      });
      if (compare && prev) {
        const shift = to - from;
        const prevByAttrs = new Map<string, { t: number; v: number }[]>();
        prev.forEach(p => {
          const key = p.attrs || metric;
          const list = prevByAttrs.get(key) ?? [];
          list.push({ t: p.time + shift, v: p.value });
          prevByAttrs.set(key, list);
        });
        prevByAttrs.forEach((pts, name) => {
          out.push({ name: `${name} (prev)`, points: pts.sort((a, b) => a.t - b.t) });
        });
      }
      setSeries(out);
    }).catch(() => setSeries(null));
  }, [metric, service, range, compare]);

  const unit = currentMeta && currentMeta.name === metric ? currentMeta.unit : undefined;

  return (
    <>
      <div className="controls">
        <ServicePicker value={service} onChange={setService}
          placeholder="Service (all)…" width={200} />
        <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>
          Metric:
        </span>
        <MetricNamePicker service={service} value={metric}
          onChange={setMetric}
          onPick={setCurrentMeta}
          placeholder="Pick a metric…" width={300} />
        {unit && <span style={{ color: 'var(--text3)', fontSize: 11 }}>unit: {unit}</span>}
      </div>

      {series === undefined && <Spinner />}
      {series === null && (
        <div style={{ color: 'var(--err)', fontSize: 12, padding: '12px 4px' }}>
          Failed to load metric.
        </div>
      )}
      {series && metric && <ExploreViz series={series} kind={viz} unit={unit} />}
      {series && !metric && (
        <div style={{ color: 'var(--text3)', fontSize: 12, padding: '12px 4px' }}>
          Pick a metric to chart.
        </div>
      )}
    </>
  );
}
