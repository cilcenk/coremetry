'use client';
import { useEffect, useMemo, useState } from 'react';
import { useRouter } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { Sparkline } from '@/components/Sparkline';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs, rowClickHandlers } from '@/lib/utils';
import { encodeRange, encodeFilters, buildQuery } from '@/lib/urlState';
import type { Service, SparklineBucket, TimeRange, SpanAgg } from '@/lib/types';

type SortKey = 'name' | 'spanCount' | 'errorRate' | 'avg' | 'p99' | 'apdex';
type SortDir = 'asc' | 'desc';

// Each column's natural starting direction when first selected.
// Apdex is a satisfaction score so 'asc' surfaces the WORST services first.
const NATURAL_DIR: Record<SortKey, SortDir> = {
  name: 'asc',
  spanCount: 'desc',
  errorRate: 'desc',
  avg: 'desc',
  p99: 'desc',
  apdex: 'asc',
};

export default function ServicesPage() {
  const router = useRouter();
  const [range, setRange] = useState<TimeRange>({ preset: '1h' });
  const [data, setData] = useState<Service[] | null | undefined>(undefined);
  const [sparklines, setSparklines] = useState<Record<string, SparklineBucket[]>>({});
  const [sortBy, setSortBy] = useState<SortKey>('errorRate');
  const [sortDir, setSortDir] = useState<SortDir>('desc');
  // Top-N cap. 50 is the chart-wide default and covers ~95% of installs;
  // the "Load all" link bumps it to 5000 for envs with a long tail.
  const [limit, setLimit] = useState<number>(50);

  // Filters (in-memory — service list is small)
  const [serviceFilter, setServiceFilter] = useState('');
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [minSpans, setMinSpans] = useState('');
  const [minP99, setMinP99] = useState('');

  useEffect(() => {
    setData(undefined);
    setSparklines({});
    const r = timeRangeToNs(range);
    // Two-phase fetch: get the services list first, then ask for
    // sparklines scoped to ONLY those names. Without the scope the
    // sparklines payload is one bucket array per service across all of
    // them — multi-MB at 10k+ services. Sparkline call is fire-and-forget
    // (non-blocking) so the table renders even if the MV is empty.
    api.services(r, limit).then(svcs => {
      setData(svcs);
      const names = (svcs ?? []).map(s => s.name);
      if (names.length > 0) {
        api.serviceSparklines(r, names).then(d => setSparklines(d ?? {})).catch(() => {});
      }
    }).catch(() => setData(null));
  }, [range, limit]);

  // Service combobox options come from the loaded data itself.
  const serviceOptions = useMemo(
    () => (data ?? []).map(s => s.name).sort(),
    [data]
  );

  // Apply filters → sort
  const sorted = useMemo(() => {
    if (!data) return data;
    const minS = parseFloat(minSpans);
    const minP = parseFloat(minP99);
    const term = serviceFilter.trim().toLowerCase();
    const filtered = data.filter(s => {
      if (term && !s.name.toLowerCase().includes(term)) return false;
      if (errorsOnly && !(s.errorCount > 0 || s.errorRate > 0)) return false;
      if (!isNaN(minS) && s.spanCount < minS) return false;
      if (!isNaN(minP) && s.p99DurationMs < minP) return false;
      return true;
    });
    const cmp = (a: Service, b: Service): number => {
      switch (sortBy) {
        case 'name':      return a.name.localeCompare(b.name);
        case 'spanCount': return a.spanCount - b.spanCount;
        case 'errorRate': return a.errorRate - b.errorRate;
        case 'avg':       return a.avgDurationMs - b.avgDurationMs;
        case 'p99':       return a.p99DurationMs - b.p99DurationMs;
        case 'apdex':     return (a.apdex ?? 0) - (b.apdex ?? 0);
      }
    };
    const arr = [...filtered].sort(cmp);
    return sortDir === 'desc' ? arr.reverse() : arr;
  }, [data, sortBy, sortDir, serviceFilter, errorsOnly, minSpans, minP99]);

  const reset = () => {
    setServiceFilter(''); setErrorsOnly(false); setMinSpans(''); setMinP99('');
  };
  const totalCount = data?.length ?? 0;

  // Aggregate row across the currently-visible (filtered) services.
  // Span count → sum. Error rate / avg / apdex → weighted by span count
  // so a chatty service with low latency doesn't drag the headline down
  // when a quiet but slow service is the actual outlier. P99 is the
  // max across services — there's no meaningful "average P99".
  const agg = useMemo(() => {
    if (!sorted || sorted.length === 0) return null;
    let totalSpans = 0, totalErrs = 0;
    let wAvg = 0, wApdex = 0, maxP99 = 0;
    for (const s of sorted) {
      totalSpans += s.spanCount;
      totalErrs += s.errorCount;
      wAvg += s.avgDurationMs * s.spanCount;
      wApdex += (s.apdex ?? 0) * s.spanCount;
      if (s.p99DurationMs > maxP99) maxP99 = s.p99DurationMs;
    }
    const avgMs = totalSpans > 0 ? wAvg / totalSpans : 0;
    const apdex = totalSpans > 0 ? wApdex / totalSpans : 0;
    const errorRate = totalSpans > 0 ? (totalErrs / totalSpans) * 100 : 0;
    return { spans: totalSpans, errs: totalErrs, errorRate, avgMs, p99Ms: maxP99, apdex };
  }, [sorted]);

  // Aggregate sparkline buckets — sum spans/errs across visible services
  // per timestamp; avgMs / p99Ms become weighted-by-spans / max so the
  // mini-chart stays representative.
  const aggBuckets = useMemo(() => {
    if (!sorted || sorted.length === 0) return [] as { t: number; spans: number; errs: number; avgMs: number; p99Ms: number }[];
    const merged = new Map<number, { spans: number; errs: number; avgWeighted: number; p99Max: number }>();
    for (const s of sorted) {
      for (const b of (sparklines[s.name] ?? [])) {
        const cur = merged.get(b.t) ?? { spans: 0, errs: 0, avgWeighted: 0, p99Max: 0 };
        cur.spans += b.spans;
        cur.errs += b.errs;
        cur.avgWeighted += b.avgMs * b.spans;
        if (b.p99Ms > cur.p99Max) cur.p99Max = b.p99Ms;
        merged.set(b.t, cur);
      }
    }
    return Array.from(merged.entries())
      .sort((a, b) => a[0] - b[0])
      .map(([t, v]) => ({
        t,
        spans: v.spans,
        errs: v.errs,
        avgMs: v.spans > 0 ? v.avgWeighted / v.spans : 0,
        p99Ms: v.p99Max,
      }));
  }, [sorted, sparklines]);

  const toggleSort = (col: SortKey) => {
    if (sortBy === col) {
      setSortDir(sortDir === 'desc' ? 'asc' : 'desc');
    } else {
      setSortBy(col);
      setSortDir(NATURAL_DIR[col]);
    }
  };

  const goToService = (svc: string) =>
    router.push(`/service?name=${encodeURIComponent(svc)}`);

  // Click-through from a sparkline → Explore, pre-filtered to the
  // service and aggregating the matching metric. Stops row propagation
  // so the user doesn't end up on /service when they meant to drill
  // into the chart. Empty `svc` (clicked from the aggregate row) opens
  // Explore unfiltered so the user sees the same totals.
  const goToExplore = (svc: string, agg: SpanAgg) => {
    const filters = svc
      ? encodeFilters([{ k: 'service.name', op: '=', v: [svc] }])
      : '';
    const q = buildQuery([
      ['range', encodeRange(range)],
      ['filters', filters],
      ['agg', agg],
      ['field', 'duration_ms'],
      ['result', 'metric'],
    ]);
    router.push(`/explore?${q}`);
  };

  return (
    <>
      <Topbar title="Services" range={range} onRangeChange={setRange} />
      <div id="content">
        {data && data.length > 0 && (
          <div className="controls">
            <Combobox value={serviceFilter} onChange={setServiceFilter}
              options={serviceOptions} placeholder="Service…" width={200} />
            <input placeholder="Min spans" value={minSpans} type="number"
              onChange={e => setMinSpans(e.target.value)} style={{ width: 100 }} />
            <input placeholder="Min P99 (ms)" value={minP99} type="number"
              onChange={e => setMinP99(e.target.value)} style={{ width: 110 }} />
            <label style={{ display: 'flex', alignItems: 'center', gap: 5,
                            color: 'var(--text2)', cursor: 'pointer' }}>
              <input type="checkbox" checked={errorsOnly}
                onChange={e => setErrorsOnly(e.target.checked)} />
              Errors only
            </label>
            <button className="sec" onClick={reset}>Reset</button>
            <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
              {sorted?.length ?? 0} / {totalCount} services
              {data && data.length >= limit && limit < 5000 && (
                <>
                  {' · '}
                  <a href="#" onClick={e => { e.preventDefault(); setLimit(5000); }}
                     title="Loaded the top 50 by span count — fetch the long tail too (slower)">
                    Load all
                  </a>
                </>
              )}
            </span>
          </div>
        )}

        {data === undefined && <Spinner />}
        {data !== undefined && (!data || data.length === 0) && (
          <Empty icon="⬡" title="No services yet">
            Point your OTLP exporter at the collector — <code>OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14318</code> (HTTP) or <code>:14317</code> (gRPC).
          </Empty>
        )}
        {data && data.length > 0 && sorted && sorted.length === 0 && (
          <Empty icon="⬡" title="No services match the current filters" />
        )}
        {sorted && sorted.length > 0 && (
          <>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <SortTh col="name"      label="Service"    sort={sortBy} dir={sortDir} onSort={toggleSort} />
                    <SortTh col="spanCount" label="Spans"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                    <SortTh col="errorRate" label="Error rate" sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                    <SortTh col="avg"       label="Avg"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                    <SortTh col="p99"       label="P99"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                    <SortTh col="apdex"     label="Apdex"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                  </tr>
                </thead>
                <tbody>
                  {agg && (
                    <tr className="agg-row">
                      <td><span style={{ fontWeight: 700, color: 'var(--text)' }}>All ({sorted.length})</span></td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={fmtNum(agg.spans)}
                                   spark={aggBuckets.map(b => b.spans)}
                                   color="var(--accent2)"
                                   title="Total spans/5m across visible services"
                                   onClick={() => goToExplore('', 'count')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={
                          <span className={`badge b-${agg.errorRate > 5 ? 'err' : agg.errorRate > 0 ? 'warn' : 'ok'}`}>
                            {agg.errorRate.toFixed(2)}%
                          </span>
                        }
                        spark={aggBuckets.map(b => b.spans > 0 ? (b.errs / b.spans) * 100 : 0)}
                        color="var(--err)"
                        title="Aggregate error rate (weighted by spans)"
                        onClick={() => goToExplore('', 'error_rate')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={`${agg.avgMs.toFixed(1)}ms`}
                                   spark={aggBuckets.map(b => b.avgMs)}
                                   color="var(--accent)"
                                   title="Aggregate avg latency (weighted by spans)"
                                   onClick={() => goToExplore('', 'avg')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={`${agg.p99Ms.toFixed(1)}ms`}
                                   spark={aggBuckets.map(b => b.p99Ms)}
                                   color="var(--warn)"
                                   title="Worst-service P99 in each bucket"
                                   onClick={() => goToExplore('', 'p99')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <ApdexBadge value={agg.apdex} />
                      </td>
                    </tr>
                  )}
                  {sorted.map(s => {
                    const errCls = s.errorRate > 5 ? 'err' : s.errorRate > 0 ? 'warn' : 'ok';
                    const buckets = sparklines[s.name] ?? [];
                    return (
                      <tr key={s.name}
                          {...rowClickHandlers(`/service?name=${encodeURIComponent(s.name)}`,
                                               () => goToService(s.name))}>
                        <td>
                          <span style={{ fontWeight: 600 }}>{s.name}</span>
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={fmtNum(s.spanCount)}
                                     spark={buckets.map(b => b.spans)}
                                     color="var(--accent2)"
                                     title={`Spans/5m for ${s.name}`}
                                     onClick={() => goToExplore(s.name, 'count')} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={
                            <span className={`badge b-${errCls === 'err' ? 'err' : errCls === 'warn' ? 'warn' : 'ok'}`}>
                              {s.errorRate.toFixed(2)}%
                            </span>
                          }
                          spark={buckets.map(b => b.spans > 0 ? (b.errs / b.spans) * 100 : 0)}
                          color="var(--err)"
                          title={`Error rate (%) for ${s.name}`}
                          onClick={() => goToExplore(s.name, 'error_rate')} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={`${s.avgDurationMs.toFixed(1)}ms`}
                                     spark={buckets.map(b => b.avgMs)}
                                     color="var(--accent)"
                                     title={`Avg latency (ms) for ${s.name}`}
                                     onClick={() => goToExplore(s.name, 'avg')} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={`${s.p99DurationMs.toFixed(1)}ms`}
                                     spark={buckets.map(b => b.p99Ms)}
                                     color="var(--warn)"
                                     title={`P99 latency (ms) for ${s.name}`}
                                     onClick={() => goToExplore(s.name, 'p99')} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <ApdexBadge value={s.apdex} />
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div style={{ marginTop: 10, fontSize: 12, color: 'var(--text3)' }}>
              {sorted.length} services · sorted by <b style={{ color: 'var(--accent2)' }}>{sortBy}</b> {sortDir}
            </div>
          </>
        )}
      </div>
    </>
  );
}

// SparkCell renders the existing numeric value next to a small inline
// sparkline. The sparkline area swallows the row click so the user
// drills into Explore with the right aggregation pre-selected, instead
// of navigating to /service.
function SparkCell({
  value, spark, color, title, onClick,
}: {
  value: React.ReactNode;
  spark: number[];
  color: string;
  title: string;
  onClick: () => void;
}) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, justifyContent: 'flex-end' }}>
      <span>{value}</span>
      <span
        onClick={e => { e.stopPropagation(); onClick(); }}
        title={title}
        style={{ display: 'inline-block' }}
      >
        <Sparkline values={spark} color={color} title={title} />
      </span>
    </span>
  );
}

// Apdex score → coloured badge.
//   ≥ 0.94  Excellent (ok)
//   ≥ 0.85  Good (info)
//   ≥ 0.70  Fair (warn)
//   <  0.70 Poor (err)
function ApdexBadge({ value }: { value: number }) {
  if (value == null || isNaN(value)) return <span style={{ color: 'var(--text3)' }}>—</span>;
  const cls = value >= 0.94 ? 'b-ok'
            : value >= 0.85 ? 'b-info'
            : value >= 0.70 ? 'b-warn'
            : 'b-err';
  return <span className={`badge ${cls}`}>{value.toFixed(2)}</span>;
}

function SortTh({ col, label, sort, dir, onSort, align }: {
  col: SortKey; label: string;
  sort: SortKey; dir: SortDir;
  onSort: (c: SortKey) => void;
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
