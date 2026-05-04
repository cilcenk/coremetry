'use client';
import { useEffect, useState, Suspense } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { FilterBuilder } from '@/components/FilterBuilder';
import { api } from '@/lib/api';
import { tsShort, timeRangeToNs, fmtNum } from '@/lib/utils';
import { encodeRange, decodeRange, encodeFilters, decodeFilters, buildQuery } from '@/lib/urlState';
import type { TracesResponse, TimeRange, SortColumn, SortOrder, AggregateRow, FilterExpr } from '@/lib/types';

type View = 'list' | 'aggregate';
type GroupBy = 'operation' | 'service';
type AggSort = 'count' | 'errorRate' | 'avg' | 'p50' | 'p95' | 'p99' | 'max' | 'name';

const AGG_NATURAL: Record<AggSort, SortOrder> = {
  count: 'desc', errorRate: 'desc', avg: 'desc', p50: 'desc',
  p95: 'desc', p99: 'desc', max: 'desc', name: 'asc',
};

function TracesPageInner() {
  const router = useRouter();
  const searchParams = useSearchParams();

  // All these hydrate from URL on first render so the back button
  // restores filters / sort / page intact after viewing a trace detail.
  const [range, setRange] = useState<TimeRange>(
    () => decodeRange(searchParams.get('range'), { preset: '1h' }));
  const [view, setView] = useState<View>(
    () => (searchParams.get('view') === 'aggregate' ? 'aggregate' : 'list'));

  // List view sort
  const [sort, setSort] = useState<SortColumn>(
    () => (searchParams.get('sort') as SortColumn) || 'time');
  const [order, setOrder] = useState<SortOrder>(
    () => (searchParams.get('order') === 'asc' ? 'asc' : 'desc'));
  const [page, setPage] = useState(
    () => parseInt(searchParams.get('page') ?? '0', 10) || 0);

  // Aggregate view sort + group-by
  const [groupBy, setGroupBy] = useState<GroupBy>(
    () => (searchParams.get('groupBy') === 'service' ? 'service' : 'operation'));
  const [aggSort, setAggSort] = useState<AggSort>(
    () => (searchParams.get('aggSort') as AggSort) || 'count');
  const [aggOrder, setAggOrder] = useState<SortOrder>(
    () => (searchParams.get('aggOrder') === 'asc' ? 'asc' : 'desc'));

  const [filter, setFilter] = useState(() => ({
    service:  searchParams.get('service') ?? '',
    search:   searchParams.get('search')  ?? '',
    traceId:  searchParams.get('traceId') ?? '',
    minMs:    searchParams.get('minMs')   ?? '',
    maxMs:    searchParams.get('maxMs')   ?? '',
    hasError: searchParams.get('hasError') === 'true',
  }));
  const [draft, setDraft] = useState(filter);
  const [advFilters, setAdvFilters] = useState<FilterExpr[]>(
    () => decodeFilters(searchParams.get('filters')));
  const [data, setData] = useState<TracesResponse | null | undefined>(undefined);
  const [agg, setAgg] = useState<AggregateRow[] | null | undefined>(undefined);

  // ── State → URL ────────────────────────────────────────────────────────────
  // Mirror everything that affects the rendered list to the URL so the
  // browser back button restores the same filters / sort / page after a
  // trip into /trace/{id}. Uses replaceState so we don't pollute history.
  useEffect(() => {
    const qs = buildQuery([
      ['range',    encodeRange(range)],
      ['view',     view !== 'list' ? view : ''],
      ['sort',     sort !== 'time' ? sort : ''],
      ['order',    order !== 'desc' ? order : ''],
      ['page',     page > 0 ? page : ''],
      ['groupBy',  view === 'aggregate' && groupBy !== 'operation' ? groupBy : ''],
      ['aggSort',  view === 'aggregate' && aggSort !== 'count' ? aggSort : ''],
      ['aggOrder', view === 'aggregate' && aggOrder !== 'desc' ? aggOrder : ''],
      ['service',  filter.service],
      ['search',   filter.search],
      ['traceId',  filter.traceId],
      ['minMs',    filter.minMs],
      ['maxMs',    filter.maxMs],
      ['hasError', filter.hasError ? 'true' : ''],
      ['filters',  encodeFilters(advFilters)],
    ]);
    const next = qs ? `?${qs}` : '';
    if (typeof window !== 'undefined' && next !== window.location.search) {
      router.replace(`/traces${next}`, { scroll: false });
    }
  }, [range, view, sort, order, page, groupBy, aggSort, aggOrder, filter, advFilters, router]);

  // Autocomplete option lists
  const [services, setServices] = useState<string[]>([]);
  const [operations, setOperations] = useState<string[]>([]);

  useEffect(() => {
    api.services(timeRangeToNs(range))
      .then(svcs => setServices((svcs ?? []).map(s => s.name)))
      .catch(() => setServices([]));
  }, [range]);

  useEffect(() => {
    api.operations(draft.service, timeRangeToNs(range))
      .then(ops => setOperations(ops ?? []))
      .catch(() => setOperations([]));
  }, [draft.service, range]);

  // Total-count opt-in. Off by default for speed at scale (full DISTINCT
  // over a multi-billion-span table can take 10s+); the user can flip it
  // on with the "Show total" affordance in the pager.
  const [showTotal, setShowTotal] = useState(false);

  // ── List fetch ─────────────────────────────────────────────────────────────
  useEffect(() => {
    if (view !== 'list') return;
    setData(undefined);
    const tid = filter.traceId.trim().toLowerCase();
    const useTimeRange = tid.length === 0;
    const { from, to } = useTimeRange ? timeRangeToNs(range) : { from: undefined, to: undefined };
    api.traces({
      limit: 50, offset: page * 50, from, to,
      sort, order,
      service: filter.service || undefined,
      search: filter.search || undefined,
      traceId: tid || undefined,
      minMs: filter.minMs || undefined,
      maxMs: filter.maxMs || undefined,
      hasError: filter.hasError || undefined,
      filters: advFilters.length ? JSON.stringify(advFilters) : undefined,
      // "exact" only when the user explicitly asked. Pinned trace IDs
      // skip the toggle — count of 1 is implicit.
      count: showTotal && !tid ? 'exact' : 'skip',
    }).then(setData).catch(() => setData(null));
  }, [view, range, sort, order, page, filter, advFilters, showTotal]);

  // ── Aggregate fetch ────────────────────────────────────────────────────────
  useEffect(() => {
    if (view !== 'aggregate') return;
    setAgg(undefined);
    const { from, to } = timeRangeToNs(range);
    api.tracesAggregate({
      groupBy, sort: aggSort, order: aggOrder, limit: 200, from, to,
      service: filter.service || undefined,
      search: filter.search || undefined,
      hasError: filter.hasError || undefined,
      minMs: filter.minMs || undefined,
      maxMs: filter.maxMs || undefined,
      filters: advFilters.length ? JSON.stringify(advFilters) : undefined,
    }).then(setAgg).catch(() => setAgg(null));
  }, [view, range, groupBy, aggSort, aggOrder, filter, advFilters]);

  const apply = () => {
    const tid = draft.traceId.trim().toLowerCase();
    if (/^[0-9a-f]{32}$/.test(tid)) { router.push(`/trace?id=${tid}`); return; }
    setPage(0); setFilter(draft);
  };
  const reset = () => {
    const empty = { service: '', search: '', traceId: '', minMs: '', maxMs: '', hasError: false };
    setDraft(empty); setFilter(empty); setPage(0);
  };
  const toggleSort = (col: SortColumn) => {
    if (sort === col) setOrder(order === 'desc' ? 'asc' : 'desc');
    else { setSort(col); setOrder(col === 'service' || col === 'operation' ? 'asc' : 'desc'); }
    setPage(0);
  };
  const toggleAggSort = (col: AggSort) => {
    if (aggSort === col) setAggOrder(aggOrder === 'desc' ? 'asc' : 'desc');
    else { setAggSort(col); setAggOrder(AGG_NATURAL[col]); }
  };

  const traces = data?.traces ?? [];
  const total = data?.total;             // undefined when count was skipped
  const hasMore = data?.hasMore ?? false;

  return (
    <>
      <Topbar title="Traces" range={range} onRangeChange={setRange} />
      <div id="content">
        {/* View toggle + dedicated Trace ID lookup on the far right */}
        <div className="controls" style={{ marginBottom: 8, alignItems: 'center' }}>
          <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
            <button onClick={() => setView('list')}
              className={view === 'list' ? '' : 'sec'}
              style={{ borderRadius: 0, borderRight: '1px solid var(--border)' }}>
              Traces
            </button>
            <button onClick={() => setView('aggregate')}
              className={view === 'aggregate' ? '' : 'sec'}
              style={{ borderRadius: 0 }}>
              Aggregated
            </button>
          </div>
          {view === 'aggregate' && (
            <>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>Group by:</span>
              <select value={groupBy} onChange={e => setGroupBy(e.target.value as GroupBy)}>
                <option value="operation">Operation</option>
                <option value="service">Service</option>
              </select>
            </>
          )}

          {/* Dedicated trace-id lookup — pinned right, visually separate */}
          <div className="trace-lookup" style={{ marginLeft: 'auto' }}>
            <span className="tl-icon" aria-hidden>🔍</span>
            <input placeholder="Trace ID (full or prefix)…"
              value={draft.traceId}
              onChange={e => setDraft({ ...draft, traceId: e.target.value })}
              onKeyDown={e => e.key === 'Enter' && apply()} />
            {draft.traceId && (
              <button className="tl-clear" type="button" title="Clear"
                onClick={() => { setDraft({ ...draft, traceId: '' });
                                 setFilter({ ...filter, traceId: '' }); }}>✕</button>
            )}
            <button className="tl-go" type="button" onClick={apply}>Go</button>
          </div>
        </div>

        {/* Filters (shared between views) */}
        <div className="controls">
          <Combobox value={draft.service} onChange={v => setDraft({ ...draft, service: v })}
            options={services} placeholder="Service…" width={170} onEnter={apply} />
          <Combobox value={draft.search} onChange={v => setDraft({ ...draft, search: v })}
            options={operations} placeholder="Operation…" width={240} onEnter={apply} />
          <input placeholder="Min ms" value={draft.minMs} onChange={e => setDraft({ ...draft, minMs: e.target.value })}
            type="number" style={{ width: 72 }} />
          <input placeholder="Max ms" value={draft.maxMs} onChange={e => setDraft({ ...draft, maxMs: e.target.value })}
            type="number" style={{ width: 72 }} />
          <label style={{ display: 'flex', alignItems: 'center', gap: 5, color: 'var(--text2)', cursor: 'pointer' }}>
            <input type="checkbox" checked={draft.hasError} onChange={e => setDraft({ ...draft, hasError: e.target.checked })} />
            Errors only
          </label>
          <button onClick={apply}>Search</button>
          <button className="sec" onClick={reset}>Reset</button>
        </div>

        {/* Advanced multi-dimension filter chips (Tempo / Dynatrace style) */}
        <FilterBuilder value={advFilters} onChange={setAdvFilters}
          suggestedValues={{
            'service.name': services,
            'resource.service.name': services,
            'name': operations,
            'span.name': operations,
            'kind': ['internal', 'server', 'client', 'producer', 'consumer'],
            'status_code': ['ok', 'error', 'unset'],
            'http.method': ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
            'db.system': ['postgresql', 'mysql', 'redis', 'mongodb', 'elasticsearch'],
          }} />

        {/* List view */}
        {view === 'list' && data === undefined && <Spinner />}
        {view === 'list' && data && traces.length === 0 && <Empty icon="⋮" title="No traces found" />}
        {view === 'list' && data && traces.length > 0 && (
          <>
            <div className="table-wrap">
              <table>
                <thead><tr>
                  <SortHeader col="time"      label="Time"      sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="service"   label="Service"   sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="operation" label="Operation" sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="duration"  label="Duration"  sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="spans"     label="Spans"     sort={sort} order={order} onSort={toggleSort} />
                  <SortHeader col="status"    label="Status"    sort={sort} order={order} onSort={toggleSort} />
                </tr></thead>
                <tbody>
                  {traces.map(t => (
                    <tr key={t.traceId} onClick={() => router.push(`/trace?id=${t.traceId}`)}>
                      <td className="mono">{tsShort(t.startTime)}</td>
                      <td><SvcBadge name={t.serviceName} /></td>
                      <td title={t.rootName}>{t.rootName || '—'}</td>
                      <td className="mono">{t.durationMs.toFixed(2)}ms</td>
                      <td>{t.spanCount}</td>
                      <td>{t.hasError ? <span className="badge b-err">ERROR</span> : <span className="badge b-ok">OK</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="pager">
              <button className="sec" onClick={() => setPage(Math.max(0, page - 1))} disabled={page === 0}>← Prev</button>
              <span>
                Page {page + 1}
                {' · '}
                {total !== undefined ? (
                  <>{total.toLocaleString()} total</>
                ) : (
                  <>
                    showing {traces.length}{hasMore ? '+' : ''}
                    {' · '}
                    <a href="#" onClick={e => { e.preventDefault(); setShowTotal(true); }}
                       title="Run an exact count(DISTINCT trace_id) — can be slow at scale">
                      Show total
                    </a>
                  </>
                )}
                {' · '}sorted by <b>{sort}</b> {order}
              </span>
              <button className="sec"
                onClick={() => setPage(page + 1)}
                disabled={total !== undefined ? (page + 1) * 50 >= total : !hasMore}>
                Next →
              </button>
            </div>
          </>
        )}

        {/* Aggregate view */}
        {view === 'aggregate' && agg === undefined && <Spinner />}
        {view === 'aggregate' && agg && agg.length === 0 && <Empty icon="∑" title="No groups in this window" />}
        {view === 'aggregate' && agg && agg.length > 0 && (
          <>
            <div className="table-wrap">
              <table>
                <thead><tr>
                  <AggHeader col="name"      label={groupBy === 'service' ? 'Service' : 'Operation'} sort={aggSort} order={aggOrder} onSort={toggleAggSort} />
                  {groupBy === 'operation' && <th>Service</th>}
                  <AggHeader col="count"     label="Traces"    sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="errorRate" label="Error %"   sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="avg"       label="Avg"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="p50"       label="P50"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="p95"       label="P95"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="p99"       label="P99"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                  <AggHeader col="max"       label="Max"       sort={aggSort} order={aggOrder} onSort={toggleAggSort} align="right" />
                </tr></thead>
                <tbody>
                  {agg.map(a => {
                    const errCls = a.errorRate > 5 ? 'b-err' : a.errorRate > 0 ? 'b-warn' : 'b-ok';
                    // Click row → drill into List view filtered to this op/service
                    const onClick = () => {
                      if (groupBy === 'service') {
                        setFilter({ ...filter, service: a.groupKey });
                        setDraft({ ...draft, service: a.groupKey });
                      } else {
                        setFilter({ ...filter, search: a.groupKey, service: a.groupExtra ?? filter.service });
                        setDraft({ ...draft, search: a.groupKey, service: a.groupExtra ?? draft.service });
                      }
                      setView('list');
                      setPage(0);
                    };
                    return (
                      <tr key={`${a.groupKey}|${a.groupExtra}`} onClick={onClick}>
                        <td><b>{a.groupKey || '—'}</b></td>
                        {groupBy === 'operation' && <td><SvcBadge name={a.groupExtra ?? ''} /></td>}
                        <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(a.traceCount)}</td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <span className={`badge ${errCls}`}>{a.errorRate.toFixed(2)}%</span>
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>{a.avgMs.toFixed(1)}ms</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{a.p50Ms.toFixed(1)}ms</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{a.p95Ms.toFixed(1)}ms</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{a.p99Ms.toFixed(1)}ms</td>
                        <td className="mono" style={{ textAlign: 'right' }}>{a.maxMs.toFixed(1)}ms</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div style={{ marginTop: 10, fontSize: 12, color: 'var(--text3)' }}>
              {agg.length} groups · grouped by <b style={{ color: 'var(--accent2)' }}>{groupBy}</b> · sorted by <b>{aggSort}</b> {aggOrder} · click a row to drill down
            </div>
          </>
        )}
      </div>
    </>
  );
}

function SortHeader({ col, label, sort, order, onSort }: {
  col: SortColumn; label: string; sort: SortColumn; order: SortOrder; onSort: (c: SortColumn) => void;
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`} onClick={() => onSort(col)}>
      {label}<span className="sort-arrow">{active ? (order === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}

function AggHeader({ col, label, sort, order, onSort, align }: {
  col: AggSort; label: string; sort: AggSort; order: SortOrder;
  onSort: (c: AggSort) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        onClick={() => onSort(col)}
        style={{ textAlign: align ?? 'left' }}>
      {label}<span className="sort-arrow">{active ? (order === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}

function SvcBadge({ name }: { name: string }) {
  return (
    <span style={{ fontSize: 11, padding: '1px 6px', background: 'var(--bg3)', borderRadius: 3, fontFamily: 'monospace' }}>
      {name || 'unknown'}
    </span>
  );
}

export default function TracesPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <TracesPageInner />
    </Suspense>
  );
}
