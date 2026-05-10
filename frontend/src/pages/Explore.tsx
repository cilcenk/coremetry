import { Suspense, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { FilterBuilder } from '@/components/FilterBuilder';
import { MultiLineChart } from '@/components/MultiLineChart';
import { ShareButton } from '@/components/ShareButton';
import { LogsExplorer } from '@/components/LogsExplorer';
import { MetricsExplorer } from '@/components/MetricsExplorer';
import { ColumnManager } from '@/components/ColumnManager';
import { api } from '@/lib/api';
import { useExemplarFetcher, useServiceDeploys } from '@/lib/queries';
import { timeRangeToNs, fmtNum, tsLong, rowClickHandlers } from '@/lib/utils';
import { encodeRange, decodeRange, encodeFilters, decodeFilters, buildQuery } from '@/lib/urlState';
import type { TimeRange, FilterExpr, SpanMetricSeries, SpanAgg, TraceRow } from '@/lib/types';

type ResultMode = 'metric' | 'traces';
type TraceSortKey = 'traceId' | 'rootName' | 'serviceName' | 'duration' | 'spans' | 'time' | 'status';

// Each column's natural starting direction when first selected: time
// and duration are most-recent / slowest-first (descending), others
// alphabetical ascending. Matches the convention on /traces and /services.
const TRACE_SORT_NATURAL: Record<TraceSortKey, 'asc' | 'desc'> = {
  traceId: 'asc', rootName: 'asc', serviceName: 'asc',
  duration: 'desc', spans: 'desc', time: 'desc', status: 'desc',
};

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

type Source = 'spans' | 'metrics' | 'logs';
type Viz = 'line' | 'bar' | 'topN' | 'kpi';

function ExploreInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  // Data source tab — Spans is the rich legacy workspace
  // (filters / aggregation / split-by / traces table). Metrics
  // and Logs are simpler dedicated panels with the same range
  // + viz picker on top so the operator can switch context
  // without retyping. Persisted in the URL as ?source=… so a
  // saved view restores the chosen source.
  const [source, setSource] = useState<Source>(() => {
    const v = searchParams.get('source');
    return v === 'metrics' || v === 'logs' ? v : 'spans';
  });
  // Visualization picker — applies to the current source's
  // result. Spans source ignores it for the traces result mode.
  const [viz, setViz] = useState<Viz>(() => {
    const v = searchParams.get('viz') as Viz;
    return ['line', 'bar', 'topN', 'kpi'].includes(v) ? v : 'line';
  });
  const [compare, setCompare] = useState(searchParams.get('compare') === 'true');

  // ── State, hydrated from URL on first render ─────────────────────────────
  const [range, setRange] = useState<TimeRange>(
    () => decodeRange(searchParams.get('range'), { preset: '15m' }));
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

  // Result mode: aggregated metrics chart, OR raw matching trace list.
  // Same filter/DSL drives both — different backend endpoint per mode.
  const [resultMode, setResultMode] = useState<ResultMode>(
    () => (searchParams.get('result') === 'traces' ? 'traces' : 'metric'));
  const [traces, setTraces] = useState<TraceRow[] | null | undefined>(undefined);
  // Client-side sort for the traces result table — page-size is small
  // (default 50, max 500) so we don't need a server roundtrip per click.
  const [traceSort, setTraceSort] = useState<TraceSortKey>('time');
  const [traceSortDir, setTraceSortDir] = useState<'asc' | 'desc'>('desc');
  const [traceTotal, setTraceTotal] = useState(0);
  const [traceLimit, setTraceLimit] = useState(
    () => parseInt(searchParams.get('limit') ?? '50', 10) || 50);
  // User-selected attribute columns for the traces result table.
  // Mirrors the /traces page: 1-line header chip + per-row mono cell.
  // Persisted to URL as ?cols=key1,key2 so saved Explore queries
  // restore the same column set; bounded to 8 server-side.
  const [extraCols, setExtraCols] = useState<string[]>(
    () => (searchParams.get('cols') ?? '').split(',').map(s => s.trim()).filter(Boolean));

  // Advanced query mode + DSL textarea
  const [mode, setMode] = useState<'builder' | 'advanced'>(
    () => (searchParams.get('mode') === 'advanced' ? 'advanced' : 'builder'));
  const [dsl, setDsl] = useState(searchParams.get('dsl') ?? '');
  const [queryError, setQueryError] = useState<string | null>(null);

  // Exemplar lookup — picks a representative trace for the
  // current filter window so the user can drill from "this
  // P99 spike" straight into the trace that landed at it.
  // Only enabled when the filter pins a single service (the
  // backend requires service for performant lookup; without
  // it we'd be querying every row in the window).
  const exemplarFetch = useExemplarFetcher();
  const [exemplarBusy, setExemplarBusy] = useState<'slow' | 'error' | null>(null);
  const [exemplarMsg, setExemplarMsg] = useState<string | null>(null);
  const exemplarCtx = useMemo(() => extractExemplarCtx(filters, mode), [filters, mode]);

  // Deploy markers — fetched only when the filter pins a
  // single service (same gating as exemplars; the lookup is
  // service-scoped). The MultiLineChart paints dashed vertical
  // lines at every deploy time so the operator can spot a
  // regression that coincides with a code ship.
  const exploreRange = timeRangeToNs(range);
  const deploysQ = useServiceDeploys(
    exemplarCtx?.service ?? '',
    exploreRange.from,
    exploreRange.to,
  );
  const deployMarkers = useMemo(() => {
    if (!deploysQ.data) return undefined;
    return deploysQ.data.map(d => ({
      timeUnixNs: d.timeUnixNs,
      label: d.version,
      description: `${d.spanCount.toLocaleString()} spans since first seen`,
    }));
  }, [deploysQ.data]);

  async function openExemplar(kind: 'slow' | 'error') {
    if (!exemplarCtx) return;
    const { from, to } = timeRangeToNs(range);
    setExemplarBusy(kind);
    setExemplarMsg(null);
    try {
      const ex = await exemplarFetch({
        service: exemplarCtx.service,
        op: exemplarCtx.op,
        from, to, kind,
      });
      if (!ex) {
        setExemplarMsg(kind === 'error'
          ? 'No error trace in this window.'
          : 'No matching trace in this window.');
        return;
      }
      navigate(`/trace?id=${encodeURIComponent(ex.traceId)}#span=${encodeURIComponent(ex.spanId)}`);
    } catch {
      setExemplarMsg('Lookup failed — try a wider time range.');
    } finally {
      setExemplarBusy(null);
    }
  }

  // ── State → URL (replaceState — keeps history clean) ─────────────────────
  useEffect(() => {
    const qs = buildQuery([
      ['source',  source !== 'spans' ? source : ''],
      ['viz',     viz !== 'line' ? viz : ''],
      ['compare', compare ? 'true' : ''],
      ['result',  resultMode === 'traces' ? 'traces' : ''],
      ['agg',     resultMode === 'metric' && agg !== 'count' ? agg : ''],
      ['field',   resultMode === 'metric' && field !== 'duration_ms' ? field : ''],
      ['groupBy', resultMode === 'metric' ? groupBy.join(',') : ''],
      ['filters', mode === 'builder' ? encodeFilters(filters) : ''],
      ['dsl',     mode === 'advanced' ? dsl : ''],
      ['mode',    mode === 'advanced' ? 'advanced' : ''],
      ['range',   encodeRange(range)],
      ['step',    resultMode === 'metric' && step ? step : ''],
      ['limit',   resultMode === 'traces' && traceLimit !== 50 ? traceLimit : ''],
      ['cols',    resultMode === 'traces' ? extraCols.join(',') : ''],
    ]);
    const next = qs ? `?${qs}` : '';
    if (next !== window.location.search) {
      navigate(`/explore${next}`, { preventScrollReset: true, replace: true });
    }
  }, [source, viz, compare, resultMode, agg, field, groupBy, filters, dsl, mode, range, step, traceLimit, extraCols, navigate]);

  // Load service options for filter value suggestions
  useEffect(() => {
    api.services(timeRangeToNs(range))
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => setServices([]));
  }, [range]);

  // Run query whenever inputs change (debounce skipped — small payload)
  useEffect(() => {
    setQueryError(null);
    const { from, to } = timeRangeToNs(range);
    const filterArg = mode === 'builder' && filters.length ? JSON.stringify(filters) : undefined;
    const dslArg    = mode === 'advanced' && dsl.trim() ? dsl : undefined;

    if (resultMode === 'metric') {
      setSeries(undefined);
      api.spanMetric({
        agg, field,
        groupBy: groupBy.join(',') || undefined,
        filters: filterArg, dsl: dslArg,
        from, to,
        step: step || undefined,
      })
        .then(r => setSeries(r ?? []))
        .catch(err => {
          setSeries(null);
          const msg = String(err?.message ?? err);
          setQueryError(msg.includes('DSL') ? msg : null);
        });
    } else {
      // Traces mode — same filters/DSL feed the trace search instead.
      setTraces(undefined);
      api.traces({
        filters: filterArg, dsl: dslArg,
        from, to,
        sort: 'time', order: 'desc',
        limit: traceLimit,
        extraAttrs: extraCols.length ? extraCols.join(',') : undefined,
      })
        .then(r => { setTraces(r.traces ?? []); setTraceTotal(r.total ?? 0); })
        .catch(err => {
          setTraces(null);
          const msg = String(err?.message ?? err);
          setQueryError(msg.includes('DSL') ? msg : null);
        });
    }
  }, [resultMode, range, filters, dsl, mode, agg, field, groupBy, step, traceLimit, extraCols]);

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
  // Sorted view of the trace results — pure client-side because the
  // page is bounded (default 50, hard max 500). Avoids a server
  // round-trip per header click.
  const sortedTraces = useMemo(() => {
    if (!traces) return traces;
    const cmp = (a: TraceRow, b: TraceRow): number => {
      switch (traceSort) {
        case 'traceId':     return a.traceId.localeCompare(b.traceId);
        case 'rootName':    return (a.rootName || '').localeCompare(b.rootName || '');
        case 'serviceName': return a.serviceName.localeCompare(b.serviceName);
        case 'duration':    return a.durationMs - b.durationMs;
        case 'spans':       return a.spanCount - b.spanCount;
        case 'time':        return a.startTime - b.startTime;
        case 'status':      return Number(a.hasError) - Number(b.hasError);
      }
    };
    const arr = [...traces].sort(cmp);
    return traceSortDir === 'desc' ? arr.reverse() : arr;
  }, [traces, traceSort, traceSortDir]);

  const toggleTraceSort = (col: TraceSortKey) => {
    if (traceSort === col) setTraceSortDir(d => d === 'desc' ? 'asc' : 'desc');
    else { setTraceSort(col); setTraceSortDir(TRACE_SORT_NATURAL[col]); }
  };

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
        {/* Source tabs — Spans (rich legacy workspace), Metrics
            (raw OTel metric_points + label split-by), Logs
            (timeseries from CH or external ES). All three share
            the page's range + viz picker. */}
        <div className="controls" style={{ marginBottom: 6 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Source:</span>
          <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
            {(['spans', 'metrics', 'logs'] as Source[]).map(s => (
              <button key={s} onClick={() => setSource(s)}
                className={source === s ? '' : 'sec'}
                style={{
                  borderRadius: 0,
                  borderRight: s !== 'logs' ? '1px solid var(--border)' : 'none',
                  textTransform: 'capitalize',
                }}>
                {s}
              </button>
            ))}
          </div>
          <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 8 }}>Viz:</span>
          <select value={viz} onChange={e => setViz(e.target.value as Viz)}>
            <option value="line">Line</option>
            <option value="bar">Bar</option>
            <option value="topN">Top-N</option>
            <option value="kpi">KPI</option>
          </select>
          <label style={{ display: 'flex', alignItems: 'center', gap: 5,
                          color: 'var(--text2)', cursor: 'pointer', fontSize: 12, marginLeft: 8 }}
            title="Overlay the previous window of the same length as faded twin series">
            <input type="checkbox" checked={compare}
              onChange={e => setCompare(e.target.checked)} />
            Compare to previous period
          </label>
          <span style={{ flex: 1 }} />
          <ShareButton />
        </div>

        {/* Metrics + Logs source panels render their own
            workspace + viz; Spans keeps its full legacy UI
            below this fork. */}
        {source === 'metrics' && (
          <MetricsExplorer range={range} viz={viz} compare={compare}
            initialService={searchParams.get('service') ?? ''}
            initialMetric={searchParams.get('metric') ?? ''} />
        )}
        {source === 'logs' && (
          <LogsExplorer range={range} viz={viz} compare={compare} />
        )}
        {source !== 'spans' && null}

        {source === 'spans' && (<>

        <div style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ fontSize: 12, color: 'var(--text2)', flex: 1 }}>
            {resultMode === 'metric'
              ? 'Build span metrics on the fly — filter spans, pick an aggregation, optionally split by attributes.'
              : 'Search raw traces with the same filter / DSL — click a row to open the waterfall.'}
          </span>
        </div>

        {/* Result mode toggle: Metric chart ⇄ Trace list (same filters drive both) */}
        <div className="controls" style={{ marginBottom: 6 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Show:</span>
          <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
            <button onClick={() => setResultMode('traces')}
              className={resultMode === 'traces' ? '' : 'sec'}
              style={{ borderRadius: 0, borderRight: '1px solid var(--border)' }}>
              ⋮ Traces
            </button>
            <button onClick={() => setResultMode('metric')}
              className={resultMode === 'metric' ? '' : 'sec'}
              style={{ borderRadius: 0 }}>
              ∿ Metric
            </button>
          </div>
        </div>

        {/* Aggregation + field row — only in metric mode */}
        {resultMode === 'metric' && (
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
        )}

        {resultMode === 'traces' && (
          <div className="controls">
            <span style={{ color: 'var(--text2)', fontSize: 12 }}>Limit:</span>
            <select value={traceLimit} onChange={e => setTraceLimit(Number(e.target.value))}>
              {[20, 50, 100, 200, 500].map(n => <option key={n} value={n}>{n} traces</option>)}
            </select>
            <span style={{ color: 'var(--text2)', fontSize: 12, marginLeft: 'auto' }}>
              Sorted by start time desc
            </span>
          </div>
        )}

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

        {/* Group by — only meaningful for metric mode */}
        {resultMode === 'metric' && (
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
        )}

        {/* ── Metric mode: chart + per-series summary ─────────────────────────── */}
        {resultMode === 'metric' && series === undefined && <Spinner />}
        {resultMode === 'metric' && series && series.length === 0 && (
          <Empty icon="◎" title="No data for this query">
            Try a wider time range, fewer filters, or remove split keys.
          </Empty>
        )}
        {resultMode === 'metric' && series && series.length > 0 && (
          <>
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14,
            }}>
              <div style={{
                display: 'flex', alignItems: 'center', gap: 8,
                fontSize: 11, color: 'var(--text2)', marginBottom: 8,
              }}>
                <span>
                  <b style={{ color: 'var(--accent2)' }}>{aggMeta.label}</b>
                  {needsField(agg) && <> of <b style={{ color: 'var(--accent2)' }}>{field}</b></>}
                  {groupBy.length > 0 && <> · split by <b style={{ color: 'var(--accent2)' }}>{groupBy.join(' / ')}</b></>}
                  {' · '}{totalSeries} series · {totalPoints} points
                </span>
                <span style={{ flex: 1 }} />
                {/* Exemplar drill — only enabled when the filter
                    pins one service (the lookup is service-scoped
                    on CH for performance). The buttons jump to a
                    representative slow / error trace for the
                    current window — Datadog / Honeycomb / Grafana
                    pattern; saves the operator from manually
                    wading through /traces. */}
                {exemplarCtx ? (
                  <span style={{ display: 'inline-flex', gap: 6, alignItems: 'center' }}>
                    {exemplarMsg && (
                      <span style={{ color: 'var(--text3)', marginRight: 4 }}>{exemplarMsg}</span>
                    )}
                    <button className="sec"
                      onClick={() => openExemplar('slow')}
                      disabled={exemplarBusy !== null}
                      title={`Open the slowest trace in this window for ${exemplarCtx.service}${exemplarCtx.op ? ' · ' + exemplarCtx.op : ''}`}
                      style={{ fontSize: 11, padding: '2px 8px' }}>
                      {exemplarBusy === 'slow' ? 'Loading…' : 'Slowest trace →'}
                    </button>
                    <button className="sec"
                      onClick={() => openExemplar('error')}
                      disabled={exemplarBusy !== null}
                      title="Open a trace with status=error in this window"
                      style={{ fontSize: 11, padding: '2px 8px' }}>
                      {exemplarBusy === 'error' ? 'Loading…' : 'Error trace →'}
                    </button>
                  </span>
                ) : (
                  <span style={{ color: 'var(--text3)', fontStyle: 'italic' }}
                        title="Add a service.name = ... filter to enable trace drill-down">
                    add service filter to enable trace drill-down
                  </span>
                )}
              </div>
              <MultiLineChart series={series} unit={unit} deploys={deployMarkers} />
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

        {/* ── Traces mode: matching trace list ────────────────────────────────── */}
        {resultMode === 'traces' && traces === undefined && <Spinner />}
        {resultMode === 'traces' && traces && traces.length === 0 && (
          <Empty icon="⋮" title="No matching traces">
            Loosen your filters or widen the time range.
          </Empty>
        )}
        {resultMode === 'traces' && traces && traces.length > 0 && (
          <>
            <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 8 }}>
              Showing <b style={{ color: 'var(--accent2)' }}>{traces.length}</b> of {fmtNum(traceTotal)} traces
              {traces.length < traceTotal && <> · raise the limit to see more</>}
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <TraceSortTh col="traceId"     label="Trace ID"  sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    <TraceSortTh col="rootName"    label="Root"      sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    <TraceSortTh col="serviceName" label="Service"   sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    <TraceSortTh col="duration"    label="Duration"  sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} align="right" />
                    <TraceSortTh col="spans"       label="Spans"     sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} align="right" />
                    <TraceSortTh col="time"        label="Started"   sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    <TraceSortTh col="status"      label="Status"    sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                    {/* Same column-manager UX as /traces — adds
                        attribute columns to the result table. */}
                    {extraCols.map(k => (
                      <th key={k} style={{ position: 'relative', whiteSpace: 'nowrap' }}>
                        <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 11 }}>{k}</span>
                        <button type="button" title="Remove column"
                          onClick={() => setExtraCols(extraCols.filter(c => c !== k))}
                          style={{
                            marginLeft: 6, padding: '0 4px', fontSize: 10, lineHeight: 1,
                            background: 'transparent', border: 'none', color: 'var(--text3)',
                            cursor: 'pointer',
                          }}>×</button>
                      </th>
                    ))}
                    <th style={{ width: 1, whiteSpace: 'nowrap' }}>
                      <ColumnManager
                        cols={extraCols}
                        onAdd={k => { if (!extraCols.includes(k) && extraCols.length < 8) setExtraCols([...extraCols, k]); }} />
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {(sortedTraces ?? []).map(t => (
                    <tr key={t.traceId}
                        {...rowClickHandlers(`/trace?id=${t.traceId}`,
                                             () => navigate(`/trace?id=${t.traceId}`))}
                        style={{ cursor: 'pointer' }}>
                      <td className="mono">
                        <Link to={`/trace?id=${t.traceId}`}
                              onClick={e => e.stopPropagation()}
                              style={{ fontSize: 11 }}>
                          {t.traceId.slice(0, 12)}…
                        </Link>
                      </td>
                      <td><b>{t.rootName}</b></td>
                      <td className="mono" style={{ fontSize: 12 }}>{t.serviceName}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        {t.durationMs.toFixed(1)}ms
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(t.spanCount)}</td>
                      <td className="mono" style={{ fontSize: 11 }}>{tsLong(t.startTime)}</td>
                      <td>
                        {t.hasError
                          ? <span className="badge b-err">ERROR</span>
                          : <span className="badge b-ok">OK</span>}
                      </td>
                      {extraCols.map(k => {
                        const v = t.extras?.[k] ?? '';
                        return (
                          <td key={k} className="mono" style={{ fontSize: 11, color: v ? 'var(--text2)' : 'var(--text3)', whiteSpace: 'nowrap', maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis' }} title={v || ''}>
                            {v || '—'}
                          </td>
                        );
                      })}
                      <td />
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
        </>)}
      </div>
    </>
  );
}

