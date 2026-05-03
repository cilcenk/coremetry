'use client';
import { Suspense, useEffect, useMemo, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { FilterBuilder } from '@/components/FilterBuilder';
import { MultiLineChart } from '@/components/MultiLineChart';
import { ShareButton } from '@/components/ShareButton';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { encodeRange, decodeRange, encodeFilters, decodeFilters, buildQuery } from '@/lib/urlState';
import type { TimeRange, FilterExpr, SpanMetricSeries, SpanAgg } from '@/lib/types';

const AGG_OPTIONS: { v: SpanAgg; label: string; unit?: string }[] = [
  { v: 'count',      label: 'Count',           unit: '' },
  { v: 'rate',       label: 'Rate (per sec)',  unit: '/s' },
  { v: 'errors',     label: 'Error count',     unit: '' },
  { v: 'error_rate', label: 'Error rate (%)',  unit: '%' },
  { v: 'avg',        label: 'Avg',             unit: 'ms' },
  { v: 'p50',        label: 'P50 (median)',    unit: 'ms' },
  { v: 'p90',        label: 'P90',             unit: 'ms' },
  { v: 'p95',        label: 'P95',             unit: 'ms' },
  { v: 'p99',        label: 'P99',             unit: 'ms' },
  { v: 'p999',       label: 'P99.9',           unit: 'ms' },
  { v: 'min',        label: 'Min',             unit: 'ms' },
  { v: 'max',        label: 'Max',             unit: 'ms' },
  { v: 'sum',        label: 'Sum',             unit: 'ms' },
];

const SUGGESTED_GROUPBY = [
  'service.name', 'name', 'kind', 'status_code',
  'http.method', 'http.route', 'http.status_code',
  'db.system', 'rpc.method', 'peer.service',
  'resource.host.name', 'resource.deployment.environment',
];

const STEP_OPTIONS = [
  { v: 0,   label: 'Auto' },
  { v: 10,  label: '10 s' },
  { v: 30,  label: '30 s' },
  { v: 60,  label: '1 min' },
  { v: 300, label: '5 min' },
  { v: 1800, label: '30 min' },
];

function ExploreInner() {
  const router = useRouter();
  const searchParams = useSearchParams();

  // ── State, hydrated from URL on first render ─────────────────────────────
  const [range, setRange] = useState<TimeRange>(
    () => decodeRange(searchParams.get('range'), { preset: '1h' }));
  const [filters, setFilters] = useState<FilterExpr[]>(
    () => decodeFilters(searchParams.get('filters')));
  const [agg, setAgg] = useState<SpanAgg>(
    () => (searchParams.get('agg') as SpanAgg) || 'count');
  const [field, setField] = useState(searchParams.get('field') ?? 'duration_ms');
  const [groupBy, setGroupBy] = useState<string[]>(
    () => (searchParams.get('groupBy') ?? '').split(',').filter(Boolean));
  const [groupDraft, setGroupDraft] = useState('');
  const [step, setStep] = useState(parseInt(searchParams.get('step') ?? '0', 10) || 0);
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  const [services, setServices] = useState<string[]>([]);

  // Advanced query mode + DSL textarea
  const [mode, setMode] = useState<'builder' | 'advanced'>(
    () => (searchParams.get('mode') === 'advanced' ? 'advanced' : 'builder'));
  const [dsl, setDsl] = useState(searchParams.get('dsl') ?? '');
  const [queryError, setQueryError] = useState<string | null>(null);

  // ── State → URL (replaceState — keeps history clean) ─────────────────────
  useEffect(() => {
    const qs = buildQuery([
      ['agg',     agg !== 'count' ? agg : ''],
      ['field',   field !== 'duration_ms' ? field : ''],
      ['groupBy', groupBy.join(',')],
      ['filters', mode === 'builder' ? encodeFilters(filters) : ''],
      ['dsl',     mode === 'advanced' ? dsl : ''],
      ['mode',    mode === 'advanced' ? 'advanced' : ''],
      ['range',   encodeRange(range)],
      ['step',    step || ''],
    ]);
    const next = qs ? `?${qs}` : '';
    if (next !== window.location.search) {
      router.replace(`/explore${next}`, { scroll: false });
    }
  }, [agg, field, groupBy, filters, dsl, mode, range, step, router]);

  // Load service options for filter value suggestions
  useEffect(() => {
    api.services(timeRangeToNs(range))
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => setServices([]));
  }, [range]);

  // Run query whenever inputs change (debounce skipped — small payload)
  useEffect(() => {
    setSeries(undefined);
    setQueryError(null);
    const { from, to } = timeRangeToNs(range);
    api.spanMetric({
      agg, field,
      groupBy: groupBy.join(',') || undefined,
      filters: mode === 'builder' && filters.length ? JSON.stringify(filters) : undefined,
      dsl:     mode === 'advanced' && dsl.trim() ? dsl : undefined,
      from, to,
      step: step || undefined,
    })
      .then(r => setSeries(r ?? []))
      .catch(err => {
        setSeries(null);
        const msg = String(err?.message ?? err);
        setQueryError(msg.includes('DSL') ? msg : null);
      });
  }, [range, filters, dsl, mode, agg, field, groupBy, step]);

  const aggMeta = AGG_OPTIONS.find(o => o.v === agg)!;
  const unit = aggMeta.unit ?? '';
  const totalSeries = series?.length ?? 0;
  const totalPoints = series?.reduce((n, s) => n + s.points.length, 0) ?? 0;

  const addGroupKey = (k: string) => {
    const t = k.trim();
    if (!t || groupBy.includes(t)) return;
    setGroupBy([...groupBy, t]);
    setGroupDraft('');
  };
  const removeGroupKey = (k: string) =>
    setGroupBy(groupBy.filter(x => x !== k));

  // Quick stats per series for the summary table
  const summary = useMemo(() => {
    if (!series) return [];
    return series.map(s => {
      const vals = s.points.map(p => p.value).filter(v => v != null && !isNaN(v));
      if (vals.length === 0) return { key: s.groupKey, count: 0, last: 0, max: 0, avg: 0 };
      const sum = vals.reduce((a, b) => a + b, 0);
      return {
        key: s.groupKey,
        count: vals.length,
        last: vals[vals.length - 1],
        max: Math.max(...vals),
        avg: sum / vals.length,
      };
    });
  }, [series]);

  return (
    <>
      <Topbar title="Explore" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ fontSize: 12, color: 'var(--text2)', flex: 1 }}>
            Build span metrics on the fly — filter spans, pick an aggregation, optionally split by attributes.
          </span>
          <ShareButton />
        </div>

        {/* Aggregation + field row */}
        <div className="controls">
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Aggregation:</span>
          <select value={agg} onChange={e => setAgg(e.target.value as SpanAgg)}>
            {AGG_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
          </select>
          {needsField(agg) && (
            <>
              <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>of:</span>
              <Combobox value={field} onChange={setField}
                options={['duration_ms', 'duration_s', 'http.status_code', '1']}
                placeholder="duration_ms" width={170} />
            </>
          )}
          <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 4 }}>Step:</span>
          <select value={step} onChange={e => setStep(Number(e.target.value))}>
            {STEP_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
          </select>
        </div>

        {/* Mode toggle: Builder ⇄ Advanced */}
        <div className="controls" style={{ marginBottom: 6 }}>
          <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
            <button onClick={() => setMode('builder')}
              className={mode === 'builder' ? '' : 'sec'}
              style={{ borderRadius: 0, borderRight: '1px solid var(--border)' }}>
              Builder
            </button>
            <button onClick={() => setMode('advanced')}
              className={mode === 'advanced' ? '' : 'sec'}
              style={{ borderRadius: 0 }}>
              Advanced query
            </button>
          </div>
          {mode === 'advanced' && (
            <span style={{ color: 'var(--text2)', fontSize: 11 }}>
              One condition per line · operators: <code>=</code> <code>!=</code> <code>&gt;</code> <code>&gt;=</code> <code>&lt;</code> <code>&lt;=</code> <code>~</code> <code>!~</code> <code>in [a,b]</code> <code>exists</code>
            </span>
          )}
        </div>

        {mode === 'builder' && (
          <FilterBuilder value={filters} onChange={setFilters}
            suggestedValues={{
              'service.name': services,
              'resource.service.name': services,
              'kind': ['internal', 'server', 'client', 'producer', 'consumer'],
              'status_code': ['ok', 'error', 'unset'],
              'http.method': ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
              'db.system': ['postgresql', 'mysql', 'redis', 'mongodb', 'elasticsearch'],
            }} />
        )}

        {mode === 'advanced' && (
          <div className="adv-query">
            <textarea value={dsl}
              onChange={e => setDsl(e.target.value)}
              spellCheck={false}
              placeholder={`# Examples — one condition per line
duration > 500ms
service.name = "frontend"
http.status_code >= 500
status_code = error
peer.service = "payment-service"
db.system in [postgresql, redis]
exception.type exists
name ~ checkout`}
              rows={Math.max(6, dsl.split('\n').length + 1)} />
            {queryError && <div className="trp-error" style={{ marginTop: 6 }}>{queryError}</div>}
            <div style={{ marginTop: 4, fontSize: 11, color: 'var(--text3)' }}>
              Conditions are AND-joined · prefix with <code>resource.</code> or <code>span.</code> to scope ·
              <code>duration</code> accepts <code>500ms</code>, <code>1.5s</code>, <code>2m</code>
            </div>
          </div>
        )}

        {/* Group by */}
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
          {groupDraft && (
            <button className="sec" onClick={() => addGroupKey(groupDraft)}>Add</button>
          )}
        </div>

        {/* Chart */}
        {series === undefined && <Spinner />}
        {series && series.length === 0 && (
          <Empty icon="◎" title="No data for this query">
            Try a wider time range, fewer filters, or remove split keys.
          </Empty>
        )}
        {series && series.length > 0 && (
          <>
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14,
            }}>
              <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 8 }}>
                <b style={{ color: 'var(--accent2)' }}>{aggMeta.label}</b>
                {needsField(agg) && <> of <b style={{ color: 'var(--accent2)' }}>{field}</b></>}
                {groupBy.length > 0 && <> · split by <b style={{ color: 'var(--accent2)' }}>{groupBy.join(' / ')}</b></>}
                {' · '}{totalSeries} series · {totalPoints} points
              </div>
              <MultiLineChart series={series} unit={unit} />
            </div>

            {/* Per-series summary */}
            {groupBy.length > 0 && summary.length > 1 && (
              <div className="table-wrap" style={{ marginTop: 14 }}>
                <table>
                  <thead>
                    <tr>
                      <th>Series</th>
                      <th style={{ textAlign: 'right' }}>Last</th>
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

function needsField(agg: SpanAgg): boolean {
  return !['count', 'rate', 'errors', 'error_rate'].includes(agg);
}

export default function ExplorePage() {
  return (
    <Suspense fallback={<Spinner />}>
      <ExploreInner />
    </Suspense>
  );
}
