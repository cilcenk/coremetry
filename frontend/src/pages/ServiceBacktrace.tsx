import { Suspense, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, hashColor, tsLong } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { CallerRow, TimeRange } from '@/lib/types';
import { useUrlRange } from '@/lib/useUrlRange';

// Dynatrace-style "service consumers" / backtrace view. One row per
// distinct (caller service × pod/instance × client IP × user-agent)
// combination calling the inspected service, with RED stats so the
// operator can pinpoint which client is driving load or errors.

const SINCE_MAP: Record<string, string> = {
  '5m': '5m', '10m': '10m', '15m': '15m', '30m': '30m',
  '1h': '1h', '3h': '3h', '6h': '6h', '12h': '12h',
  '24h': '24h', '2d': '48h', '7d': '168h', '30d': '720h',
};

// Columns for the shared sortable + resizable DataTable.
const BACKTRACE_COLS: DataTableColumn<CallerRow>[] = [
  { id: 'callerService', label: 'Caller service', sortValue: r => r.callerService, naturalDir: 'asc', width: 200 },
  { id: 'hostInstance',  label: 'Host / Instance', width: 180 },
  { id: 'clientUa',      label: 'Client IP / User-Agent', width: 210 },
  { id: 'calls',      label: 'Calls', sortValue: r => r.calls,      numeric: true, naturalDir: 'desc', width: 90 },
  { id: 'errorRate',  label: 'Err %', sortValue: r => r.errorRate,  numeric: true, naturalDir: 'desc', width: 90 },
  { id: 'p50Ms',      label: 'p50',   sortValue: r => r.p50Ms,      numeric: true, naturalDir: 'desc', width: 80 },
  { id: 'p95Ms',      label: 'p95',   sortValue: r => r.p95Ms,      numeric: true, naturalDir: 'desc', width: 80 },
  { id: 'p99Ms',      label: 'p99',   sortValue: r => r.p99Ms,      numeric: true, naturalDir: 'desc', width: 80 },
  { id: 'lastSeenNs', label: 'Last seen', sortValue: r => r.lastSeenNs, naturalDir: 'desc', width: 160 },
  { id: 'actions',    label: '', width: 96 },
];

function BacktraceInner() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const svc = searchParams.get('name') ?? '';

  const [range, setRange] = useUrlRange('30m');
  const [data, setData] = useState<CallerRow[] | null | undefined>(undefined);
  const [filter, setFilter] = useState('');

  useEffect(() => {
    if (!svc) return;
    setData(undefined);
    api.serviceBacktrace(svc, {
      since: SINCE_MAP[range.preset] ?? '1h',
      limit: 200,
    }).then(r => setData(r?.callers ?? []))
      .catch(() => setData(null));
  }, [svc, range]);

  const filtered = useMemo(() => {
    if (!data) return [];
    const f = filter.trim().toLowerCase();
    return !f ? data : data.filter(r =>
      r.callerService.toLowerCase().includes(f) ||
      r.callerHost.toLowerCase().includes(f) ||
      r.callerInstance.toLowerCase().includes(f) ||
      r.clientAddress.toLowerCase().includes(f) ||
      r.userAgent.toLowerCase().includes(f)
    );
  }, [data, filter]);

  // Shared sortable + resizable table over the filtered callers.
  const dt = useDataTable<CallerRow>({
    storageKey: 'backtrace', columns: BACKTRACE_COLS,
    rows: filtered, initialSort: { id: 'calls', dir: 'desc' },
  });

  // Top-line KPIs across the visible (filtered) row set so the
  // operator gets a quick "scope of inbound traffic" feel without
  // tallying calls themselves.
  const totals = useMemo(() => {
    const t = { calls: 0, errors: 0, services: new Set<string>(), instances: new Set<string>() };
    for (const r of filtered) {
      t.calls += r.calls;
      t.errors += r.errors;
      t.services.add(r.callerService);
      if (r.callerHost) t.instances.add(`${r.callerService}/${r.callerHost}`);
    }
    return t;
  }, [filtered]);

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
            aria-label="Filter backtrace by service, host, IP, or user-agent"
            value={filter} onChange={e => setFilter(e.target.value)}
            style={{ flex: 1, minWidth: 280 }} />
          {filter && <button className="sec" onClick={() => setFilter('')}>Clear</button>}
        </div>

        {data === undefined && <Spinner />}
        {data === null && <Empty icon="⚠" title="Failed to load backtrace" />}
        {data && filtered.length === 0 && (
          <Empty icon="—" title={
            filter
              ? 'No callers match the filter'
              : `No inbound callers observed for ${svc} in this window`
          } />
        )}
        {data && filtered.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map((r, i) => {
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
                            v0.7.42 — Operator-reported: these traces were
                            hidden because /traces defaults rootOnly=ON, but a
                            caller→callee hop is mid-trace, not a root span.
                            Force rootOnly=false so the co-occurring traces
                            actually show. (view=list is the default tab since
                            v0.7.37 but kept explicit.) */}
                        <Link
                          to={`/traces?services=${encodeURIComponent(r.callerService)},${encodeURIComponent(svc)}&view=list&rootOnly=false`}
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