function needsField(agg: SpanAgg): boolean {
  return !['count', 'rate', 'errors', 'error_rate'].includes(agg);
}

// extractExemplarCtx — pull (service, op) from the filter chips so
// we can light up the "Open exemplar trace" buttons. Returns null
// when no single service is pinned by an `=` filter; the backend
// requires service for the lookup to fan out cheaply across the
// (service_name, time) primary key, and a wide-open lookup would
// regress at billion-span scale. Operation is optional — when set
// it narrows the exemplar to a specific endpoint, otherwise the
// slowest span anywhere in the service wins.
function extractExemplarCtx(filters: FilterExpr[], mode: 'builder' | 'advanced'): {
  service: string;
  op?: string;
} | null {
  if (mode !== 'builder') return null;
  let service = '';
  let op: string | undefined;
  for (const f of filters) {
    // FilterExpr.v is an array; only pick single-value '=' filters
    // for unambiguous extraction. IN with one value also counts.
    if ((f.op !== '=' && f.op !== 'IN') || f.v.length !== 1) continue;
    const val = f.v[0];
    if (f.k === 'service.name' || f.k === 'resource.service.name') service = val;
    else if (f.k === 'name' || f.k === 'span.name') op = val;
  }
  if (!service) return null;
  return { service, op };
}

export default function ExplorePage() {
  return (
    <Suspense fallback={<Spinner />}>
      <ExploreInner />
    </Suspense>
  );
}

// Sortable header for the traces result table. Reuses the same .sortable
// CSS class as the /traces and /services tables for visual consistency.
function TraceSortTh({ col, label, sort, dir, onSort, align }: {
  col: TraceSortKey; label: string;
  sort: TraceSortKey; dir: 'asc' | 'desc';
  onSort: (c: TraceSortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        onClick={() => onSort(col)}
        style={{ textAlign: align ?? 'left' }}>
      {label}
      <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}
