import { Suspense, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, hashColor, tsLong } from '@/lib/utils';
import type { CallerRow, TimeRange, SortOrder } from '@/lib/types';

// Dynatrace-style "service consumers" / backtrace view. One row per
// distinct (caller service × pod/instance × client IP × user-agent)
// combination calling the inspected service, with RED stats so the
// operator can pinpoint which client is driving load or errors.

const SINCE_MAP: Record<string, string> = {
  '5m': '5m', '10m': '10m', '15m': '15m', '30m': '30m',
  '1h': '1h', '3h': '3h', '6h': '6h', '12h': '12h',
  '24h': '24h', '2d': '48h', '7d': '168h', '30d': '720h',
};

type SortKey = 'calls' | 'errorRate' | 'p50Ms' | 'p95Ms' | 'p99Ms' | 'avgMs' | 'lastSeenNs' | 'callerService';

const NATURAL: Record<SortKey, SortOrder> = {
  calls: 'desc', errorRate: 'desc', p50Ms: 'desc', p95Ms: 'desc',
  p99Ms: 'desc', avgMs: 'desc', lastSeenNs: 'desc', callerService: 'asc',
};

function BacktraceInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const svc = searchParams.get('name') ?? '';

  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [data, setData] = useState<CallerRow[] | null | undefined>(undefined);
  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState<SortKey>('calls');
  const [order, setOrder] = useState<SortOrder>('desc');

  useEffect(() => {
    if (!svc) return;
    setData(undefined);
    api.serviceBacktrace(svc, {
      since: SINCE_MAP[range.preset] ?? '1h',
      limit: 200,
    }).then(r => setData(r?.callers ?? []))
      .catch(() => setData(null));
  }, [svc, range]);

  const rows = useMemo(() => {
    if (!data) return [];
    const f = filter.trim().toLowerCase();
    const filtered = !f ? data : data.filter(r =>
      r.callerService.toLowerCase().includes(f) ||
      r.callerHost.toLowerCase().includes(f) ||
      r.callerInstance.toLowerCase().includes(f) ||
      r.clientAddress.toLowerCase().includes(f) ||
      r.userAgent.toLowerCase().includes(f)
    );
    const sorted = [...filtered].sort((a, b) => {
      const av = a[sort], bv = b[sort];
      if (typeof av === 'string' && typeof bv === 'string') {
        return order === 'asc' ? av.localeCompare(bv) : bv.localeCompare(av);
      }
      const ax = Number(av), bx = Number(bv);
      return order === 'asc' ? ax - bx : bx - ax;
    });
    return sorted;
  }, [data, filter, sort, order]);

  const toggleSort = (col: SortKey) => {
    if (sort === col) setOrder(order === 'desc' ? 'asc' : 'desc');
    else { setSort(col); setOrder(NATURAL[col]); }
  };

  // Top-line KPIs across the visible (filtered) row set so the
  // operator gets a quick "scope of inbound traffic" feel without
  // tallying calls themselves.
  const totals = useMemo(() => {
    const t = { calls: 0, errors: 0, services: new Set<string>(), instances: new Set<string>() };
    for (const r of rows) {
      t.calls += r.calls;
      t.errors += r.errors;
      t.services.add(r.callerService);
      if (r.callerHost) t.instances.add(`${r.callerService}/${r.callerHost}`);
    }
    return t;
  }, [rows]);

  if (!svc) {
    return (
      <>
        <Topbar title="Backtrace" range={range} onRangeChange={setRange} />
        <div id="content"><Empty icon="⚠" title="Missing service name" /></div>
      </>
    );
  }

  return (
    <>
      <Topbar title={`Backtrace · ${svc}`} range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14, flexWrap: 'wrap' }}>
          <Link to={`/service?name=${encodeURIComponent(svc)}`} className="sec" style={{
            padding: '5px 12px', border: '1px solid var(--border)',
            borderRadius: 6, fontSize: 12, color: 'var(--text)', textDecoration: 'none',
          }}>← Service overview</Link>
          <KPI label="Inbound calls"      value={fmtNum(totals.calls)} />
          <KPI label="Errors"             value={fmtNum(totals.errors)}
               cls={totals.calls > 0 && totals.errors / totals.calls > 0.05 ? 'err' : 'ok'} />
          <KPI label="Caller services"    value={String(totals.services.size)} />
          <KPI label="Caller instances"   value={String(totals.instances.size)} />
        </div>

        <div className="controls" style={{ marginBottom: 8 }}>
          <input placeholder="Filter by service / host / IP / user-agent…"
            value={filter} onChange={e => setFilter(e.target.value)}
            style={{ flex: 1, minWidth: 280 }} />
          {filter && <button className="sec" onClick={() => setFilter('')}>Clear</button>}
        </div>

        {data === undefined && <Spinner />}
        {data === null && <Empty icon="⚠" title="Failed to load backtrace" />}
        {data && rows.length === 0 && (
          <Empty icon="—" title={
            filter
              ? 'No callers match the filter'
              : `No inbound callers observed for ${svc} in this window`
          } />
        )}
        {data && rows.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead><tr>
                <SortHeader col="callerService" label="Caller service" sort={sort} order={order} onSort={toggleSort} />
                <th>Host / Instance</th>
                <th>Client IP / User-Agent</th>
                <SortHeader col="calls"       label="Calls"  sort={sort} order={order} onSort={toggleSort} numeric />
                <SortHeader col="errorRate"   label="Err %"  sort={sort} order={order} onSort={toggleSort} numeric />
                <SortHeader col="p50Ms"       label="p50"    sort={sort} order={order} onSort={toggleSort} numeric />
                <SortHeader col="p95Ms"       label="p95"    sort={sort} order={order} onSort={toggleSort} numeric />
                <SortHeader col="p99Ms"       label="p99"    sort={sort} order={order} onSort={toggleSort} numeric />
                <SortHeader col="lastSeenNs"  label="Last seen" sort={sort} order={order} onSort={toggleSort} />
                <th style={{ width: 1 }}></th>
              </tr></thead>
              <tbody>
                {rows.map((r, i) => {
                  const color = hashColor(r.callerService);
                  const errBad = r.errorRate >= 5;
                  const errWarn = !errBad && r.errorRate > 0;
                  return (
                    <tr key={i} style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 44px' }}>
                      <td>
                        <Link to={`/service?name=${encodeURIComponent(r.callerService)}`}
                              style={{ display: 'inline-flex', alignItems: 'center', gap: 8, color: 'var(--text)', textDecoration: 'none' }}>
                          <span style={{ width: 8, height: 8, borderRadius: '50%', background: color }} />
                          {r.callerService}
                        </Link>
                      </td>
                      <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                        <div>{r.callerHost || <em style={{ color: 'var(--text3)' }}>—</em>}</div>
                        {r.callerInstance && (
                          <div style={{ color: 'var(--text3)', fontSize: 10 }} title={r.callerInstance}>
                            {r.callerInstance.length > 36 ? r.callerInstance.slice(0, 33) + '…' : r.callerInstance}
                          </div>
                        )}
                      </td>
                      <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                        <div>{r.clientAddress || <em style={{ color: 'var(--text3)' }}>—</em>}</div>
                        {r.userAgent && (
                          <div style={{ color: 'var(--text3)', fontSize: 10 }} title={r.userAgent}>
                            {r.userAgent.length > 40 ? r.userAgent.slice(0, 37) + '…' : r.userAgent}
                          </div>
                        )}
                      </td>
                      <td className="num">{fmtNum(r.calls)}</td>
                      <td className={`num ${errBad ? 'err' : errWarn ? 'warn' : ''}`}>
                        {r.errorRate.toFixed(2)}%
                      </td>
                      <td className="num">{r.p50Ms.toFixed(1)}ms</td>
                      <td className="num">{r.p95Ms.toFixed(1)}ms</td>
                      <td className="num">{r.p99Ms.toFixed(1)}ms</td>
                      <td title={tsLong(r.lastSeenNs)} style={{ color: 'var(--text2)', fontSize: 11 }}>
                        {tsLong(r.lastSeenNs)}
                      </td>
                      <td>
                        {/* Drill-in: open the trace list filtered to traces
                            where BOTH services co-occur. ?services=A,B
                            applies a HAVING-based fan-in check on the
                            backend so we land on actual caller × callee
                            traces rather than all traces from either side.
                            view=list forces the trace list (Aggregated is
                            the default landing tab). */}
                        <Link
                          to={`/traces?services=${encodeURIComponent(r.callerService)},${encodeURIComponent(svc)}&view=list`}
                          title={`Traces where ${r.callerService} called ${svc}`}
                          style={{
                            fontSize: 11, padding: '3px 10px',
                            background: 'var(--bg3)', border: '1px solid var(--border)',
                            borderRadius: 4, color: 'var(--accent2)', textDecoration: 'none',
                          }}>
                          ⋮ Traces
                        </Link>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

function SortHeader({ col, label, sort, order, onSort, numeric }: {
  col: SortKey;
  label: string;
  sort: SortKey;
  order: SortOrder;
  onSort: (c: SortKey) => void;
  numeric?: boolean;
}) {
  const active = sort === col;
  return (
    <th onClick={() => onSort(col)}
        style={{ cursor: 'pointer', userSelect: 'none', textAlign: numeric ? 'right' : 'left' }}>
      {label}{active ? (order === 'asc' ? ' ↑' : ' ↓') : ''}
    </th>
  );
}

function KPI({ label, value, cls }: { label: string; value: string; cls?: string }) {
  return (
    <div style={{
      padding: '4px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg2)',
    }}>
      <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4 }}>{label}</div>
      <div className={cls} style={{ fontSize: 14, fontWeight: 600 }}>{value}</div>
    </div>
  );
}

export default function BacktracePage() {
  return (
    <Suspense fallback={<Spinner />}>
      <BacktraceInner />
    </Suspense>
  );
}
