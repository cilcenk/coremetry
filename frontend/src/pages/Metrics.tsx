import { useEffect, useMemo, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { ServicePicker } from '@/components/ServicePicker';
import { MetricNamePicker } from '@/components/MetricNamePicker';
import { FilterBuilder } from '@/components/FilterBuilder';
import { MultiLineChart } from '@/components/MultiLineChart';
import { ShareButton } from '@/components/ShareButton';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import type { Service, MetricInfo, SpanMetricSeries, FilterExpr, TimeRange } from '@/lib/types';

const AGG_OPTIONS = [
  { v: 'avg',  label: 'Average' },
  { v: 'sum',  label: 'Sum' },
  { v: 'last', label: 'Last' },
  { v: 'min',  label: 'Min' },
  { v: 'max',  label: 'Max' },
  { v: 'p50',  label: 'P50' },
  { v: 'p95',  label: 'P95' },
  { v: 'p99',  label: 'P99' },
];

// v0.5.259 — sub-10s steps for short-window deep-dives. 1s + 5s
// expose the OTel point-precision metrics actually carry — at
// ingest rate ~10/sec per metric we were artificially folding
// 10 samples into one bucket on a 10min view.
const STEP_OPTIONS = [
  { v: 0,    label: 'Auto' },
  { v: 1,    label: '1 s' },
  { v: 5,    label: '5 s' },
  { v: 10,   label: '10 s' },
  { v: 30,   label: '30 s' },
  { v: 60,   label: '1 min' },
  { v: 300,  label: '5 min' },
  { v: 1800, label: '30 min' },
];

const SUGGESTED_GROUPBY = [
  'host.name', 'service.name',
  'resource.host.name', 'resource.deployment.environment',
  'resource.service.instance.id', 'resource.k8s.pod.name', 'resource.k8s.node.name',
  'http.method', 'http.route', 'http.status_code',
  'db.system',
];

export default function MetricsPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  // v0.5.198 — `services` eager cache dropped. FilterBuilder
  // server-fetches service.name values via /api/attribute-values?q=
  // when the operator types in the value field; the ServicePicker
  // up top is already debounced server-side. The eager all-services
  // load at billion-span scale was the page's biggest TTFI cost.
  // Metadata for the currently-picked metric (unit, type,
  // description). Populated by MetricNamePicker's onPick
  // callback so we don't have to eager-fetch the entire metric
  // catalogue just to display "ms · gauge · request_duration".
  // v0.5.181 — previously this was derived from a full
  // metricNames[] list eager-loaded on mount, which at 10k+
  // metrics dominated the page's TTFI.
  const [currentMeta, setCurrentMeta] = useState<MetricInfo | null>(null);
  const [service, setService] = useState('');
  const [metric, setMetric] = useState('');
  const [agg, setAgg] = useState('avg');
  const [step, setStep] = useState(0);
  const [filters, setFilters] = useState<FilterExpr[]>([]);
  const [groupBy, setGroupBy] = useState<string[]>([]);
  const [groupDraft, setGroupDraft] = useState('');
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);

  // Suggestion sources for filters and group-by
  const [hostValues, setHostValues] = useState<string[]>([]);
  const [instanceValues, setInstanceValues] = useState<string[]>([]);

  // Service swap → metric becomes stale. Clear the selection so
  // the operator picks fresh from the new service's catalog
  // rather than seeing data for the wrong service via a metric
  // that doesn't apply.
  useEffect(() => {
    setMetric('');
    setCurrentMeta(null);
  }, [service]); // eslint-disable-line react-hooks/exhaustive-deps

  // Pull dimension values for the current metric (host / instance) for combobox
  useEffect(() => {
    if (!metric) { setHostValues([]); setInstanceValues([]); return; }
    api.metricLabels(metric, 'host.name').then(v => setHostValues(v ?? [])).catch(() => {});
    api.metricLabels(metric, 'resource.service.instance.id').then(v => setInstanceValues(v ?? [])).catch(() => {});
  }, [metric]);

  useEffect(() => {
    if (!metric) { setSeries(null); return; }
    setSeries(undefined);
    const { from, to } = timeRangeToNs(range);
    api.metricQuery({
      name: metric,
      service: service || undefined,
      agg,
      filters: filters.length ? JSON.stringify(filters) : undefined,
      groupBy: groupBy.join(',') || undefined,
      from, to, step: step || undefined,
    }).then(r => setSeries(r ?? [])).catch(() => setSeries(null));
  }, [metric, service, agg, filters, groupBy, step, range]);

  // Display meta only when the picker handed us metadata for
  // the currently-typed metric — typing an arbitrary name
  // before picking shows no meta, which is the right signal.
  const meta = currentMeta && currentMeta.name === metric ? currentMeta : null;
  const unit = meta?.unit ?? '';

  const addGroupKey = (k: string) => {
    const t = k.trim();
    if (!t || groupBy.includes(t)) return;
    setGroupBy([...groupBy, t]);
    setGroupDraft('');
  };
  const removeGroupKey = (k: string) => setGroupBy(groupBy.filter(x => x !== k));

  // Per-series stats for the legend table
  const summary = useMemo(() => (series ?? []).map(s => {
    const vs = s.points.map(p => p.value).filter(v => v != null && !isNaN(v));
    if (vs.length === 0) return { key: s.groupKey, last: 0, avg: 0, max: 0, min: 0, count: 0 };
    return {
      key: s.groupKey,
      last: vs[vs.length - 1],
      avg: vs.reduce((a, b) => a + b, 0) / vs.length,
      max: Math.max(...vs),
      min: Math.min(...vs),
      count: vs.length,
    };
  }), [series]);

  return (
    <>
      <Topbar title="Metrics" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ fontSize: 12, color: 'var(--text2)', flex: 1 }}>
            Pick a metric, slice by service / host / instance, or split by any attribute.
          </span>
          <ShareButton />
        </div>

        {/* Metric + service + agg + step */}
        <div className="controls">
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Service:</span>
          <ServicePicker value={service} onChange={setService}
            placeholder="(all)" width={170} />
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Metric:</span>
          <MetricNamePicker service={service} value={metric}
            onChange={setMetric}
            onPick={setCurrentMeta}
            placeholder="select metric…" width={280} />
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Agg:</span>
          <select value={agg} onChange={e => setAgg(e.target.value)}>
            {AGG_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
          </select>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Step:</span>
          <select value={step} onChange={e => setStep(Number(e.target.value))}>
            {STEP_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
          </select>
          {meta && (
            <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
              {meta.type}{meta.unit && ` · ${meta.unit}`}{meta.description && ` · ${meta.description}`}
            </span>
          )}
        </div>

        {/* Attribute filters */}
        <FilterBuilder value={filters} onChange={setFilters}
          suggestedValues={{
            'host.name':      hostValues,
            'resource.host.name': hostValues,
            'resource.service.instance.id': instanceValues,
            'resource.deployment.environment': ['production', 'demo', 'staging'],
            'http.method':    ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
          }} />

        {/* Split by */}
        <div className="controls" style={{ marginBottom: 14 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Split by:</span>
          {groupBy.length === 0 && (
            <span style={{ color: 'var(--text3)', fontSize: 12, fontStyle: 'italic' }}>
              (single line — add attributes to break down)
            </span>
          )}
          {groupBy.map(k => (
            <span key={k} className="fb-chip">
              <b>{k}</b>
              <button className="fb-chip-x" type="button"
                onClick={() => removeGroupKey(k)} aria-label="Remove">✕</button>
            </span>
          ))}
          <Combobox value={groupDraft} onChange={setGroupDraft}
            options={SUGGESTED_GROUPBY.filter(k => !groupBy.includes(k))}
            placeholder="+ split key" width={200}
            onEnter={() => addGroupKey(groupDraft)} />
          {groupDraft && <button className="sec" onClick={() => addGroupKey(groupDraft)}>Add</button>}
        </div>

        {!metric && (
          <Empty icon="∿" title="Select a metric to begin">
            Use the Metric dropdown above. <code>OTEL_EXPORTER_OTLP_ENDPOINT</code> apps push here.
          </Empty>
        )}
        {metric && series === undefined && <Spinner />}
        {metric && series && series.length === 0 && (
          <Empty icon="∿" title="No data points for this query">
            Try a wider time range, fewer filters, or remove split keys.
          </Empty>
        )}
        {metric && series && series.length > 0 && (
          <>
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14,
            }}>
              <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 8 }}>
                <b style={{ color: 'var(--accent2)' }}>{agg}</b> of{' '}
                <b style={{ color: 'var(--accent2)' }}>{metric}</b>
                {service && <> · service <b>{service}</b></>}
                {groupBy.length > 0 && <> · split by <b>{groupBy.join(' / ')}</b></>}
                {' · '}{series.length} series
              </div>
              <MultiLineChart series={series} unit={unit} />
            </div>

            {groupBy.length > 0 && summary.length > 1 && (
              <div className="table-wrap" style={{ marginTop: 14 }}>
                <table>
                  <thead>
                    <tr>
                      <th>Series</th>
                      <th style={{ textAlign: 'right' }}>Last</th>
                      <th style={{ textAlign: 'right' }}>Min</th>
                      <th style={{ textAlign: 'right' }}>Avg</th>
                      <th style={{ textAlign: 'right' }}>Max</th>
                      <th style={{ textAlign: 'right' }}>Buckets</th>
                    </tr>
                  </thead>
                  <tbody>
                    {summary.map((row, i) => (
                      <tr key={i}>
                        <td><b>{row.key.join(' / ') || '(all)'}</b></td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.last.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.min.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.avg.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{row.max.toFixed(2)}{unit}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(row.count)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
    </>
  );
}
