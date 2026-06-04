import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { encodeRange } from '@/lib/urlState';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { Sparkline } from '@/components/Sparkline';
import type { DataTableColumn } from '@/lib/dataTable';
import type { OperationSummary, DBQueryStat, TimeRange } from '@/lib/types';

// Service Overview tables (v0.7.96) — the compact Operations + Top DB
// statements pair from the design handoff. Both use the shared
// useDataTable primitive (sortable + resizable). Operations comes from the
// already-fetched service bundle; DB statements fetch once here.

function errBadge(rate: number): string {
  return `badge ${rate > 5 ? 'b-err' : rate > 1 ? 'b-warn' : 'b-ok'}`;
}

// ── Operations (compact) ────────────────────────────────────────────────
const OP_COLS: DataTableColumn<OperationSummary>[] = [
  { id: 'name', label: 'Operation', sortValue: r => r.name, naturalDir: 'asc', width: 280 },
  { id: 'calls', label: 'Calls', sortValue: r => r.spanCount, numeric: true, width: 80 },
  { id: 'err', label: 'Err %', sortValue: r => r.errorRate, numeric: true, width: 76 },
  { id: 'p99', label: 'P99', sortValue: r => r.p99DurationMs, numeric: true, width: 80 },
  { id: 'trend', label: 'Trend', width: 96 },
];

export function OpsCard({ service, range, operations }: {
  service: string; range: TimeRange; operations: OperationSummary[];
}) {
  const rangeParam = encodeRange(range);
  const navigate = useNavigate();
  // Drill an operation into /traces (service + name pre-filtered), the same
  // destination the full Operations tab uses. onOpen also lights up j/k/Enter
  // keyboard nav (UX#4); no searchRef here — the compact card has no filter
  // input of its own.
  const opHref = (op: string) =>
    `/traces?service=${encodeURIComponent(service)}&search=${encodeURIComponent(op)}&range=${rangeParam}&view=list&rootOnly=false`;
  const dt = useDataTable<OperationSummary>({
    storageKey: 'svc-ov-ops',
    columns: OP_COLS,
    rows: operations,
    initialSort: { id: 'calls', dir: 'desc' },
    onOpen: (op) => navigate(opHref(op.name)),
  });
  return (
    <div className="card">
      <div className="ov-card-h">
        <h3>Operations</h3>
        <span className="ov-right">
          <Link className="ov-sub" to={`/service?name=${encodeURIComponent(service)}&range=${rangeParam}`}>
            View all {operations.length} →
          </Link>
        </span>
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.slice(0, 8).map((r, i) => (
              <tr key={r.name} {...dt.rowProps(i)} style={{ cursor: 'pointer' }}
                  onClick={() => navigate(opHref(r.name))}>
                <td><span className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'block' }} title={r.name}>{r.name}</span></td>
                <td className="num">{r.spanCount >= 1000 ? `${(r.spanCount / 1000).toFixed(1)}K` : r.spanCount}</td>
                <td className="num"><span className={errBadge(r.errorRate)}>{r.errorRate.toFixed(2)}%</span></td>
                <td className="num mono">{r.p99DurationMs.toFixed(0)} ms</td>
                <td><div style={{ width: 84, marginLeft: 'auto' }}><Sparkline values={r.sparkline ?? []} width={84} height={22} /></div></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ── Top DB statements (compact) ─────────────────────────────────────────
const DB_COLS: DataTableColumn<DBQueryStat>[] = [
  { id: 'stmt', label: 'Statement', sortValue: r => r.statement, naturalDir: 'asc', width: 240 },
  { id: 'calls', label: 'Calls', sortValue: r => r.count, numeric: true, width: 80 },
  { id: 'p99', label: 'P99', sortValue: r => r.p99Ms, numeric: true, width: 80 },
  { id: 'time', label: 'Time/req', sortValue: r => r.avgMs, numeric: true, width: 120 },
];

export function DbCard({ service, from, to }: { service: string; from: number; to: number }) {
  const dbQ = useQuery({
    queryKey: ['service-overview-db', service, from, to],
    queryFn: () => api.serviceDBQueries(service, { from, to, limit: 50 }),
    enabled: !!service,
    staleTime: 30_000,
  });
  const rows = useMemo(() => dbQ.data ?? [], [dbQ.data]);
  const maxTime = useMemo(() => Math.max(1, ...rows.map(r => r.avgMs)), [rows]);
  const dt = useDataTable<DBQueryStat>({
    storageKey: 'svc-ov-db',
    columns: DB_COLS,
    rows,
    initialSort: { id: 'time', dir: 'desc' },
  });
  return (
    <div className="card">
      <div className="ov-card-h"><h3>Top DB statements</h3></div>
      {rows.length === 0 ? (
        <div className="ov-card-b" style={{ color: 'var(--text2)', fontSize: 13 }}>
          {dbQ.isLoading ? 'Loading…' : `No db.statement spans for ${service} in this window.`}
        </div>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.slice(0, 8).map((r, i) => (
                <tr key={i}>
                  <td>
                    <div className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.sampleStatement || r.statement}>{r.statement}</div>
                    <div className="ov-st">{r.dbSystem}</div>
                  </td>
                  <td className="num">{r.count >= 1000 ? `${(r.count / 1000).toFixed(1)}K` : r.count}</td>
                  <td className="num mono">{r.p99Ms.toFixed(0)} ms</td>
                  <td>
                    <div className="ov-barcell">
                      <span className="mono" style={{ minWidth: 52 }}>{r.avgMs.toFixed(1)} ms</span>
                      <span className="ov-minibar"><i style={{ width: `${(r.avgMs / maxTime) * 100}%`, background: r.errorCount > 0 ? 'var(--warn)' : 'var(--teal)' }} /></span>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
