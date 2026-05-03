'use client';
import { useEffect, useMemo, useState } from 'react';
import { useRouter } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import type { Service, TimeRange } from '@/lib/types';

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
  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
  const [data, setData] = useState<Service[] | null | undefined>(undefined);
  const [sortBy, setSortBy] = useState<SortKey>('errorRate');
  const [sortDir, setSortDir] = useState<SortDir>('desc');

  // Filters (in-memory — service list is small)
  const [serviceFilter, setServiceFilter] = useState('');
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [minSpans, setMinSpans] = useState('');
  const [minP99, setMinP99] = useState('');

  useEffect(() => {
    setData(undefined);
    api.services(timeRangeToNs(range)).then(setData).catch(() => setData(null));
  }, [range]);

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
                  {sorted.map(s => {
                    const errCls = s.errorRate > 5 ? 'err' : s.errorRate > 0 ? 'warn' : 'ok';
                    return (
                      <tr key={s.name} onClick={() => goToService(s.name)}>
                        <td>
                          <span style={{ fontWeight: 600 }}>{s.name}</span>
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          {fmtNum(s.spanCount)}
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <span className={`badge b-${errCls === 'err' ? 'err' : errCls === 'warn' ? 'warn' : 'ok'}`}>
                            {s.errorRate.toFixed(2)}%
                          </span>
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          {s.avgDurationMs.toFixed(1)}ms
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          {s.p99DurationMs.toFixed(1)}ms
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
